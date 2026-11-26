package documents

import (
	"net/http"
	"time"

	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/inference"
	"mar-backend/internal/storage"
)

func FromConfig(cfg config.Config, objects storage.ObjectStore, resources storage.ResourceRepository, vectors storage.VectorStore) *Pipeline {
	embeddingURL := cfg.Inference.BGEM3URL
	var accelerator *inference.GPU
	if cfg.Inference.BGEM3CPUURL != "" {
		embeddingURL = cfg.Inference.BGEM3CPUURL
		accelerator = inference.NewGPU(cfg.Inference.BGEM3URL, 2)
	}
	return &Pipeline{
		Objects: objects, Resources: resources, Vectors: vectors,
		RawContainer: cfg.Blob.ContainerRaw, DerivedContainer: cfg.Blob.ContainerDerived,
		Collection: cfg.Qdrant.TextCollection, ModelRevision: cfg.Documents.ModelRevision,
		Extract: HTTPExtractor{
			URL: cfg.Documents.ExtractorURL, Client: &http.Client{Timeout: 5 * time.Minute},
			MaxResponseBytes: checkpointLimit,
		},
		Embed: HTTPEmbedder{
			URL: embeddingURL, Client: &http.Client{Timeout: cfg.Inference.RequestTimeout}, GPU: accelerator,
			MaxResponseBytes: cfg.Inference.MaxResponseBytes,
		},
	}
}
