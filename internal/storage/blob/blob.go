// Package blob implementa el puerto storage.ObjectStore sobre Azure Blob
// Storage. En local apunta a Azurite; en producción a Azure real, sin cambios
// de código (solo de configuración). Toda la dependencia del SDK queda
// confinada en este paquete.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	sdkblob "github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"

	"mar-backend/internal/storage"
)

// MetadataContentHash es la clave de metadatos donde se persiste el hash
// SHA-256 del contenido. Los nombres de metadatos de Azure Blob deben ser
// identificadores válidos, por eso usa guion bajo.
const MetadataContentHash = "content_sha256"

// Config agrupa los parámetros de conexión del adaptador.
type Config struct {
	// ConnectionString permite "UseDevelopmentStorage=true" (Azurite) o una
	// cadena de Azure real.
	ConnectionString string
	// Endpoint, AccountName y AccountKey se usan cuando no hay cadena de
	// conexión. En local el endpoint de Azurite incluye la cuenta, p. ej.
	// http://localhost:10000/devstoreaccount1
	Endpoint       string
	AccountName    string
	AccountKey     string
	RequestTimeout time.Duration
}

// Store es el adaptador de Azure Blob Storage.
type Store struct {
	client  *azblob.Client
	timeout time.Duration
}

var _ storage.ObjectStore = (*Store)(nil)

