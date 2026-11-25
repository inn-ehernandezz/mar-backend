// Package config carga y valida la configuración del servicio desde variables
// de entorno. La configuración se valida al arranque: un servicio mal
// configurado no debe llegar a aceptar tráfico.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Entornos soportados.
const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

const envPrefix = "MAR_"

// Credenciales públicas del emulador Azurite, documentadas por Microsoft para
// el entorno local. No son secretos y no deben usarse fuera de él.
const (
	azuriteAccountName = "devstoreaccount1"
	azuriteAccountKey  = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="
)

// Config es la configuración completa del servicio.
type Config struct {
	Env         string
	ServiceName string

	Server    ServerConfig
	Log       LogConfig
	Tenancy   TenancyConfig
	Qdrant    QdrantConfig
	Blob      BlobConfig
	Table     TableConfig
	Bus       BusConfig
	Inference InferenceConfig
	Documents DocumentConfig
}

// ServerConfig agrupa los parámetros del servidor HTTP.
type ServerConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
	RequestTimeout    time.Duration
	MaxBodyBytes      int64
}

// LogConfig agrupa los parámetros del registro estructurado.
type LogConfig struct {
	Level  string
	Format string
}

// TenancyConfig define cómo se resuelve el tenant. Es un punto de extensión:
// hoy se resuelve por cabecera; cuando los requisitos corporativos de
// autenticación estén definidos, se sustituirá la implementación.
type TenancyConfig struct {
	Header        string
	RequireHeader bool
	DevTenant     string
}

