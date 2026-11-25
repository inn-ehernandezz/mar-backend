// Package table implementa los puertos storage.ResourceRepository y
// storage.JobRepository sobre Azure Table Storage. En local apunta a Azurite;
// en producción a Azure real, sin cambios de código (solo de configuración).
// Toda la dependencia del SDK aztables queda confinada en este paquete.
//
// Modelo de datos:
//
//   - Tabla de recursos: PartitionKey = tenant_id, RowKey = resource_id.
//   - Tabla de trabajos: PartitionKey = tenant_id, RowKey = job_id.
//
// Las entidades `job` y `outbox` (patrón outbox, ADR 0006) compartirán la
// partición del tenant en la tabla de trabajos, requisito para usar el lote
// transaccional intra-partición. Las fechas se persisten como cadenas ISO-8601
// en UTC (Edm.String), lo que evita las restricciones de rango de Edm.DateTime
// y permite filtrar lexicográficamente.
package table

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"

	"mar-backend/internal/storage"
)

// Nombres de tabla por defecto.
const (
	DefaultResourcesTable = "resources"
	DefaultJobsTable      = "jobs"
)

// defaultPageLimit es el tamaño de página por defecto de los listados.
const defaultPageLimit = 100

// continuationSep separa la clave de partición y la de fila en el token de
// continuación. Es un carácter de control que no aparece en identificadores.
const continuationSep = "\x1f"

// Config agrupa los parámetros de conexión del adaptador.
type Config struct {
	// ConnectionString permite "UseDevelopmentStorage=true" (Azurite) o una
	// cadena de Azure real.
	ConnectionString string
	// Endpoint, AccountName y AccountKey se usan cuando no hay cadena de
	// conexión. En local el endpoint de Azurite incluye la cuenta, p. ej.
	// http://localhost:10002/devstoreaccount1
	Endpoint    string
	AccountName string
	AccountKey  string

	// TableResources y TableJobs son los nombres de las tablas. Si están
	// vacíos se aplican los valores por defecto.
	TableResources string
	TableJobs      string

	RequestTimeout time.Duration
}

// Store mantiene el cliente de servicio y construye los repositorios.
type Store struct {
	service        *aztables.ServiceClient
	timeout        time.Duration
	resourcesTable string
	jobsTable      string
}

// New crea el cliente de Table Storage a partir de la configuración. No
// establece conexión (el SDK es perezoso): una Table Storage no disponible no
// impide arrancar; se refleja en el health check.
func New(cfg Config) (*Store, error) {
	var (
		service *aztables.ServiceClient
		err     error
	)

	switch {
	case strings.TrimSpace(cfg.ConnectionString) != "":
		service, err = aztables.NewServiceClientFromConnectionString(cfg.ConnectionString, nil)
	default:
		if cfg.Endpoint == "" || cfg.AccountName == "" || cfg.AccountKey == "" {
			return nil, fmt.Errorf("%w: credenciales de Table Storage incompletas",
				storage.ErrInvalidArgument)
		}
		var cred *aztables.SharedKeyCredential
		cred, err = aztables.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("table: credencial de clave compartida: %w", err)
		}
		service, err = aztables.NewServiceClientWithSharedKey(cfg.Endpoint, cred, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("table: crear cliente: %w", err)
	}

	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	resourcesTable := cfg.TableResources
	if resourcesTable == "" {
		resourcesTable = DefaultResourcesTable
	}
	jobsTable := cfg.TableJobs
	if jobsTable == "" {
		jobsTable = DefaultJobsTable
	}

	return &Store{
		service:        service,
		timeout:        timeout,
		resourcesTable: resourcesTable,
		jobsTable:      jobsTable,
	}, nil
}

// Resources devuelve el repositorio de recursos.
func (s *Store) Resources() *ResourceRepository {
	return &ResourceRepository{client: s.service.NewClient(s.resourcesTable), timeout: s.timeout}
}

// Jobs devuelve el repositorio de trabajos.
func (s *Store) Jobs() *JobRepository {
	return &JobRepository{client: s.service.NewClient(s.jobsTable), timeout: s.timeout}
}

