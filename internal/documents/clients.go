// Package documents coordinates extraction, embeddings and document search.
package documents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"path/filepath"
	"strings"

	"mar-backend/internal/platform/inference"
	"mar-backend/internal/storage"
)

const modelName = "BAAI/bge-m3"
const vectorSize = 1024

type Extractor interface {
	Extract(context.Context, storage.Resource, io.Reader) ([]Fragment, error)
}

type Embedder interface {
	Embed(context.Context, []string) ([][]float32, error)
}

// RemoteError distinguishes invalid source data from transport and model failures.
type RemoteError struct {
	Code      string
	Permanent bool
	Cause     error
}

func (e *RemoteError) Error() string { return fmt.Sprintf("%s: %v", e.Code, e.Cause) }
func (e *RemoteError) Unwrap() error { return e.Cause }

type HTTPExtractor struct {
	URL              string
	Client           *http.Client
	MaxResponseBytes int64
}

func (e HTTPExtractor) Extract(ctx context.Context, resource storage.Resource, body io.Reader) ([]Fragment, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.URL, "/")+"/extract", body)
	if err != nil {
		return nil, err
	}
	req.ContentLength = resource.FileSize
	req.Header.Set("Content-Type", TextContentType(resource.FileName, resource.ContentType))
	req.Header.Set("X-Content-SHA256", resource.ContentHash)
	var out struct {
		Fragments []Fragment `json:"fragments"`
	}
	if err := requestJSON(e.Client.Do, req, e.MaxResponseBytes, &out); err != nil {
		return nil, err
	}
	return out.Fragments, nil
}

type HTTPEmbedder struct {
	URL              string
	Client           *http.Client
	GPU              *inference.GPU
	MaxResponseBytes int64
}

func (e HTTPEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(struct {
		Model string   `json:"model"`
		Input []string `json:"input"`
	}{modelName, texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.URL, "/")+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	var out struct {
		Data []struct {
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	do := func(request *http.Request) (*http.Response, error) {
		return e.GPU.Do(e.Client, request, "/v1/embeddings")
	}
	if err := requestJSON(do, req, e.MaxResponseBytes, &out); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(texts))
	if len(out.Data) != len(vectors) {
		return nil, errors.New("el modelo devolvió un número inesperado de vectores")
	}
	for _, item := range out.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= len(vectors) || vectors[*item.Index] != nil {
			return nil, errors.New("índice de embedding inválido o duplicado")
		}
		vectors[*item.Index] = item.Embedding
	}
	return vectors, validateVectors(vectors, len(texts))
}

func validateVectors(vectors [][]float32, count int) error {
	if len(vectors) != count {
		return errors.New("número de embeddings incompatible con el lote")
	}
	for _, vector := range vectors {
		if len(vector) != vectorSize {
			return fmt.Errorf("dimensión de embedding %d, esperada %d", len(vector), vectorSize)
		}
		var nonzero bool
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return errors.New("embedding con valor no finito")
			}
			nonzero = nonzero || value != 0
		}
		if !nonzero {
			return errors.New("embedding de norma cero")
		}
	}
	return nil
}

func requestJSON(do func(*http.Request) (*http.Response, error), req *http.Request, limit int64, target any) error {
	response, err := do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body)
		return &RemoteError{
			Code:      body.Code,
			Permanent: response.StatusCode == http.StatusUnprocessableEntity || response.StatusCode == http.StatusRequestEntityTooLarge,
			Cause:     fmt.Errorf("servicio interno: HTTP %d", response.StatusCode),
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("respuesta del servicio interno supera el límite")
	}
	return json.Unmarshal(data, target)
}

func TextContentType(filename, contentType string) string {
	if contentType == "text/plain" {
		switch strings.ToLower(filepath.Ext(filename)) {
		case ".csv":
			return "text/csv"
		case ".md":
			return "text/markdown"
		}
	}
	return contentType
}