// QdrantConfig describe la conexión y las colecciones vectoriales.
type QdrantConfig struct {
	URL    string
	APIKey string
	UseTLS bool

	TextCollection           string
	TextCollectionVectorSize int

	VisualCollection           string
	VisualCollectionVectorSize int

	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

// BlobConfig describe Azure Blob Storage y sus contenedores.
type BlobConfig struct {
	ConnectionString string
	Endpoint         string
	AccountName      string
	AccountKey       string

	ContainerRaw         string
	ContainerDerived     string
	ContainerFrames      string
	ContainerTranscripts string

	RequestTimeout time.Duration
}

// TableConfig describe Azure Table Storage y la tabla de trabajos.
type TableConfig struct {
	ConnectionString string
	Endpoint         string
	AccountName      string
	AccountKey       string

	// TableResources y TableJobs son las tablas de Table Storage. La tabla de
	// trabajos alojará también las entidades `outbox` en la misma partición,
	// requisito del patrón outbox (ver ADR 0002 y ADR 0006).
	TableResources string
	TableJobs      string

	RequestTimeout time.Duration
}

// BusConfig describe Azure Service Bus.
type BusConfig struct {
	ConnectionString   string
	Enabled            bool
	QueueIngestion     string
	QueueEmbedding     string
	QueueIndexing      string
	TopicNotifications string
	MaxConcurrent      int
}

type DocumentConfig struct {
	Enabled       bool
	ExtractorURL  string
	ModelRevision string
}

// InferenceConfig describe los endpoints de los motores de inferencia.
// Los nombres son lógicos: cambiar de motor no afecta a la lógica de negocio.
type InferenceConfig struct {
	// MockEnabled habilita adaptadores de desarrollo. Nunca en producción.
	MockEnabled bool

	Qwen3URL         string
	Qwen3VisionURL   string
	BGEM3URL         string
	RerankerURL      string
	WhisperURL       string
	CosmosEmbed1URL  string
	RequestTimeout   time.Duration
	MaxResponseBytes int64
}

// Load construye la configuración desde el entorno y la valida.
func Load() (Config, error) {
	cfg := defaults()

	cfg.Env = getString("MAR_ENV", cfg.Env)
	cfg.ServiceName = getString("MAR_SERVICE_NAME", cfg.ServiceName)

	cfg.Server.Addr = getString("MAR_HTTP_ADDR", cfg.Server.Addr)
	cfg.Server.ReadHeaderTimeout = getDuration("MAR_HTTP_READ_HEADER_TIMEOUT", cfg.Server.ReadHeaderTimeout)
	cfg.Server.ReadTimeout = getDuration("MAR_HTTP_READ_TIMEOUT", cfg.Server.ReadTimeout)
	cfg.Server.WriteTimeout = getDuration("MAR_HTTP_WRITE_TIMEOUT", cfg.Server.WriteTimeout)
	cfg.Server.IdleTimeout = getDuration("MAR_HTTP_IDLE_TIMEOUT", cfg.Server.IdleTimeout)
	cfg.Server.ShutdownTimeout = getDuration("MAR_HTTP_SHUTDOWN_TIMEOUT", cfg.Server.ShutdownTimeout)
	cfg.Server.RequestTimeout = getDuration("MAR_HTTP_REQUEST_TIMEOUT", cfg.Server.RequestTimeout)
	cfg.Server.MaxBodyBytes = getInt64("MAR_HTTP_MAX_BODY_BYTES", cfg.Server.MaxBodyBytes)

	cfg.Log.Level = getString("MAR_LOG_LEVEL", cfg.Log.Level)
	cfg.Log.Format = getString("MAR_LOG_FORMAT", cfg.Log.Format)

	cfg.Tenancy.Header = getString("MAR_TENANT_HEADER", cfg.Tenancy.Header)
	cfg.Tenancy.RequireHeader = getBool("MAR_TENANT_REQUIRE_HEADER", cfg.Tenancy.RequireHeader)
	cfg.Tenancy.DevTenant = getString("MAR_TENANT_DEV_TENANT", cfg.Tenancy.DevTenant)

	cfg.Qdrant.URL = getString("MAR_QDRANT_URL", cfg.Qdrant.URL)
	cfg.Qdrant.APIKey = getString("MAR_QDRANT_API_KEY", cfg.Qdrant.APIKey)
	cfg.Qdrant.UseTLS = getBool("MAR_QDRANT_USE_TLS", cfg.Qdrant.UseTLS)
	cfg.Qdrant.TextCollection = getString("MAR_QDRANT_TEXT_COLLECTION", cfg.Qdrant.TextCollection)
	cfg.Qdrant.TextCollectionVectorSize = getInt("MAR_QDRANT_TEXT_VECTOR_SIZE", cfg.Qdrant.TextCollectionVectorSize)
	cfg.Qdrant.VisualCollection = getString("MAR_QDRANT_VISUAL_COLLECTION", cfg.Qdrant.VisualCollection)
	cfg.Qdrant.VisualCollectionVectorSize = getInt("MAR_QDRANT_VISUAL_VECTOR_SIZE", cfg.Qdrant.VisualCollectionVectorSize)
	cfg.Qdrant.ConnectTimeout = getDuration("MAR_QDRANT_CONNECT_TIMEOUT", cfg.Qdrant.ConnectTimeout)
	cfg.Qdrant.RequestTimeout = getDuration("MAR_QDRANT_REQUEST_TIMEOUT", cfg.Qdrant.RequestTimeout)

	cfg.Blob.ConnectionString = getString("MAR_BLOB_CONNECTION_STRING", cfg.Blob.ConnectionString)
	cfg.Blob.Endpoint = getString("MAR_BLOB_ENDPOINT", cfg.Blob.Endpoint)
	cfg.Blob.AccountName = getString("MAR_BLOB_ACCOUNT_NAME", cfg.Blob.AccountName)
	cfg.Blob.AccountKey = getString("MAR_BLOB_ACCOUNT_KEY", cfg.Blob.AccountKey)
	cfg.Blob.ContainerRaw = getString("MAR_BLOB_CONTAINER_RAW", cfg.Blob.ContainerRaw)
	cfg.Blob.ContainerDerived = getString("MAR_BLOB_CONTAINER_DERIVED", cfg.Blob.ContainerDerived)
	cfg.Blob.ContainerFrames = getString("MAR_BLOB_CONTAINER_FRAMES", cfg.Blob.ContainerFrames)
	cfg.Blob.ContainerTranscripts = getString("MAR_BLOB_CONTAINER_TRANSCRIPTS", cfg.Blob.ContainerTranscripts)
	cfg.Blob.RequestTimeout = getDuration("MAR_BLOB_REQUEST_TIMEOUT", cfg.Blob.RequestTimeout)

	cfg.Table.ConnectionString = getString("MAR_TABLE_CONNECTION_STRING", cfg.Table.ConnectionString)
	cfg.Table.Endpoint = getString("MAR_TABLE_ENDPOINT", cfg.Table.Endpoint)
	cfg.Table.AccountName = getString("MAR_TABLE_ACCOUNT_NAME", cfg.Table.AccountName)
	cfg.Table.AccountKey = getString("MAR_TABLE_ACCOUNT_KEY", cfg.Table.AccountKey)
	cfg.Table.TableResources = getString("MAR_TABLE_RESOURCES", cfg.Table.TableResources)
	cfg.Table.TableJobs = getString("MAR_TABLE_JOBS", cfg.Table.TableJobs)
	cfg.Table.RequestTimeout = getDuration("MAR_TABLE_REQUEST_TIMEOUT", cfg.Table.RequestTimeout)

	cfg.Bus.ConnectionString = getString("MAR_SERVICEBUS_CONNECTION_STRING", cfg.Bus.ConnectionString)
	cfg.Bus.Enabled = getBool("MAR_SERVICEBUS_ENABLED", cfg.Bus.Enabled)
	cfg.Bus.QueueIngestion = getString("MAR_SERVICEBUS_QUEUE_INGESTION", cfg.Bus.QueueIngestion)
	cfg.Bus.QueueEmbedding = getString("MAR_SERVICEBUS_QUEUE_EMBEDDING", cfg.Bus.QueueEmbedding)
	cfg.Bus.QueueIndexing = getString("MAR_SERVICEBUS_QUEUE_INDEXING", cfg.Bus.QueueIndexing)
	cfg.Bus.TopicNotifications = getString("MAR_SERVICEBUS_TOPIC_NOTIFICATIONS", cfg.Bus.TopicNotifications)
	cfg.Bus.MaxConcurrent = getInt("MAR_SERVICEBUS_MAX_CONCURRENT", cfg.Bus.MaxConcurrent)

	cfg.Documents.Enabled = getBool("MAR_DOCUMENTS_ENABLED", cfg.Documents.Enabled)
	cfg.Documents.ExtractorURL = getString("MAR_DOCUMENT_EXTRACTOR_URL", cfg.Documents.ExtractorURL)
	cfg.Documents.ModelRevision = getString("MAR_MODEL_BGE_M3_REVISION", cfg.Documents.ModelRevision)
	cfg.Inference.MockEnabled = getBool("MAR_INFERENCE_MOCK_ENABLED", cfg.Inference.MockEnabled)
	cfg.Inference.Qwen3URL = getString("MAR_MODEL_QWEN3_URL", cfg.Inference.Qwen3URL)
	cfg.Inference.Qwen3VisionURL = getString("MAR_MODEL_QWEN3_VL_URL", cfg.Inference.Qwen3VisionURL)
	cfg.Inference.BGEM3URL = getString("MAR_MODEL_BGE_M3_URL", cfg.Inference.BGEM3URL)
	cfg.Inference.RerankerURL = getString("MAR_MODEL_RERANKER_URL", cfg.Inference.RerankerURL)
	cfg.Inference.WhisperURL = getString("MAR_MODEL_WHISPER_URL", cfg.Inference.WhisperURL)
	cfg.Inference.CosmosEmbed1URL = getString("MAR_MODEL_COSMOS_EMBED1_URL", cfg.Inference.CosmosEmbed1URL)
	cfg.Inference.RequestTimeout = getDuration("MAR_MODEL_REQUEST_TIMEOUT", cfg.Inference.RequestTimeout)
	cfg.Inference.MaxResponseBytes = getInt64("MAR_MODEL_MAX_RESPONSE_BYTES", cfg.Inference.MaxResponseBytes)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func defaults() Config {
	return Config{
		Env:         EnvDevelopment,
		ServiceName: "mar",
		Server: ServerConfig{
			Addr:              "0.0.0.0:8080",
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
			ShutdownTimeout:   20 * time.Second,
			RequestTimeout:    30 * time.Second,
			MaxBodyBytes:      100 << 20, // 100 MiB
		},
		Log: LogConfig{Level: "info", Format: "json"},
		Tenancy: TenancyConfig{
			Header:        "X-Tenant-ID",
			RequireHeader: true,
		},
		Qdrant: QdrantConfig{
			URL:                      "http://localhost:6334",
			TextCollection:           "text_embeddings",
			TextCollectionVectorSize: 1024, // BGE-M3 (confirmado)
			VisualCollection:         "visual_embeddings",
			ConnectTimeout:           5 * time.Second,
			RequestTimeout:           30 * time.Second,
		},
		Blob: BlobConfig{
			Endpoint:             "http://localhost:10000/devstoreaccount1",
			AccountName:          azuriteAccountName,
			AccountKey:           azuriteAccountKey,
			ContainerRaw:         "raw",
			ContainerDerived:     "derived",
			ContainerFrames:      "frames",
			ContainerTranscripts: "transcripts",
			RequestTimeout:       30 * time.Second,
		},
		Table: TableConfig{
			Endpoint:       "http://localhost:10002/devstoreaccount1",
			AccountName:    azuriteAccountName,
			AccountKey:     azuriteAccountKey,
			TableResources: "resources",
			TableJobs:      "jobs",
			RequestTimeout: 10 * time.Second,
		},
		Bus: BusConfig{
			QueueIngestion: "ingestion",
			QueueEmbedding: "embedding",
			QueueIndexing:  "indexing",
			MaxConcurrent:  1,
		},
		Inference: InferenceConfig{
			RequestTimeout:   120 * time.Second,
			MaxResponseBytes: 8 << 20,
		},
	}
}

// Validate comprueba la coherencia de la configuración. Devuelve todos los
// problemas encontrados, no solo el primero.
func (c Config) Validate() error {
	var errs []error

	switch c.Env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		errs = append(errs, fmt.Errorf("MAR_ENV debe ser %q, %q o %q; valor recibido: %q",
			EnvDevelopment, EnvTest, EnvProduction, c.Env))
	}
	if strings.TrimSpace(c.ServiceName) == "" {
		errs = append(errs, errors.New("MAR_SERVICE_NAME no puede estar vacío"))
	}

	// Servidor.
	if strings.TrimSpace(c.Server.Addr) == "" {
		errs = append(errs, errors.New("MAR_HTTP_ADDR no puede estar vacío"))
	}
	errs = append(errs, requirePositiveDuration("MAR_HTTP_READ_HEADER_TIMEOUT", c.Server.ReadHeaderTimeout))
	errs = append(errs, requirePositiveDuration("MAR_HTTP_READ_TIMEOUT", c.Server.ReadTimeout))
	errs = append(errs, requirePositiveDuration("MAR_HTTP_WRITE_TIMEOUT", c.Server.WriteTimeout))
	errs = append(errs, requirePositiveDuration("MAR_HTTP_IDLE_TIMEOUT", c.Server.IdleTimeout))
	errs = append(errs, requirePositiveDuration("MAR_HTTP_SHUTDOWN_TIMEOUT", c.Server.ShutdownTimeout))
	errs = append(errs, requirePositiveDuration("MAR_HTTP_REQUEST_TIMEOUT", c.Server.RequestTimeout))
	if c.Server.MaxBodyBytes <= 0 {
		errs = append(errs, errors.New("MAR_HTTP_MAX_BODY_BYTES debe ser mayor que cero"))
	}

	// Log.
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("MAR_LOG_LEVEL no válido: %q", c.Log.Level))
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("MAR_LOG_FORMAT no válido: %q", c.Log.Format))
	}

	// Tenancy.
	if strings.TrimSpace(c.Tenancy.Header) == "" {
		errs = append(errs, errors.New("MAR_TENANT_HEADER no puede estar vacío"))
	}
	if c.Tenancy.DevTenant != "" && c.IsProduction() {
		errs = append(errs, errors.New(
			"MAR_TENANT_DEV_TENANT no está permitido en producción: sería un mecanismo de identidad no autenticado"))
	}

	// Qdrant.
	if strings.TrimSpace(c.Qdrant.URL) == "" {
		errs = append(errs, errors.New("MAR_QDRANT_URL no puede estar vacío"))
	}
	if strings.TrimSpace(c.Qdrant.TextCollection) == "" {
		errs = append(errs, errors.New("MAR_QDRANT_TEXT_COLLECTION no puede estar vacío"))
	}
	if strings.TrimSpace(c.Qdrant.VisualCollection) == "" {
		errs = append(errs, errors.New("MAR_QDRANT_VISUAL_COLLECTION no puede estar vacío"))
	}
	if c.Qdrant.TextCollectionVectorSize <= 0 {
		errs = append(errs, errors.New(
			"MAR_QDRANT_TEXT_VECTOR_SIZE debe ser mayor que cero; se confirma al desplegar el motor de embeddings"))
	}

	// Blob.
	if !c.Blob.HasCredentials() {
		errs = append(errs, errors.New(
			"Azure Blob requiere MAR_BLOB_CONNECTION_STRING o bien MAR_BLOB_ENDPOINT con MAR_BLOB_ACCOUNT_NAME y MAR_BLOB_ACCOUNT_KEY"))
	}
	for name, value := range map[string]string{
		"MAR_BLOB_CONTAINER_RAW":         c.Blob.ContainerRaw,
		"MAR_BLOB_CONTAINER_DERIVED":     c.Blob.ContainerDerived,
		"MAR_BLOB_CONTAINER_FRAMES":      c.Blob.ContainerFrames,
		"MAR_BLOB_CONTAINER_TRANSCRIPTS": c.Blob.ContainerTranscripts,
	} {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Errorf("%s no puede estar vacío", name))
		}
	}

	// Table.
	if !c.Table.HasCredentials() {
		errs = append(errs, errors.New(
			"Azure Table requiere MAR_TABLE_CONNECTION_STRING o bien MAR_TABLE_ENDPOINT con MAR_TABLE_ACCOUNT_NAME y MAR_TABLE_ACCOUNT_KEY"))
	}
	if strings.TrimSpace(c.Table.TableResources) == "" {
		errs = append(errs, errors.New("MAR_TABLE_RESOURCES no puede estar vacío"))
	}
	if strings.TrimSpace(c.Table.TableJobs) == "" {
		errs = append(errs, errors.New("MAR_TABLE_JOBS no puede estar vacío"))
	}

	// En producción no se admiten destinos locales: indicarían que el servicio
	// apunta a emuladores (Azurite) o a la propia máquina.
	if c.IsProduction() {
		for _, target := range []struct{ name, value string }{
			{"MAR_QDRANT_URL", c.Qdrant.URL},
			{"MAR_BLOB_ENDPOINT", firstNonEmpty(c.Blob.ConnectionString, c.Blob.Endpoint)},
			{"MAR_TABLE_ENDPOINT", firstNonEmpty(c.Table.ConnectionString, c.Table.Endpoint)},
		} {
			if looksLocalEndpoint(target.value) {
				errs = append(errs, fmt.Errorf(
					"%s apunta a un destino local (%q), no permitido en producción",
					target.name, target.value))
			}
		}
	}

	// Service Bus.
	if c.Bus.Enabled {
		if strings.TrimSpace(c.Bus.ConnectionString) == "" {
			errs = append(errs, errors.New("MAR_SERVICEBUS_CONNECTION_STRING es obligatorio si MAR_SERVICEBUS_ENABLED=true"))
		}
		if strings.TrimSpace(c.Bus.QueueIngestion) == "" {
			errs = append(errs, errors.New("MAR_SERVICEBUS_QUEUE_INGESTION es obligatorio si MAR_SERVICEBUS_ENABLED=true"))
		}
		if c.Bus.MaxConcurrent <= 0 {
			errs = append(errs, errors.New("MAR_SERVICEBUS_MAX_CONCURRENT debe ser mayor que cero"))
		}
	}

	// Inferencia.
	if c.Documents.Enabled {
		for name, value := range map[string]string{
			"MAR_DOCUMENT_EXTRACTOR_URL": c.Documents.ExtractorURL,
			"MAR_MODEL_BGE_M3_URL":       c.Inference.BGEM3URL,
		} {
			u, err := url.Parse(value)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				errs = append(errs, fmt.Errorf("%s debe ser una URL base HTTP o HTTPS", name))
			}
		}
		if len(c.Documents.ModelRevision) != 40 || strings.ContainsFunc(c.Documents.ModelRevision, func(r rune) bool {
			return !strings.ContainsRune("0123456789abcdef", r)
		}) {
			errs = append(errs, errors.New("MAR_MODEL_BGE_M3_REVISION debe ser el SHA de 40 caracteres de los pesos"))
		}
		if c.Qdrant.TextCollectionVectorSize != 1024 {
			errs = append(errs, errors.New("BGE-M3 requiere MAR_QDRANT_TEXT_VECTOR_SIZE=1024"))
		}
	}
	if c.Inference.MockEnabled && c.IsProduction() {
		errs = append(errs, errors.New(
			"MAR_INFERENCE_MOCK_ENABLED no está permitido en producción: los resultados serían ficticios"))
	}
	for name, value := range map[string]string{
		"MAR_MODEL_QWEN3_URL":         c.Inference.Qwen3URL,
		"MAR_MODEL_QWEN3_VL_URL":      c.Inference.Qwen3VisionURL,
		"MAR_MODEL_BGE_M3_URL":        c.Inference.BGEM3URL,
		"MAR_MODEL_RERANKER_URL":      c.Inference.RerankerURL,
		"MAR_MODEL_WHISPER_URL":       c.Inference.WhisperURL,
		"MAR_MODEL_COSMOS_EMBED1_URL": c.Inference.CosmosEmbed1URL,
	} {
		if err := validateOptionalURL(name, value); err != nil {
			errs = append(errs, err)
		}
	}
	errs = append(errs, requirePositiveDuration("MAR_MODEL_REQUEST_TIMEOUT", c.Inference.RequestTimeout))
	if c.Inference.MaxResponseBytes <= 0 {
		errs = append(errs, errors.New("MAR_MODEL_MAX_RESPONSE_BYTES debe ser mayor que cero"))
	}

	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}