// EnsureSchema crea las tablas si no existen. Es idempotente.
func (s *Store) EnsureSchema(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	for _, name := range []string{s.resourcesTable, s.jobsTable} {
		if _, err := s.service.CreateTable(c, name, nil); err != nil {
			if isErrorCode(err, aztables.TableAlreadyExists) {
				continue
			}
			return fmt.Errorf("table: crear tabla %q: %w", name, mapError(err))
		}
	}
	return nil
}

// Ping comprueba la disponibilidad del servicio de tablas.
func (s *Store) Ping(ctx context.Context) error {
	c, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	if _, err := s.service.GetProperties(c, nil); err != nil {
		return fmt.Errorf("table: comprobar servicio: %w", mapError(err))
	}
	return nil
}

// --- Repositorio de recursos ---

// ResourceRepository implementa storage.ResourceRepository.
type ResourceRepository struct {
	client  *aztables.Client
	timeout time.Duration
}

var _ storage.ResourceRepository = (*ResourceRepository)(nil)

// resourceEntity es la representación persistida de un recurso.
type resourceEntity struct {
	PartitionKey  string `json:"PartitionKey"`
	RowKey        string `json:"RowKey"`
	TenantID      string `json:"TenantID"`
	ResourceID    string `json:"ResourceID"`
	FileName      string `json:"FileName"`
	ContentType   string `json:"ContentType"`
	FileSize      int64  `json:"FileSize"`
	BlobPath      string `json:"BlobPath"`
	ContentHash   string `json:"ContentHash"`
	IndexVersion  string `json:"IndexVersion"`
	Status        string `json:"Status"`
	CreatedAt     string `json:"CreatedAt"`
	UpdatedAt     string `json:"UpdatedAt"`
	SchemaVersion int32  `json:"SchemaVersion"`
}

