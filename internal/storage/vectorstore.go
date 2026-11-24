package storage

import "context"

// Distance es la métrica de similitud de un espacio vectorial.
type Distance int

// Métricas de similitud soportadas.
const (
	DistanceUnspecified Distance = iota
	DistanceCosine
	DistanceEuclid
	DistanceDot
	DistanceManhattan
)

// VectorParams describe la configuración de un vector nombrado.
type VectorParams struct {
	// Size es la dimensión del vector. Debe ser mayor que cero.
	Size uint64
	// Distance es la métrica de similitud.
	Distance Distance
}

// PayloadIndexType es el tipo de índice de payload.
type PayloadIndexType int

// Tipos de índice de payload soportados.
const (
	IndexKeyword PayloadIndexType = iota
	IndexInteger
	IndexFloat
	IndexBool
	IndexDatetime
)

// PayloadIndex describe un índice sobre un campo del payload.
type PayloadIndex struct {
	Field string
	Type  PayloadIndexType
}

// CollectionSpec describe el esquema deseado de una colección vectorial.
type CollectionSpec struct {
	Name string
	// Vectors asocia un nombre lógico de vector con sus parámetros. Una
	// colección puede tener varios vectores nombrados (dense, sparse, etc.).
	Vectors map[string]VectorParams
	// Indexes son los índices de payload que deben existir.
	Indexes []PayloadIndex
}

// CollectionInfo describe el esquema real de una colección existente.
type CollectionInfo struct {
	Name string
	// VectorSizes asocia el nombre del vector con su dimensión real.
	VectorSizes map[string]uint64
	// VectorDistances asocia el nombre del vector con su métrica real.
	VectorDistances map[string]Distance
}

// VectorPoint es un punto del índice vectorial.
type VectorPoint struct {
	// ID es el identificador del punto. Debe ser determinista para garantizar
	// la idempotencia de las reinserciones.
	ID string
	// Vectors asocia un nombre de vector con sus valores.
	Vectors map[string][]float32
	// Payload son los metadatos del punto.
	Payload map[string]any
}

// FieldMatch es una condición de igualdad sobre un campo del payload.
type FieldMatch struct {
	Field string
	Value string
}

// Filter es un filtro por metadatos. Solo se soporta igualdad, que es lo que
// necesitan los patrones de acceso actuales (aislamiento por tenant y borrado
// por recurso).
type Filter struct {
	// Must exige que todas las condiciones se cumplan.
	Must []FieldMatch
	// MustNot exige que ninguna condición se cumpla.
	MustNot []FieldMatch
}

// TenantFilter construye el filtro que aísla un tenant. Es el mecanismo
// obligatorio para garantizar el aislamiento en las operaciones sobre el
// índice vectorial.
func TenantFilter(tenantID string, extra ...FieldMatch) *Filter {
	must := make([]FieldMatch, 0, 1+len(extra))
	must = append(must, FieldMatch{Field: FieldTenantID, Value: tenantID})
	must = append(must, extra...)
	return &Filter{Must: must}
}

// ScoredPoint es un punto devuelto por una búsqueda, con su puntuación.
type ScoredPoint struct {
	ID      string
	Score   float32
	Payload map[string]any
}

// SearchQuery describe una búsqueda por similitud.
type SearchQuery struct {
	// VectorName identifica el vector nombrado a usar. Si está vacío se usa el
	// vector por defecto de la colección.
	VectorName string
	Vector     []float32
	Limit      uint64
	Filter     *Filter
	// WithPayload solicita incluir el payload en los resultados.
	WithPayload bool
}

// ScrollQuery describe una enumeración paginada de puntos.
type ScrollQuery struct {
	Limit       uint32
	Offset      string
	Filter      *Filter
	WithPayload bool
}

// ScrollResult es el resultado paginado de una enumeración.
type ScrollResult struct {
	Points []VectorPoint
	// NextOffset es el desplazamiento para la siguiente página. Vacío si no hay
	// más resultados.
	NextOffset string
}

// VectorStore es el puerto de índice vectorial. Cada colección corresponde a un
// modelo de embeddings; nunca se comparan vectores de modelos distintos.
type VectorStore interface {
	// Ping comprueba la disponibilidad del servicio vectorial.
	Ping(ctx context.Context) error
	// EnsureCollection crea la colección de forma idempotente y valida que una
	// colección existente sea compatible. Nunca elimina ni modifica una
	// colección incompatible: devuelve ErrIncompatibleCollection.
	EnsureCollection(ctx context.Context, spec CollectionSpec) (CollectionInfo, error)
	// Upsert inserta o actualiza puntos de forma idempotente.
	Upsert(ctx context.Context, collection string, points []VectorPoint) error
	// Get recupera puntos por identificador.
	Get(ctx context.Context, collection string, ids []string) ([]VectorPoint, error)
	// Search devuelve los puntos más similares al vector de consulta.
	Search(ctx context.Context, collection string, query SearchQuery) ([]ScoredPoint, error)
	// DeleteByIDs elimina puntos por identificador.
	DeleteByIDs(ctx context.Context, collection string, ids []string) error
	// DeleteByFilter elimina los puntos que cumplen el filtro y devuelve cuántos
	// se eliminaron.
	DeleteByFilter(ctx context.Context, collection string, filter Filter) (uint64, error)
	// Scroll enumera puntos de forma paginada.
	Scroll(ctx context.Context, collection string, query ScrollQuery) (ScrollResult, error)
	// Count cuenta los puntos que cumplen el filtro.
	Count(ctx context.Context, collection string, filter *Filter) (uint64, error)
	// Close libera los recursos del cliente.
	Close() error
}
