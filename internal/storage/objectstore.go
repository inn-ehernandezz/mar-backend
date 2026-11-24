package storage

import (
	"context"
	"io"
	"time"
)

// ObjectStore es el puerto de almacenamiento de objetos (blobs). Abstrae Azure
// Blob Storage (Azurite en local, Azure real en producción) sin exponer
// detalles del SDK.
type ObjectStore interface {
	// EnsureContainer crea el contenedor si no existe. Es idempotente.
	EnsureContainer(ctx context.Context, container string) error
	// Put sube un objeto leyéndolo desde un io.Reader, sin cargarlo completo
	// en memoria.
	Put(ctx context.Context, in PutObjectInput) (ObjectInfo, error)
	// Get descarga un objeto y devuelve un lector en streaming. Quien llama es
	// responsable de cerrar Body.
	Get(ctx context.Context, container, name string) (ObjectReader, error)
	// Stat devuelve los metadatos de un objeto sin descargar su contenido.
	Stat(ctx context.Context, container, name string) (ObjectInfo, error)
	// Exists indica si un objeto existe.
	Exists(ctx context.Context, container, name string) (bool, error)
	// Delete elimina un objeto. Es idempotente: borrar algo inexistente no es
	// un error.
	Delete(ctx context.Context, container, name string) error
	// List enumera objetos por prefijo con paginación por continuación.
	List(ctx context.Context, in ListOptions) (ListResult, error)
	// Ping comprueba la disponibilidad del servicio de almacenamiento.
	Ping(ctx context.Context) error
}

// PutObjectInput describe una subida de objeto.
type PutObjectInput struct {
	Container string
	Name      string
	Body      io.Reader
	// Size es el tamaño en bytes si se conoce; 0 si es desconocido.
	Size int64
	// ContentType es el tipo MIME del objeto. Se conserva como metadato.
	ContentType string
	// Metadata son metadatos adicionales definidos por la aplicación.
	Metadata map[string]string
	// ContentHash es el hash de integridad calculado por el productor (p. ej.
	// SHA-256 en hexadecimal). Es opcional.
	ContentHash string
	// IfNoneMatch evita sobrescribir accidentalmente un objeto existente.
	IfNoneMatch bool
}

// ObjectInfo describe un objeto almacenado.
type ObjectInfo struct {
	Container    string
	Name         string
	ContentType  string
	Size         int64
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}

// ObjectReader combina el contenido en streaming con sus metadatos.
type ObjectReader struct {
	Info ObjectInfo
	Body io.ReadCloser
}

// ListOptions describe una petición de listado por prefijo.
type ListOptions struct {
	Container string
	Prefix    string
	// MaxResults acota el número de elementos por página. Si es 0 se aplica un
	// valor por defecto del adaptador.
	MaxResults int32
	// Continuation es el token devuelto por una llamada anterior; vacío para la
	// primera página.
	Continuation string
}

// ListResult es el resultado paginado de un listado.
type ListResult struct {
	Items []ObjectInfo
	// Continuation es el token para solicitar la siguiente página. Vacío si no
	// hay más resultados.
	Continuation string
}
