package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/health"
	"mar-backend/internal/platform/persistence"
	"mar-backend/internal/storage"
	"mar-backend/internal/storage/blob"
	"mar-backend/internal/storage/qdrant"
	"mar-backend/internal/storage/table"
)

// dependencies agrupa los adaptadores de almacenamiento construidos desde la
// configuración. Es el composition root del binario: construye las
// implementaciones concretas de los puertos de storage y las inyecta en la
// capa HTTP. Ningún módulo de negocio conoce estos tipos concretos.
type dependencies struct {
	Blob   *blob.Store
	Table  *table.Store
	Qdrant *qdrant.Store
}

// buildDependencies construye los adaptadores y registra las comprobaciones de
// disponibilidad. Un error aquí solo refleja configuración incoherente (por
// ejemplo, credenciales incompletas ya detectadas en config.Validate): la mera
// indisponibilidad de un servicio remoto no impide arrancar, porque los
// clientes son perezosos y el estado se refleja en /health/ready.
func buildDependencies(cfg config.Config, checks *health.Registry) (*dependencies, error) {
	blobStore, tableStore, err := persistence.New(cfg)
	if err != nil {
		return nil, err
	}

	vectorStore, err := qdrant.New(qdrant.Config{
		URL:            cfg.Qdrant.URL,
		APIKey:         cfg.Qdrant.APIKey,
		UseTLS:         cfg.Qdrant.UseTLS,
		RequestTimeout: cfg.Qdrant.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("almacenamiento vectorial: %w", err)
	}

	// Las tres dependencias de persistencia de la Fase 2 son obligatorias: si
	// una no responde, el servicio está degradado y /health/ready devuelve 503.
	checks.Register(
		health.Check{Name: "qdrant", Fn: vectorStore.Ping},
		health.Check{Name: "blob", Fn: blobStore.Ping},
		health.Check{Name: "table", Fn: tableStore.Ping},
	)

	return &dependencies{Blob: blobStore, Table: tableStore, Qdrant: vectorStore}, nil
}

// ensureStorage prepara los recursos persistentes locales de forma idempotente:
// contenedores de Blob, tablas de Table y colecciones de Qdrant. Los fallos no
// abortan el arranque (la sonda de disponibilidad los reflejará) y nunca se
// elimina ni se modifica un recurso existente incompatible.
func ensureStorage(ctx context.Context, cfg config.Config, deps *dependencies, logger *slog.Logger) {
	for _, container := range []string{
		cfg.Blob.ContainerRaw,
		cfg.Blob.ContainerDerived,
		cfg.Blob.ContainerFrames,
		cfg.Blob.ContainerTranscripts,
	} {
		if err := deps.Blob.EnsureContainer(ctx, container); err != nil {
			logger.Error("no se pudo preparar el contenedor de Blob Storage",
				slog.String("container", container), slog.String("error", err.Error()))
		}
	}

	if err := deps.Table.EnsureSchema(ctx); err != nil {
		logger.Error("no se pudo preparar el esquema de Table Storage",
			slog.String("error", err.Error()))
	}

	// Colección textual: BGE-M3, vector denso de 1024 dimensiones, coseno.
	textSpec := storage.CollectionSpec{
		Name: cfg.Qdrant.TextCollection,
		Vectors: map[string]storage.VectorParams{
			"dense": {
				Size:     uint64(cfg.Qdrant.TextCollectionVectorSize),
				Distance: storage.DistanceCosine,
			},
		},
		Indexes: payloadIndexes(),
	}
	if _, err := deps.Qdrant.EnsureCollection(ctx, textSpec); err != nil {
		reportEnsureCollectionError(logger, cfg.Qdrant.TextCollection, err)
	}

	// Colección visual: Cosmos-Embed1. La dimensión oficial por variante aún no
	// está confirmada (ver ADR 0004), de modo que la colección solo se crea si
	// la configuración declara una dimensión válida. Sin ella no se indexa
	// contenido visual en lugar de crear una colección con una dimensión
	// inventada.
	if cfg.Qdrant.VisualCollectionVectorSize > 0 {
		visualSpec := storage.CollectionSpec{
			Name: cfg.Qdrant.VisualCollection,
			Vectors: map[string]storage.VectorParams{
				"dense": {
					Size:     uint64(cfg.Qdrant.VisualCollectionVectorSize),
					Distance: storage.DistanceCosine,
				},
			},
			Indexes: payloadIndexes(),
		}
		if _, err := deps.Qdrant.EnsureCollection(ctx, visualSpec); err != nil {
			reportEnsureCollectionError(logger, cfg.Qdrant.VisualCollection, err)
		}
	} else {
		logger.Warn("colección vectorial visual omitida: MAR_QDRANT_VISUAL_VECTOR_SIZE no está definida",
			slog.String("collection", cfg.Qdrant.VisualCollection),
			slog.String("motivo", "dimensión de Cosmos-Embed1 pendiente de confirmar (ADR 0004)"))
	}
}

// payloadIndexes devuelve los índices de payload comunes a las colecciones de
// MAR: aislamiento por tenant, borrado por recurso y filtrado por tipo.
func payloadIndexes() []storage.PayloadIndex {
	return []storage.PayloadIndex{
		{Field: storage.FieldTenantID, Type: storage.IndexKeyword},
		{Field: storage.FieldResourceID, Type: storage.IndexKeyword},
		{Field: storage.FieldContentType, Type: storage.IndexKeyword},
	}
}

// reportEnsureCollectionError distingue la incompatibilidad de esquema (que no
// debe resolverse nunca eliminando datos) del fallo de conexión transitorio.
func reportEnsureCollectionError(logger *slog.Logger, collection string, err error) {
	if errors.Is(err, storage.ErrIncompatibleCollection) {
		logger.Error("la colección existente es incompatible con la configuración; no se ha modificado ni eliminado",
			slog.String("collection", collection), slog.String("error", err.Error()))
		return
	}
	logger.Error("no se pudo preparar la colección vectorial",
		slog.String("collection", collection), slog.String("error", err.Error()))
}