// IsProduction indica si el servicio se ejecuta en producción.
func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// UsesMockInference indica si se usan adaptadores de inferencia de desarrollo.
func (c Config) UsesMockInference() bool { return c.Inference.MockEnabled }

// HasCredentials indica si hay datos suficientes para autenticarse en Blob.
func (b BlobConfig) HasCredentials() bool {
	if strings.TrimSpace(b.ConnectionString) != "" {
		return true
	}
	return strings.TrimSpace(b.Endpoint) != "" &&
		strings.TrimSpace(b.AccountName) != "" &&
		strings.TrimSpace(b.AccountKey) != ""
}

// HasCredentials indica si hay datos suficientes para autenticarse en Table.
func (t TableConfig) HasCredentials() bool {
	if strings.TrimSpace(t.ConnectionString) != "" {
		return true
	}
	return strings.TrimSpace(t.Endpoint) != "" &&
		strings.TrimSpace(t.AccountName) != "" &&
		strings.TrimSpace(t.AccountKey) != ""
}

// SafeSummary devuelve un resumen de la configuración apto para logs:
// sin claves, secretos ni cadenas de conexión.
func (c Config) SafeSummary() map[string]any {
	configured := func(v string) bool { return strings.TrimSpace(v) != "" }
	return map[string]any{
		"env":                      c.Env,
		"service_name":             c.ServiceName,
		"http_addr":                c.Server.Addr,
		"log_level":                c.Log.Level,
		"tenant_header":            c.Tenancy.Header,
		"qdrant_url":               c.Qdrant.URL,
		"qdrant_text_collection":   c.Qdrant.TextCollection,
		"qdrant_visual_collection": c.Qdrant.VisualCollection,
		"table_resources":          c.Table.TableResources,
		"table_jobs":               c.Table.TableJobs,
		"servicebus_enabled":       c.Bus.Enabled,
		"inference_mock_enabled":   c.Inference.MockEnabled,
		"blob_endpoint":            c.Blob.Endpoint,
		"models_configured": map[string]bool{
			"qwen3":         configured(c.Inference.Qwen3URL),
			"qwen3_vl":      configured(c.Inference.Qwen3VisionURL),
			"bge_m3":        configured(c.Inference.BGEM3URL),
			"reranker":      configured(c.Inference.RerankerURL),
			"whisper":       configured(c.Inference.WhisperURL),
			"cosmos_embed1": configured(c.Inference.CosmosEmbed1URL),
		},
	}
}

// --- utilidades de lectura del entorno ---

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return strings.TrimSpace(v)
	}
	return def
}

func getBool(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	v, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}

func getInt(key string, def int) int {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}

func getInt64(key string, def int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return def
	}
	return v
}

func getDuration(key string, def time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	v, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return v
}

func requirePositiveDuration(name string, value time.Duration) error {
	if value <= 0 {
		return fmt.Errorf("%s debe ser mayor que cero", name)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// looksLocalEndpoint detecta destinos locales o de emulador.
func looksLocalEndpoint(value string) bool {
	v := strings.ToLower(value)
	for _, marker := range []string{"localhost", "127.0.0.1", "0.0.0.0", "::1"} {
		if strings.Contains(v, marker) {
			return true
		}
	}
	return false
}

func validateOptionalURL(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if _, err := url.ParseRequestURI(value); err != nil {
		return fmt.Errorf("%s no es una URL válida: %w", name, err)
	}
	return nil
}