// Create persiste un recurso nuevo. Si ya existe uno con el mismo
// identificador, devuelve el existente (idempotencia ante reintentos).
func (r *ResourceRepository) Create(ctx context.Context, res storage.Resource) (storage.Resource, error) {
	if err := res.Validate(); err != nil {
		return storage.Resource{}, err
	}

	now := time.Now().UTC()
	if res.CreatedAt.IsZero() {
		res.CreatedAt = now
	}
	res.UpdatedAt = now
	if res.SchemaVersion == 0 {
		res.SchemaVersion = storage.EntitySchemaVersion
	}

	body, err := json.Marshal(toResourceEntity(res))
	if err != nil {
		return storage.Resource{}, fmt.Errorf("table: serializar recurso %s: %w", res.ResourceID, err)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if _, err := r.client.AddEntity(c, body, nil); err != nil {
		if isErrorCode(err, aztables.EntityAlreadyExists) {
			return r.Get(ctx, res.TenantID, res.ResourceID)
		}
		return storage.Resource{}, fmt.Errorf("table: crear recurso %s: %w", res.ResourceID, mapError(err))
	}

	// Se relee para devolver el ETag asignado por el servicio.
	return r.Get(ctx, res.TenantID, res.ResourceID)
}

// Get recupera un recurso por identificador y tenant.
func (r *ResourceRepository) Get(ctx context.Context, tenantID, resourceID string) (storage.Resource, error) {
	if tenantID == "" || resourceID == "" {
		return storage.Resource{}, fmt.Errorf("%w: tenant_id y resource_id obligatorios",
			storage.ErrInvalidArgument)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	resp, err := r.client.GetEntity(c, tenantID, resourceID, nil)
	if err != nil {
		return storage.Resource{}, fmt.Errorf("table: obtener recurso %s: %w", resourceID, mapError(err))
	}

	res, err := fromResourceEntityBytes(resp.Value)
	if err != nil {
		return storage.Resource{}, err
	}
	res.ETag = string(resp.ETag)
	return res, nil
}

// Update actualiza un recurso. ifMatch transporta el ETag para concurrencia
// optimista; si está vacío la escritura es incondicional.
func (r *ResourceRepository) Update(ctx context.Context, res storage.Resource, ifMatch string) (storage.Resource, error) {
	if err := res.Validate(); err != nil {
		return storage.Resource{}, err
	}

	res.UpdatedAt = time.Now().UTC()
	if res.SchemaVersion == 0 {
		res.SchemaVersion = storage.EntitySchemaVersion
	}

	body, err := json.Marshal(toResourceEntity(res))
	if err != nil {
		return storage.Resource{}, fmt.Errorf("table: serializar recurso %s: %w", res.ResourceID, err)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if _, err := r.client.UpdateEntity(c, body, &aztables.UpdateEntityOptions{
		IfMatch:    etagOrWildcard(ifMatch),
		UpdateMode: aztables.UpdateModeReplace,
	}); err != nil {
		return storage.Resource{}, fmt.Errorf("table: actualizar recurso %s: %w", res.ResourceID, mapError(err))
	}

	return r.Get(ctx, res.TenantID, res.ResourceID)
}

// List enumera los recursos de un tenant de forma paginada.
func (r *ResourceRepository) List(ctx context.Context, q storage.ResourceQuery) (storage.ResourcePage, error) {
	if q.TenantID == "" {
		return storage.ResourcePage{}, fmt.Errorf("%w: tenant_id obligatorio", storage.ErrInvalidArgument)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	filter := fmt.Sprintf("PartitionKey eq '%s'", escapeOData(q.TenantID))
	opts := &aztables.ListEntitiesOptions{Filter: &filter, Top: ptr(limit)}
	applyContinuation(opts, q.Continuation)

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	page, err := r.client.NewListEntitiesPager(opts).NextPage(c)
	if err != nil {
		return storage.ResourcePage{}, fmt.Errorf("table: listar recursos: %w", mapError(err))
	}

	items := make([]storage.Resource, 0, len(page.Entities))
	for _, raw := range page.Entities {
		res, err := fromResourceEntityBytes(raw)
		if err != nil {
			return storage.ResourcePage{}, err
		}
		items = append(items, res)
	}

	return storage.ResourcePage{
		Items:        items,
		Continuation: encodeContinuation(page.NextPartitionKey, page.NextRowKey),
	}, nil
}

func toResourceEntity(res storage.Resource) resourceEntity {
	return resourceEntity{
		PartitionKey:  res.TenantID,
		RowKey:        res.ResourceID,
		TenantID:      res.TenantID,
		ResourceID:    res.ResourceID,
		FileName:      res.FileName,
		ContentType:   res.ContentType,
		FileSize:      res.FileSize,
		BlobPath:      res.BlobPath,
		ContentHash:   res.ContentHash,
		IndexVersion:  res.IndexVersion,
		Status:        string(res.Status),
		CreatedAt:     formatTime(res.CreatedAt),
		UpdatedAt:     formatTime(res.UpdatedAt),
		SchemaVersion: int32(res.SchemaVersion),
	}
}

func fromResourceEntityBytes(raw []byte) (storage.Resource, error) {
	var e resourceEntity
	if err := json.Unmarshal(raw, &e); err != nil {
		return storage.Resource{}, fmt.Errorf("table: deserializar recurso: %w", err)
	}

	createdAt, err := parseTime(e.CreatedAt)
	if err != nil {
		return storage.Resource{}, fmt.Errorf("table: created_at inválido en recurso %s: %w", e.ResourceID, err)
	}
	updatedAt, err := parseTime(e.UpdatedAt)
	if err != nil {
		return storage.Resource{}, fmt.Errorf("table: updated_at inválido en recurso %s: %w", e.ResourceID, err)
	}

	return storage.Resource{
		TenantID:      e.TenantID,
		ResourceID:    e.ResourceID,
		FileName:      e.FileName,
		ContentType:   e.ContentType,
		FileSize:      e.FileSize,
		BlobPath:      e.BlobPath,
		ContentHash:   e.ContentHash,
		IndexVersion:  e.IndexVersion,
		Status:        storage.ResourceStatus(e.Status),
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		SchemaVersion: int(e.SchemaVersion),
	}, nil
}

// --- Repositorio de trabajos ---

// JobRepository implementa storage.JobRepository.
type JobRepository struct {
	client  *aztables.Client
	timeout time.Duration
}

var _ storage.JobRepository = (*JobRepository)(nil)

// jobEntity es la representación persistida de un trabajo.
type jobEntity struct {
	PartitionKey  string `json:"PartitionKey"`
	RowKey        string `json:"RowKey"`
	TenantID      string `json:"TenantID"`
	JobID         string `json:"JobID"`
	ResourceID    string `json:"ResourceID"`
	JobType       string `json:"JobType"`
	Status        string `json:"Status"`
	Progress      int32  `json:"Progress"`
	Attempts      int32  `json:"Attempts"`
	Stage         string `json:"Stage"`
	Pipeline      string `json:"Pipeline"`
	ErrorStage    string `json:"ErrorStage"`
	CommitStarted bool   `json:"CommitStarted"`
	LeaseUntil    string `json:"LeaseUntil"`
	DispatchID    string `json:"DispatchID"`
	DispatchAt    string `json:"DispatchAt"`
	RequestID     string `json:"RequestID"`
	ErrorCode     string `json:"ErrorCode"`
	ErrorMessage  string `json:"ErrorMessage"`
	CreatedAt     string `json:"CreatedAt"`
	UpdatedAt     string `json:"UpdatedAt"`
	StartedAt     string `json:"StartedAt"`
	CompletedAt   string `json:"CompletedAt"`
	SchemaVersion int32  `json:"SchemaVersion"`
}

// Create persiste un trabajo nuevo. Si ya existe uno con el mismo
// identificador, devuelve el existente (idempotencia ante reintentos).
func (r *JobRepository) Create(ctx context.Context, job storage.Job) (storage.Job, error) {
	if err := job.Validate(); err != nil {
		return storage.Job{}, err
	}

	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	if job.SchemaVersion == 0 {
		job.SchemaVersion = storage.EntitySchemaVersion
	}

	body, err := json.Marshal(toJobEntity(job))
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: serializar trabajo %s: %w", job.JobID, err)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if _, err := r.client.AddEntity(c, body, nil); err != nil {
		if isErrorCode(err, aztables.EntityAlreadyExists) {
			return r.Get(ctx, job.TenantID, job.JobID)
		}
		return storage.Job{}, fmt.Errorf("table: crear trabajo %s: %w", job.JobID, mapError(err))
	}

	return r.Get(ctx, job.TenantID, job.JobID)
}

// Get recupera un trabajo por identificador y tenant.
func (r *JobRepository) Get(ctx context.Context, tenantID, jobID string) (storage.Job, error) {
	job, _, err := r.get(ctx, tenantID, jobID)
	return job, err
}

// get es la variante interna que además devuelve el ETag.
func (r *JobRepository) get(ctx context.Context, tenantID, jobID string) (storage.Job, string, error) {
	if tenantID == "" || jobID == "" {
		return storage.Job{}, "", fmt.Errorf("%w: tenant_id y job_id obligatorios", storage.ErrInvalidArgument)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	resp, err := r.client.GetEntity(c, tenantID, jobID, nil)
	if err != nil {
		return storage.Job{}, "", fmt.Errorf("table: obtener trabajo %s: %w", jobID, mapError(err))
	}

	job, err := fromJobEntityBytes(resp.Value)
	if err != nil {
		return storage.Job{}, "", err
	}
	job.ETag = string(resp.ETag)
	return job, job.ETag, nil
}

// Update actualiza un trabajo validando la transición de estado y aplicando
// concurrencia optimista mediante ifMatch. Si ifMatch está vacío la escritura
// es incondicional.
func (r *JobRepository) Update(ctx context.Context, job storage.Job, ifMatch string) (storage.Job, error) {
	if err := job.Validate(); err != nil {
		return storage.Job{}, err
	}

	// Se lee el estado actual para validar la transición. El ETag sigue
	// protegiendo la escritura frente a cambios concurrentes.
	current, _, err := r.get(ctx, job.TenantID, job.JobID)
	if err != nil {
		return storage.Job{}, err
	}
	if err := storage.ValidateJobTransition(current.Status, job.Status); err != nil {
		return storage.Job{}, err
	}

	job.UpdatedAt = time.Now().UTC()
	if job.SchemaVersion == 0 {
		job.SchemaVersion = storage.EntitySchemaVersion
	}

	body, err := json.Marshal(toJobEntity(job))
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: serializar trabajo %s: %w", job.JobID, err)
	}

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	if _, err := r.client.UpdateEntity(c, body, &aztables.UpdateEntityOptions{
		IfMatch:    etagOrWildcard(ifMatch),
		UpdateMode: aztables.UpdateModeReplace,
	}); err != nil {
		return storage.Job{}, fmt.Errorf("table: actualizar trabajo %s: %w", job.JobID, mapError(err))
	}

	return r.Get(ctx, job.TenantID, job.JobID)
}

// List enumera los trabajos de un tenant de forma paginada, con filtros
// opcionales por recurso y estado.
func (r *JobRepository) List(ctx context.Context, q storage.JobQuery) (storage.JobPage, error) {
	if q.TenantID == "" {
		return storage.JobPage{}, fmt.Errorf("%w: tenant_id obligatorio", storage.ErrInvalidArgument)
	}

	filter := fmt.Sprintf("PartitionKey eq '%s'", escapeOData(q.TenantID))
	conditions := []string{filter, "JobType ne ''"}

	if q.ResourceID != "" {
		conditions = append(conditions, fmt.Sprintf("ResourceID eq '%s'", escapeOData(q.ResourceID)))
	}
	if q.Status != "" {
		conditions = append(conditions, fmt.Sprintf("Status eq '%s'", escapeOData(string(q.Status))))
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultPageLimit
	}
	filter = strings.Join(conditions, " and ")
	opts := &aztables.ListEntitiesOptions{Filter: &filter, Top: ptr(limit)}
	applyContinuation(opts, q.Continuation)

	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	page, err := r.client.NewListEntitiesPager(opts).NextPage(c)
	if err != nil {
		return storage.JobPage{}, fmt.Errorf("table: listar trabajos: %w", mapError(err))
	}

	items := make([]storage.Job, 0, len(page.Entities))
	for _, raw := range page.Entities {
		job, err := fromJobEntityBytes(raw)
		if err != nil {
			return storage.JobPage{}, err
		}
		if job.JobID == "" {
			continue // outbox rows have no JobID
		}
		items = append(items, job)
	}

	return storage.JobPage{
		Items:        items,
		Continuation: encodeContinuation(page.NextPartitionKey, page.NextRowKey),
	}, nil
}

func toJobEntity(job storage.Job) jobEntity {
	return jobEntity{
		PartitionKey:  job.TenantID,
		RowKey:        job.JobID,
		TenantID:      job.TenantID,
		JobID:         job.JobID,
		ResourceID:    job.ResourceID,
		JobType:       job.JobType,
		Status:        string(job.Status),
		Progress:      job.Progress,
		Attempts:      job.Attempts,
		Stage:         job.Stage,
		Pipeline:      job.Pipeline,
		ErrorStage:    job.ErrorStage,
		CommitStarted: job.CommitStarted,
		LeaseUntil:    formatSortableTime(job.LeaseUntil),
		DispatchID:    job.DispatchID,
		DispatchAt:    formatSortableTime(job.DispatchAt),
		RequestID:     job.RequestID,
		ErrorCode:     job.ErrorCode,
		ErrorMessage:  job.ErrorMessage,
		CreatedAt:     formatTime(job.CreatedAt),
		UpdatedAt:     formatTime(job.UpdatedAt),
		StartedAt:     formatTimePtr(job.StartedAt),
		CompletedAt:   formatTimePtr(job.CompletedAt),
		SchemaVersion: int32(job.SchemaVersion),
	}
}

func fromJobEntityBytes(raw []byte) (storage.Job, error) {
	var e jobEntity
	if err := json.Unmarshal(raw, &e); err != nil {
		return storage.Job{}, fmt.Errorf("table: deserializar trabajo: %w", err)
	}

	createdAt, err := parseTime(e.CreatedAt)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: created_at inválido en trabajo %s: %w", e.JobID, err)
	}
	updatedAt, err := parseTime(e.UpdatedAt)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: updated_at inválido en trabajo %s: %w", e.JobID, err)
	}
	startedAt, err := parseTimePtr(e.StartedAt)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: started_at inválido en trabajo %s: %w", e.JobID, err)
	}
	completedAt, err := parseTimePtr(e.CompletedAt)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: completed_at inválido en trabajo %s: %w", e.JobID, err)
	}

	leaseUntil, err := parseTimePtr(e.LeaseUntil)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: lease inválido: %w", err)
	}

	dispatchAt, err := parseTimePtr(e.DispatchAt)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: dispatch inválido: %w", err)
	}

	return storage.Job{
		TenantID:      e.TenantID,
		JobID:         e.JobID,
		ResourceID:    e.ResourceID,
		JobType:       e.JobType,
		Status:        storage.JobStatus(e.Status),
		Progress:      e.Progress,
		Attempts:      e.Attempts,
		Stage:         e.Stage,
		Pipeline:      e.Pipeline,
		ErrorStage:    e.ErrorStage,
		CommitStarted: e.CommitStarted,
		LeaseUntil:    leaseUntil,
		DispatchID:    e.DispatchID,
		DispatchAt:    dispatchAt,
		RequestID:     e.RequestID,
		ErrorCode:     e.ErrorCode,
		ErrorMessage:  e.ErrorMessage,
		CreatedAt:     createdAt,
		UpdatedAt:     updatedAt,
		StartedAt:     startedAt,
		CompletedAt:   completedAt,
		SchemaVersion: int(e.SchemaVersion),
	}, nil
}

