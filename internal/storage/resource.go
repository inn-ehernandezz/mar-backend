package storage

import (
	"context"
	"fmt"
	"time"
)

// EntitySchemaVersion es la versión del esquema de las entidades persistidas en
// Table Storage (recursos y trabajos).
const EntitySchemaVersion = 1

// ResourceStatus describe el estado del ciclo de vida de un recurso. El
// conjunto inicial es deliberadamente mínimo; la ingesta (Fase 3) lo ampliará
// según sus necesidades reales.
type ResourceStatus string

// Estados iniciales de un recurso.
const (
	// ResourcePending: registrado, aún sin su contenido confirmado en Blob.
	ResourcePending ResourceStatus = "PENDING"
	// ResourceStored: original almacenado y verificado en Blob.
	ResourceStored ResourceStatus = "STORED"
	// ResourceProcessed: procesado (extracción, fragmentación e indexado).
	ResourceProcessed ResourceStatus = "PROCESSED"
	// ResourceFailed: el procesamiento del recurso terminó con error.
	ResourceFailed ResourceStatus = "FAILED"
)

// validResourceStatuses permite validar la escritura de estados conocidos.
var validResourceStatuses = map[ResourceStatus]struct{}{
	ResourcePending:   {},
	ResourceStored:    {},
	ResourceProcessed: {},
	ResourceFailed:    {},
}

// Resource es un recurso (documento, imagen, audio o vídeo) registrado por
// MAR. Es un tipo de dominio: no contiene ninguna dependencia de SDK.
type Resource struct {
	TenantID      string
	ResourceID    string
	FileName      string
	ContentType   string
	FileSize      int64
	BlobPath      string
	ContentHash   string
	IndexVersion  string
	Status        ResourceStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
	SchemaVersion int
	// ETag es el token de concurrencia optimista. No forma parte de la lógica
	// de negocio: lo asigna el almacenamiento y se exige en las actualizaciones
	// condicionales.
	ETag string
}

// Validate comprueba los campos obligatorios de un recurso.
func (r Resource) Validate() error {
	if r.TenantID == "" {
		return fmt.Errorf("%w: tenant_id obligatorio en el recurso", ErrInvalidArgument)
	}
	if r.ResourceID == "" {
		return fmt.Errorf("%w: resource_id obligatorio", ErrInvalidArgument)
	}
	if r.FileName == "" {
		return fmt.Errorf("%w: file_name obligatorio", ErrInvalidArgument)
	}
	if _, ok := validResourceStatuses[r.Status]; !ok {
		return fmt.Errorf("%w: estado de recurso desconocido %q", ErrInvalidArgument, r.Status)
	}
	return nil
}

// ResourceQuery describe un listado paginado de recursos de un tenant.
type ResourceQuery struct {
	// TenantID es obligatorio: el listado siempre se aísla por tenant.
	TenantID string
	// Limit acota el número de recursos por página. Si es 0 se aplica un valor
	// por defecto del adaptador.
	Limit int32
	// Continuation es el token devuelto por una llamada anterior; vacío para
	// la primera página.
	Continuation string
}

// ResourcePage es una página de recursos.
type ResourcePage struct {
	Items []Resource
	// Continuation es el token para solicitar la siguiente página. Vacío si no
	// hay más resultados.
	Continuation string
}

// ResourceRepository es el puerto de persistencia de recursos. Table Storage es
// la fuente de verdad de sus metadatos operativos.
type ResourceRepository interface {
	// Create persiste un recurso nuevo. Es idempotente: si ya existe un recurso
	// con el mismo identificador, devuelve el existente sin modificarlo.
	Create(ctx context.Context, r Resource) (Resource, error)
	// Get recupera un recurso por identificador. El tenant es obligatorio para
	// garantizar el aislamiento incluso en accesos directos.
	Get(ctx context.Context, tenantID, resourceID string) (Resource, error)
	// Update actualiza un recurso aplicando concurrencia optimista mediante
	// ETag. Un ETag vacío fuerza la escritura incondicional.
	Update(ctx context.Context, r Resource, ifMatch string) (Resource, error)
	// List enumera los recursos de un tenant de forma paginada.
	List(ctx context.Context, q ResourceQuery) (ResourcePage, error)
}
