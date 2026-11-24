// Package qdrant implementa el puerto storage.VectorStore sobre Qdrant usando
// el cliente oficial de Go (gRPC). Toda la dependencia del SDK queda confinada
// en este paquete: el resto del código solo conoce internal/storage.
package qdrant

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	qdr "github.com/qdrant/go-client/qdrant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"mar-backend/internal/storage"
)

// defaultPort es el puerto gRPC por defecto de Qdrant.
const defaultPort = 6334

// upsertChunk acota el número de puntos por operación de escritura.
const upsertChunk = 128

// Config agrupa los parámetros de conexión del adaptador.
type Config struct {
	// URL admite "host", "host:puerto" o "esquema://host:puerto".
	URL            string
	APIKey         string
	UseTLS         bool
	RequestTimeout time.Duration
	// PoolSize es el número de conexiones gRPC. 0 usa una única conexión.
	PoolSize uint
}

// Store es el adaptador de Qdrant.
type Store struct {
	client     *qdr.Client
	reqTimeout time.Duration
}

// Compila la comprobación de que el adaptador cumple el puerto.
var _ storage.VectorStore = (*Store)(nil)

// New crea el cliente de Qdrant. No bloquea: la conexión gRPC es perezosa, de
// modo que un Qdrant no disponible no impide arrancar el servicio; la
// disponibilidad se refleja en el health check.
func New(cfg Config) (*Store, error) {
	host, port, tlsFromURL, err := parseEndpoint(cfg.URL)
	if err != nil {
		return nil, err
	}

	pool := cfg.PoolSize
	if pool == 0 {
		pool = 1
	}

	client, err := qdr.NewClient(&qdr.Config{
		Host:   host,
		Port:   port,
		APIKey: cfg.APIKey,
		UseTLS: cfg.UseTLS || tlsFromURL,
		// Una sola conexión evita repartir peticiones entre varias conexiones
		// sin necesidad en el entorno actual.
		PoolSize: pool,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: crear cliente: %w", err)
	}

	reqTimeout := cfg.RequestTimeout
	if reqTimeout <= 0 {
		reqTimeout = 30 * time.Second
	}

	return &Store{client: client, reqTimeout: reqTimeout}, nil
}

// Close libera las conexiones del cliente.
func (s *Store) Close() error {
	return s.client.Close()
}

// Ping comprueba la disponibilidad del servidor.
func (s *Store) Ping(ctx context.Context) error {
	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.HealthCheck(c); err != nil {
		return fmt.Errorf("qdrant: health check: %w", err)
	}
	return nil
}

// EnsureCollection crea la colección de forma idempotente. Si ya existe,
// valida que su esquema coincida con el configurado; si no coincide devuelve
// storage.ErrIncompatibleCollection sin modificar ni eliminar nada.
func (s *Store) EnsureCollection(ctx context.Context, spec storage.CollectionSpec) (storage.CollectionInfo, error) {
	if err := validateSpec(spec); err != nil {
		return storage.CollectionInfo{}, err
	}

	raw, exists, err := s.getCollection(ctx, spec.Name)
	if err != nil {
		return storage.CollectionInfo{}, err
	}

	if !exists {
		if err := s.createCollection(ctx, spec); err != nil {
			return storage.CollectionInfo{}, err
		}
		raw, _, err = s.getCollection(ctx, spec.Name)
		if err != nil {
			return storage.CollectionInfo{}, err
		}
	}

	if err := checkCompatible(spec, raw); err != nil {
		return storage.CollectionInfo{}, err
	}
	if err := s.ensureIndexes(ctx, spec, raw); err != nil {
		return storage.CollectionInfo{}, err
	}

	return toCollectionInfo(spec.Name, raw), nil
}

// Upsert inserta o actualiza puntos por lotes. Es idempotente siempre que los
// identificadores sean deterministas.
func (s *Store) Upsert(ctx context.Context, collection string, points []storage.VectorPoint) error {
	if err := requireCollection(collection); err != nil {
		return err
	}
	if len(points) == 0 {
		return nil
	}

	for start := 0; start < len(points); start += upsertChunk {
		end := min(start+upsertChunk, len(points))

		batch := make([]*qdr.PointStruct, 0, end-start)
		for _, p := range points[start:end] {
			pb, err := toPointStruct(p)
			if err != nil {
				return err
			}
			batch = append(batch, pb)
		}

		c, cancel := s.withTimeout(ctx)
		_, err := s.client.Upsert(c, &qdr.UpsertPoints{
			CollectionName: collection,
			Points:         batch,
			Wait:           qdr.PtrOf(true),
		})
		cancel()
		if err != nil {
			return fmt.Errorf("qdrant: upsert en %q: %w", collection, err)
		}
	}
	return nil
}

// Get recupera puntos por identificador.
func (s *Store) Get(ctx context.Context, collection string, ids []string) ([]storage.VectorPoint, error) {
	if err := requireCollection(collection); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}

	pbIDs := make([]*qdr.PointId, 0, len(ids))
	for _, id := range ids {
		pid, err := toPointID(id)
		if err != nil {
			return nil, err
		}
		pbIDs = append(pbIDs, pid)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	found, err := s.client.Get(c, &qdr.GetPoints{
		CollectionName: collection,
		Ids:            pbIDs,
		WithPayload:    qdr.NewWithPayload(true),
		WithVectors:    qdr.NewWithVectors(true),
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant: get en %q: %w", collection, err)
	}

	points := make([]storage.VectorPoint, 0, len(found))
	for _, rp := range found {
		points = append(points, fromRetrieved(rp))
	}
	return points, nil
}

// Search devuelve los puntos más similares al vector de consulta. El filtro de
// aislamiento por tenant es responsabilidad de quien llama.
func (s *Store) Search(ctx context.Context, collection string, query storage.SearchQuery) ([]storage.ScoredPoint, error) {
	if err := requireCollection(collection); err != nil {
		return nil, err
	}
	if len(query.Vector) == 0 {
		return nil, fmt.Errorf("%w: vector de consulta vacío", storage.ErrInvalidArgument)
	}

	limit := query.Limit
	if limit == 0 {
		limit = 10
	}

	req := &qdr.QueryPoints{
		CollectionName: collection,
		Query:          qdr.NewQueryDense(query.Vector),
		Limit:          &limit,
		WithPayload:    qdr.NewWithPayload(query.WithPayload),
	}
	if query.VectorName != "" {
		req.Using = &query.VectorName
	}
	if query.Filter != nil {
		req.Filter = toFilter(*query.Filter)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	hits, err := s.client.Query(c, req)
	if err != nil {
		return nil, fmt.Errorf("qdrant: búsqueda en %q: %w", collection, err)
	}

	results := make([]storage.ScoredPoint, 0, len(hits))
	for _, h := range hits {
		results = append(results, storage.ScoredPoint{
			ID:      pointIDString(h.GetId()),
			Score:   h.GetScore(),
			Payload: fromPayload(h.GetPayload()),
		})
	}
	return results, nil
}

// DeleteByIDs elimina puntos por identificador.
func (s *Store) DeleteByIDs(ctx context.Context, collection string, ids []string) error {
	if err := requireCollection(collection); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	pbIDs := make([]*qdr.PointId, 0, len(ids))
	for _, id := range ids {
		pid, err := toPointID(id)
		if err != nil {
			return err
		}
		pbIDs = append(pbIDs, pid)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.Delete(c, &qdr.DeletePoints{
		CollectionName: collection,
		Points:         qdr.NewPointsSelectorIDs(pbIDs),
		Wait:           qdr.PtrOf(true),
	}); err != nil {
		return fmt.Errorf("qdrant: borrado por id en %q: %w", collection, err)
	}
	return nil
}

// DeleteByFilter elimina los puntos que cumplen el filtro. Devuelve cuántos
// puntos coincidían. Qdrant no devuelve el número de eliminados, por lo que se
// cuenta antes de borrar (puede haber una carrera mínima con escrituras
// concurrentes).
func (s *Store) DeleteByFilter(ctx context.Context, collection string, filter storage.Filter) (uint64, error) {
	if err := requireCollection(collection); err != nil {
		return 0, err
	}

	affected, err := s.Count(ctx, collection, &filter)
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, nil
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.Delete(c, &qdr.DeletePoints{
		CollectionName: collection,
		Points:         qdr.NewPointsSelectorFilter(toFilter(filter)),
		Wait:           qdr.PtrOf(true),
	}); err != nil {
		return 0, fmt.Errorf("qdrant: borrado por filtro en %q: %w", collection, err)
	}
	return affected, nil
}

// Scroll enumera puntos de forma paginada.
func (s *Store) Scroll(ctx context.Context, collection string, query storage.ScrollQuery) (storage.ScrollResult, error) {
	if err := requireCollection(collection); err != nil {
		return storage.ScrollResult{}, err
	}

	limit := query.Limit
	if limit == 0 {
		limit = 100
	}

	req := &qdr.ScrollPoints{
		CollectionName: collection,
		Limit:          &limit,
		WithPayload:    qdr.NewWithPayload(query.WithPayload),
	}
	if query.Filter != nil {
		req.Filter = toFilter(*query.Filter)
	}
	if strings.TrimSpace(query.Offset) != "" {
		offset, err := toPointID(query.Offset)
		if err != nil {
			return storage.ScrollResult{}, err
		}
		req.Offset = offset
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	points, next, err := s.client.ScrollAndOffset(c, req)
	if err != nil {
		return storage.ScrollResult{}, fmt.Errorf("qdrant: scroll en %q: %w", collection, err)
	}

	result := storage.ScrollResult{Points: make([]storage.VectorPoint, 0, len(points))}
	for _, rp := range points {
		result.Points = append(result.Points, fromRetrieved(rp))
	}
	if next != nil {
		result.NextOffset = pointIDString(next)
	}
	return result, nil
}

// Count cuenta los puntos que cumplen el filtro.
func (s *Store) Count(ctx context.Context, collection string, filter *storage.Filter) (uint64, error) {
	if err := requireCollection(collection); err != nil {
		return 0, err
	}

	req := &qdr.CountPoints{
		CollectionName: collection,
		Exact:          qdr.PtrOf(true),
	}
	if filter != nil {
		req.Filter = toFilter(*filter)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	total, err := s.client.Count(c, req)
	if err != nil {
		return 0, fmt.Errorf("qdrant: contar en %q: %w", collection, err)
	}
	return total, nil
}

// --- internos ---

func (s *Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.reqTimeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.reqTimeout)
}

// getCollection devuelve la configuración de la colección y si existe.
func (s *Store) getCollection(ctx context.Context, name string) (*qdr.CollectionInfo, bool, error) {
	c, cancel := s.withTimeout(ctx)
	defer cancel()

	info, err := s.client.GetCollectionInfo(c, name)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("qdrant: obtener colección %q: %w", name, err)
	}
	return info, true, nil
}

func (s *Store) createCollection(ctx context.Context, spec storage.CollectionSpec) error {
	var vectorsConfig *qdr.VectorsConfig
	if vp, ok := spec.Vectors[""]; ok && len(spec.Vectors) == 1 {
		// Vector único sin nombre (vector por defecto de la colección).
		vectorsConfig = qdr.NewVectorsConfig(toVectorParams(vp))
	} else {
		named := make(map[string]*qdr.VectorParams, len(spec.Vectors))
		for name, vp := range spec.Vectors {
			named[name] = toVectorParams(vp)
		}
		vectorsConfig = qdr.NewVectorsConfigMap(named)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if err := s.client.CreateCollection(c, &qdr.CreateCollection{
		CollectionName: spec.Name,
		VectorsConfig:  vectorsConfig,
	}); err != nil {
		return fmt.Errorf("qdrant: crear colección %q: %w", spec.Name, err)
	}
	return nil
}

// ensureIndexes crea los índices de payload que falten. Es idempotente: se
// consulta el esquema actual antes de crear cada índice.
func (s *Store) ensureIndexes(ctx context.Context, spec storage.CollectionSpec, raw *qdr.CollectionInfo) error {
	if len(spec.Indexes) == 0 {
		return nil
	}

	existing := raw.GetPayloadSchema()
	for _, idx := range spec.Indexes {
		if _, ok := existing[idx.Field]; ok {
			continue
		}

		fieldType := toFieldType(idx.Type)

		c, cancel := s.withTimeout(ctx)
		_, err := s.client.CreateFieldIndex(c, &qdr.CreateFieldIndexCollection{
			CollectionName: spec.Name,
			FieldName:      idx.Field,
			FieldType:      &fieldType,
			Wait:           qdr.PtrOf(true),
		})
		cancel()
		if err != nil {
			return fmt.Errorf("qdrant: crear índice de payload %q en %q: %w", idx.Field, spec.Name, err)
		}
	}
	return nil
}

func validateSpec(spec storage.CollectionSpec) error {
	if strings.TrimSpace(spec.Name) == "" {
		return fmt.Errorf("%w: nombre de colección vacío", storage.ErrInvalidArgument)
	}
	if len(spec.Vectors) == 0 {
		return fmt.Errorf("%w: la colección %q no define vectores", storage.ErrInvalidArgument, spec.Name)
	}
	for name, vp := range spec.Vectors {
		if vp.Size == 0 {
			return fmt.Errorf("%w: el vector %q de %q no define dimensión",
				storage.ErrInvalidArgument, displayVectorName(name), spec.Name)
		}
	}
	return nil
}

// checkCompatible compara el esquema esperado con el real sin modificar nada.
func checkCompatible(spec storage.CollectionSpec, raw *qdr.CollectionInfo) error {
	actual := toCollectionInfo(spec.Name, raw)

	var problems []string
	for name, want := range spec.Vectors {
		gotSize, ok := actual.VectorSizes[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("falta el vector %q", displayVectorName(name)))
			continue
		}
		if gotSize != want.Size {
			problems = append(problems, fmt.Sprintf(
				"el vector %q tiene dimensión %d y se esperaba %d",
				displayVectorName(name), gotSize, want.Size))
		}
		if want.Distance != storage.DistanceUnspecified {
			if gotDist, ok := actual.VectorDistances[name]; ok && gotDist != want.Distance {
				problems = append(problems, fmt.Sprintf(
					"el vector %q usa distancia %s y se esperaba %s",
					displayVectorName(name), distanceName(gotDist), distanceName(want.Distance)))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: colección %q: %s. No se modifica ni se elimina; revise la configuración o reindexe en una colección nueva",
			storage.ErrIncompatibleCollection, spec.Name, strings.Join(problems, "; "))
	}
	return nil
}

func toCollectionInfo(name string, raw *qdr.CollectionInfo) storage.CollectionInfo {
	info := storage.CollectionInfo{
		Name:            name,
		VectorSizes:     map[string]uint64{},
		VectorDistances: map[string]storage.Distance{},
	}

	params := raw.GetConfig().GetParams()
	if params == nil {
		return info
	}
	vc := params.GetVectorsConfig()
	if vc == nil {
		return info
	}

	if p := vc.GetParams(); p != nil {
		info.VectorSizes[""] = p.GetSize()
		info.VectorDistances[""] = fromDistance(p.GetDistance())
	}
	for vecName, vp := range vc.GetParamsMap().GetMap() {
		info.VectorSizes[vecName] = vp.GetSize()
		info.VectorDistances[vecName] = fromDistance(vp.GetDistance())
	}
	return info
}

func toVectorParams(vp storage.VectorParams) *qdr.VectorParams {
	distance := qdr.Distance_Cosine
	switch vp.Distance {
	case storage.DistanceEuclid:
		distance = qdr.Distance_Euclid
	case storage.DistanceDot:
		distance = qdr.Distance_Dot
	case storage.DistanceManhattan:
		distance = qdr.Distance_Manhattan
	case storage.DistanceCosine, storage.DistanceUnspecified:
		distance = qdr.Distance_Cosine
	}
	return &qdr.VectorParams{Size: vp.Size, Distance: distance}
}

func fromDistance(d qdr.Distance) storage.Distance {
	switch d {
	case qdr.Distance_Cosine:
		return storage.DistanceCosine
	case qdr.Distance_Euclid:
		return storage.DistanceEuclid
	case qdr.Distance_Dot:
		return storage.DistanceDot
	case qdr.Distance_Manhattan:
		return storage.DistanceManhattan
	default:
		return storage.DistanceUnspecified
	}
}

func distanceName(d storage.Distance) string {
	switch d {
	case storage.DistanceCosine:
		return "cosine"
	case storage.DistanceEuclid:
		return "euclid"
	case storage.DistanceDot:
		return "dot"
	case storage.DistanceManhattan:
		return "manhattan"
	default:
		return "desconocida"
	}
}

func toFieldType(t storage.PayloadIndexType) qdr.FieldType {
	switch t {
	case storage.IndexInteger:
		return qdr.FieldType_FieldTypeInteger
	case storage.IndexFloat:
		return qdr.FieldType_FieldTypeFloat
	case storage.IndexBool:
		return qdr.FieldType_FieldTypeBool
	case storage.IndexDatetime:
		return qdr.FieldType_FieldTypeDatetime
	case storage.IndexKeyword:
		fallthrough
	default:
		return qdr.FieldType_FieldTypeKeyword
	}
}

func toPointStruct(p storage.VectorPoint) (*qdr.PointStruct, error) {
	id, err := toPointID(p.ID)
	if err != nil {
		return nil, err
	}

	vectors := make(map[string]*qdr.Vector, len(p.Vectors))
	for name, values := range p.Vectors {
		vectors[name] = qdr.NewVector(values...)
	}

	payload := make(map[string]*qdr.Value, len(p.Payload))
	for key, value := range p.Payload {
		v, err := qdr.NewValue(value)
		if err != nil {
			return nil, fmt.Errorf("%w: payload %q: %v", storage.ErrInvalidArgument, key, err)
		}
		payload[key] = v
	}

	return &qdr.PointStruct{
		Id:      id,
		Vectors: qdr.NewVectorsMap(vectors),
		Payload: payload,
	}, nil
}

func toFilter(f storage.Filter) *qdr.Filter {
	filter := &qdr.Filter{}
	for _, m := range f.Must {
		filter.Must = append(filter.Must, qdr.NewMatchKeyword(m.Field, m.Value))
	}
	for _, m := range f.MustNot {
		filter.MustNot = append(filter.MustNot, qdr.NewMatchKeyword(m.Field, m.Value))
	}
	return filter
}

func fromRetrieved(rp *qdr.RetrievedPoint) storage.VectorPoint {
	point := storage.VectorPoint{
		ID:      pointIDString(rp.GetId()),
		Payload: fromPayload(rp.GetPayload()),
	}
	if vectors := rp.GetVectors(); vectors != nil {
		point.Vectors = make(map[string][]float32)
		if v := vectors.GetVector(); v != nil {
			point.Vectors[""] = v.GetData()
		}
		for name, v := range vectors.GetVectors().GetVectors() {
			point.Vectors[name] = v.GetData()
		}
	}
	return point
}

func fromPayload(payload map[string]*qdr.Value) map[string]any {
	if len(payload) == 0 {
		return nil
	}
	out := make(map[string]any, len(payload))
	for key, value := range payload {
		out[key] = value.AsInterface()
	}
	return out
}

func pointIDString(id *qdr.PointId) string {
	if id == nil {
		return ""
	}
	if uuid := id.GetUuid(); uuid != "" {
		return uuid
	}
	return strconv.FormatUint(id.GetNum(), 10)
}

func toPointID(raw string) (*qdr.PointId, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: identificador de punto vacío", storage.ErrInvalidArgument)
	}
	if num, err := strconv.ParseUint(raw, 10, 64); err == nil {
		return qdr.NewIDNum(num), nil
	}
	if isUUID(raw) {
		return qdr.NewID(raw), nil
	}
	return nil, fmt.Errorf("%w: identificador de punto %q no es un UUID ni un entero sin signo",
		storage.ErrInvalidArgument, raw)
}

func requireCollection(collection string) error {
	if strings.TrimSpace(collection) == "" {
		return fmt.Errorf("%w: nombre de colección vacío", storage.ErrInvalidArgument)
	}
	return nil
}

func displayVectorName(name string) string {
	if name == "" {
		return "(por defecto)"
	}
	return name
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexDigit(r) {
				return false
			}
		}
	}
	return true
}

func isHexDigit(r rune) bool {
	switch {
	case r >= '0' && r <= '9':
		return true
	case r >= 'a' && r <= 'f':
		return true
	case r >= 'A' && r <= 'F':
		return true
	default:
		return false
	}
}

// parseEndpoint admite "host", "host:puerto" o "esquema://host:puerto".
func parseEndpoint(raw string) (host string, port int, useTLS bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, false, fmt.Errorf("%w: MAR_QDRANT_URL vacío", storage.ErrInvalidArgument)
	}

	if strings.Contains(raw, "://") {
		u, perr := url.Parse(raw)
		if perr != nil {
			return "", 0, false, fmt.Errorf("%w: URL de Qdrant inválida %q: %v",
				storage.ErrInvalidArgument, raw, perr)
		}
		if u.Hostname() == "" {
			return "", 0, false, fmt.Errorf("%w: URL de Qdrant sin host %q",
				storage.ErrInvalidArgument, raw)
		}
		port = defaultPort
		if p := u.Port(); p != "" {
			n, aerr := strconv.Atoi(p)
			if aerr != nil {
				return "", 0, false, fmt.Errorf("%w: puerto de Qdrant inválido %q",
					storage.ErrInvalidArgument, p)
			}
			port = n
		}
		return u.Hostname(), port, u.Scheme == "https" || u.Scheme == "grpcs", nil
	}

	if h, p, serr := net.SplitHostPort(raw); serr == nil {
		n, aerr := strconv.Atoi(p)
		if aerr != nil {
			return "", 0, false, fmt.Errorf("%w: puerto de Qdrant inválido %q",
				storage.ErrInvalidArgument, p)
		}
		return h, n, false, nil
	}

	return raw, defaultPort, false, nil
}