// --- Utilidades compartidas ---

// formatTime serializa una fecha como ISO-8601 en UTC. Una fecha cero se
// serializa como cadena vacía.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return formatTime(*t)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func parseTimePtr(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := parseTime(s)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// etagOrWildcard convierte un ETag textual en el valor esperado por el SDK.
// Un ETag vacío se traduce en "*", que acepta cualquier versión (escritura
// incondicional).
func etagOrWildcard(ifMatch string) *azcore.ETag {
	trimmed := strings.TrimSpace(ifMatch)
	if trimmed == "" {
		trimmed = "*"
	}
	etag := azcore.ETag(trimmed)
	return &etag
}

// escapeOData neutraliza comillas simples en un literal de filtro OData.
func escapeOData(value string) string {
	return strings.ReplaceAll(value, "'", "''")
}

// applyContinuation traduce el token de continuación opaco del puerto a las
// claves de continuación del SDK.
func applyContinuation(opts *aztables.ListEntitiesOptions, continuation string) {
	if continuation == "" {
		return
	}
	partitionKey, rowKey := decodeContinuation(continuation)
	if partitionKey != "" {
		opts.NextPartitionKey = &partitionKey
	}
	if rowKey != "" {
		opts.NextRowKey = &rowKey
	}
}

func encodeContinuation(partitionKey, rowKey *string) string {
	pk := ""
	if partitionKey != nil {
		pk = *partitionKey
	}
	rk := ""
	if rowKey != nil {
		rk = *rowKey
	}
	if pk == "" && rk == "" {
		return ""
	}
	return pk + continuationSep + rk
}

func decodeContinuation(continuation string) (partitionKey, rowKey string) {
	if continuation == "" {
		return "", ""
	}
	parts := strings.SplitN(continuation, continuationSep, 2)
	if len(parts) != 2 {
		return "", continuation
	}
	return parts[0], parts[1]
}

// mapError traduce los errores del SDK a los errores de dominio del paquete
// storage.
func mapError(err error) error {
	if err == nil {
		return nil
	}

	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.ErrorCode {
		case string(aztables.EntityNotFound),
			string(aztables.TableNotFound),
			string(aztables.ResourceNotFound):
			return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
		case string(aztables.UpdateConditionNotSatisfied),
			string(aztables.EntityAlreadyExists):
			return fmt.Errorf("%w: %v", storage.ErrConflict, err)
		}

		switch respErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %v", storage.ErrNotFound, err)
		case http.StatusConflict, http.StatusPreconditionFailed:
			return fmt.Errorf("%w: %v", storage.ErrConflict, err)
		}
	}

	return err
}

func isErrorCode(err error, code aztables.TableErrorCode) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	return respErr.ErrorCode == string(code)
}

func ptr[T any](v T) *T {
	return &v
}