// New crea el cliente de Blob Storage a partir de la configuración.
func New(cfg Config) (*Store, error) {
	var (
		client *azblob.Client
		err    error
	)

	switch {
	case strings.TrimSpace(cfg.ConnectionString) != "":
		client, err = azblob.NewClientFromConnectionString(cfg.ConnectionString, nil)
	default:
		if cfg.Endpoint == "" || cfg.AccountName == "" || cfg.AccountKey == "" {
			return nil, fmt.Errorf("%w: credenciales de Blob Storage incompletas",
				storage.ErrInvalidArgument)
		}
		var cred *azblob.SharedKeyCredential
		cred, err = azblob.NewSharedKeyCredential(cfg.AccountName, cfg.AccountKey)
		if err != nil {
			return nil, fmt.Errorf("azblob: credencial de clave compartida: %w", err)
		}
		client, err = azblob.NewClientWithSharedKeyCredential(cfg.Endpoint, cred, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("azblob: crear cliente: %w", err)
	}

	timeout := cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Store{client: client, timeout: timeout}, nil
}

// EnsureContainer crea el contenedor si no existe. Es idempotente.
func (s *Store) EnsureContainer(ctx context.Context, container string) error {
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("%w: nombre de contenedor vacío", storage.ErrInvalidArgument)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.CreateContainer(c, container, nil); err != nil {
		if isAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("azblob: crear contenedor %q: %w", container, mapError(err))
	}
	return nil
}

// Put sube un objeto en streaming. Si se declara ContentHash, se verifica
// contra el contenido realmente subido; si no coincide, el objeto se descarta.
func (s *Store) Put(ctx context.Context, in storage.PutObjectInput) (storage.ObjectInfo, error) {
	if err := validateObjectPath(in.Container, in.Name); err != nil {
		return storage.ObjectInfo{}, err
	}
	if in.Body == nil {
		return storage.ObjectInfo{}, fmt.Errorf("%w: cuerpo del objeto vacío", storage.ErrInvalidArgument)
	}

	hasher := sha256.New()
	body := io.TeeReader(in.Body, hasher)

	metadata := toSDKMetadata(in.Metadata)
	if in.ContentHash != "" {
		if metadata == nil {
			metadata = map[string]*string{}
		}
		metadata[MetadataContentHash] = ptr(in.ContentHash)
	}

	opts := &azblob.UploadStreamOptions{
		HTTPHeaders: &sdkblob.HTTPHeaders{},
		Metadata:    metadata,
	}
	if in.ContentType != "" {
		opts.HTTPHeaders.BlobContentType = ptr(in.ContentType)
	}
	if in.IfNoneMatch {
		opts.AccessConditions = &sdkblob.AccessConditions{
			ModifiedAccessConditions: &sdkblob.ModifiedAccessConditions{
				IfNoneMatch: ptr(azcore.ETag("*")),
			},
		}
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	resp, err := s.client.UploadStream(c, in.Container, in.Name, body, opts)
	if err != nil {
		if in.IfNoneMatch && isPreconditionFailure(err) {
			return storage.ObjectInfo{}, fmt.Errorf(
				"%w: el objeto %s/%s ya existe", storage.ErrPreconditionFailed, in.Container, in.Name)
		}
		return storage.ObjectInfo{}, fmt.Errorf("azblob: subir %s/%s: %w",
			in.Container, in.Name, mapError(err))
	}

	computed := hex.EncodeToString(hasher.Sum(nil))
	if in.ContentHash != "" && !strings.EqualFold(in.ContentHash, computed) {
		// El contenido no coincide con el hash declarado: se descarta el objeto
		// para no almacenar datos potencialmente corruptos.
		_ = s.Delete(context.WithoutCancel(ctx), in.Container, in.Name)
		return storage.ObjectInfo{}, fmt.Errorf(
			"%w: el hash declarado no coincide con el contenido subido en %s/%s",
			storage.ErrInvalidArgument, in.Container, in.Name)
	}

	info := storage.ObjectInfo{
		Container:    in.Container,
		Name:         in.Name,
		ContentType:  in.ContentType,
		Size:         in.Size,
		ETag:         derefETag(resp.ETag),
		LastModified: derefTime(resp.LastModified),
		Metadata:     echoMetadata(in.Metadata, in.ContentHash),
	}
	if info.Size <= 0 {
		// El tamaño no se conocía al subir en streaming; se consulta para
		// devolver un dato real en lugar de un valor inventado.
		stat, statErr := s.Stat(ctx, in.Container, in.Name)
		if statErr != nil {
			return storage.ObjectInfo{}, fmt.Errorf("azblob: verificar %s/%s tras subir: %w",
				in.Container, in.Name, statErr)
		}
		info.Size = stat.Size
		info.Metadata = stat.Metadata
	}
	return info, nil
}

// echoMetadata reconstruye los metadatos efectivamente almacenados a partir de
// los enviados en la subida, sin depender de la respuesta del SDK (que no los
// devuelve en UploadStream).
func echoMetadata(meta map[string]string, contentHash string) map[string]string {
	if len(meta) == 0 && contentHash == "" {
		return nil
	}
	out := make(map[string]string, len(meta)+1)
	for k, v := range meta {
		out[k] = v
	}
	if contentHash != "" {
		out[MetadataContentHash] = contentHash
	}
	return out
}

// Get descarga un objeto devolviendo un lector en streaming.
func (s *Store) Get(ctx context.Context, container, name string) (storage.ObjectReader, error) {
	if err := validateObjectPath(container, name); err != nil {
		return storage.ObjectReader{}, err
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	resp, err := s.client.DownloadStream(c, container, name, nil)
	if err != nil {
		return storage.ObjectReader{}, fmt.Errorf("azblob: descargar %s/%s: %w",
			container, name, mapError(err))
	}

	return storage.ObjectReader{
		Info: storage.ObjectInfo{
			Container:    container,
			Name:         name,
			ContentType:  derefString(resp.ContentType, ""),
			Size:         derefInt64(resp.ContentLength),
			ETag:         derefETag(resp.ETag),
			LastModified: derefTime(resp.LastModified),
			Metadata:     fromSDKMetadata(resp.Metadata),
		},
		Body: resp.Body,
	}, nil
}

// Stat devuelve los metadatos de un objeto sin descargar su contenido.
func (s *Store) Stat(ctx context.Context, container, name string) (storage.ObjectInfo, error) {
	if err := validateObjectPath(container, name); err != nil {
		return storage.ObjectInfo{}, err
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	props, err := s.blobClient(container, name).GetProperties(c, nil)
	if err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("azblob: metadatos de %s/%s: %w",
			container, name, mapError(err))
	}

	return storage.ObjectInfo{
		Container:    container,
		Name:         name,
		ContentType:  derefString(props.ContentType, ""),
		Size:         derefInt64(props.ContentLength),
		ETag:         derefETag(props.ETag),
		LastModified: derefTime(props.LastModified),
		Metadata:     fromSDKMetadata(props.Metadata),
	}, nil
}

// Exists indica si un objeto existe.
func (s *Store) Exists(ctx context.Context, container, name string) (bool, error) {
	_, err := s.Stat(ctx, container, name)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	return false, err
}

// Delete elimina un objeto. Es idempotente.
func (s *Store) Delete(ctx context.Context, container, name string) error {
	if err := validateObjectPath(container, name); err != nil {
		return err
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.DeleteBlob(c, container, name, nil); err != nil {
		if errors.Is(mapError(err), storage.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("azblob: eliminar %s/%s: %w", container, name, mapError(err))
	}
	return nil
}

// List enumera objetos por prefijo con paginación por marca de continuación.
func (s *Store) List(ctx context.Context, in storage.ListOptions) (storage.ListResult, error) {
	if strings.TrimSpace(in.Container) == "" {
		return storage.ListResult{}, fmt.Errorf("%w: nombre de contenedor vacío", storage.ErrInvalidArgument)
	}

	opts := &azblob.ListBlobsFlatOptions{
		Include: azblob.ListBlobsInclude{Metadata: true},
	}
	if in.Prefix != "" {
		opts.Prefix = ptr(in.Prefix)
	}
	if in.MaxResults > 0 {
		opts.MaxResults = ptr(in.MaxResults)
	}
	if in.Continuation != "" {
		opts.Marker = ptr(in.Continuation)
	}

	c, cancel := s.withTimeout(ctx)
	defer cancel()

	pager := s.client.NewListBlobsFlatPager(in.Container, opts)
	if !pager.More() {
		return storage.ListResult{}, nil
	}

	page, err := pager.NextPage(c)
	if err != nil {
		return storage.ListResult{}, fmt.Errorf("azblob: listar %q: %w", in.Container, mapError(err))
	}

	result := storage.ListResult{Items: make([]storage.ObjectInfo, 0, len(page.Segment.BlobItems))}
	for _, item := range page.Segment.BlobItems {
		result.Items = append(result.Items, storage.ObjectInfo{
			Container:    in.Container,
			Name:         derefString(item.Name, ""),
			ContentType:  derefString(item.Properties.ContentType, ""),
			Size:         derefInt64(item.Properties.ContentLength),
			ETag:         derefETag(item.Properties.ETag),
			LastModified: derefTime(item.Properties.LastModified),
			Metadata:     fromSDKMetadata(item.Metadata),
		})
	}
	if page.NextMarker != nil {
		result.Continuation = *page.NextMarker
	}
	return result, nil
}

// Ping comprueba la disponibilidad del servicio de Blob Storage.
func (s *Store) Ping(ctx context.Context) error {
	c, cancel := s.withTimeout(ctx)
	defer cancel()

	if _, err := s.client.ServiceClient().GetProperties(c, nil); err != nil {
		return fmt.Errorf("azblob: health check: %w", mapError(err))
	}
	return nil
}

// --- internos ---

func (s *Store) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, s.timeout)
}

func (s *Store) blobClient(container, name string) *sdkblob.Client {
	return s.client.ServiceClient().NewContainerClient(container).NewBlobClient(name)
}

// validateObjectPath evita rutas ambiguas o intentos de escape de contenedor.
func validateObjectPath(container, name string) error {
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("%w: nombre de contenedor vacío", storage.ErrInvalidArgument)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: nombre de objeto vacío", storage.ErrInvalidArgument)
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("%w: nombre de objeto no válido %q", storage.ErrInvalidArgument, name)
	}
	return nil
}

// mapError traduce los errores del SDK a errores de dominio.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		switch respErr.StatusCode {
		case http.StatusNotFound:
			return fmt.Errorf("%w: %w", storage.ErrNotFound, err)
		case http.StatusConflict:
			return fmt.Errorf("%w: %w", storage.ErrConflict, err)
		case http.StatusPreconditionFailed:
			return fmt.Errorf("%w: %w", storage.ErrPreconditionFailed, err)
		}
	}
	return err
}

func isAlreadyExists(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode == http.StatusConflict &&
			strings.Contains(strings.ToLower(respErr.ErrorCode), "alreadyexists")
	}
	return false
}

func isPreconditionFailure(err error) bool {
	var respErr *azcore.ResponseError
	if !errors.As(err, &respErr) {
		return false
	}
	if respErr.StatusCode == http.StatusPreconditionFailed {
		return true
	}
	// Azurite devuelve 409 BlobAlreadyExists ante If-None-Match cuando el
	// objeto ya existe.
	return respErr.StatusCode == http.StatusConflict &&
		strings.Contains(strings.ToLower(respErr.ErrorCode), "alreadyexists")
}

func toSDKMetadata(in map[string]string) map[string]*string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]*string, len(in))
	for k, v := range in {
		out[k] = ptr(v)
	}
	return out
}

func fromSDKMetadata(in map[string]*string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

func ptr[T any](v T) *T { return &v }

func derefString(v *string, def string) string {
	if v == nil {
		return def
	}
	return *v
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func derefETag(v *azcore.ETag) string {
	if v == nil {
		return ""
	}
	return string(*v)
}

func derefTime(v *time.Time) time.Time {
	if v == nil {
		return time.Time{}
	}
	return *v
}
