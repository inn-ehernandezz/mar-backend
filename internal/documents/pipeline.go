package documents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mar-backend/internal/storage"
)

const extractionVersion = "document-v1"
const batchSize = 8
const maxFragments = 3000
const checkpointLimit = 24 << 20

type Fragment struct {
	Text    string `json:"text"`
	Page    int    `json:"page,omitempty"`
	Pages   []int  `json:"pages,omitempty"`
	Section string `json:"section,omitempty"`
	Sheet   string `json:"sheet,omitempty"`
	Cells   string `json:"cells,omitempty"`
}

type Failure struct {
	Code      string
	Permanent bool
	Err       error
}

func (f *Failure) Error() string { return fmt.Sprintf("%s: %v", f.Code, f.Err) }
func (f *Failure) Unwrap() error { return f.Err }

type Pipeline struct {
	Objects          storage.ObjectStore
	Resources        storage.ResourceRepository
	Vectors          storage.VectorStore
	Extract          Extractor
	Embed            Embedder
	RawContainer     string
	DerivedContainer string
	Collection       string
	ModelRevision    string
}

func (p *Pipeline) IndexVersion() string { return extractionVersion + ":" + p.ModelRevision }

func Supported(contentType string) bool {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0])) {
	case "application/pdf", "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"text/plain", "text/markdown", "text/csv":
		return true
	}
	return false
}

// Run checkpoints extraction and each embedding batch before moving to the
// next dependency. Acknowledged and ambiguous Qdrant writes are safe to repeat.
func (p *Pipeline) Run(ctx context.Context, resource storage.Resource, stage func(string, int32) error) error {
	root := strings.TrimSuffix(resource.BlobPath, "/original") + "/" + extractionVersion + "/" + resource.ContentHash
	path := root + "/fragments.json"
	if err := stage("extracting", 10); err != nil {
		return err
	}
	var fragments []Fragment
	hit, err := readCheckpoint(ctx, p.Objects, p.DerivedContainer, path, &fragments)
	if err != nil {
		return failed("extraction_checkpoint", err)
	}
	if !hit {
		source, err := p.Objects.Get(ctx, p.RawContainer, resource.BlobPath)
		if err != nil {
			return &Failure{"original_read_failed", errors.Is(err, storage.ErrNotFound), err}
		}
		fragments, err = p.Extract.Extract(ctx, resource, source.Body)
		_ = source.Body.Close()
		if err != nil {
			var remote *RemoteError
			if errors.As(err, &remote) && remote.Permanent {
				code := "document_rejected"
				switch remote.Code {
				case "no_extractable_text", "document_integrity_error", "document_too_large",
					"too_many_fragments", "too_many_cells", "invalid_document_format",
					"office_archive_too_large", "invalid_office_document", "document_conversion_failed":
					code = remote.Code
				}
				return &Failure{code, true, err}
			}
			return failed("extraction_unavailable", err)
		}
		if err := validateFragments(fragments); err != nil {
			return &Failure{"invalid_document_fragments", true, err}
		}
		if err := writeCheckpoint(ctx, p.Objects, p.DerivedContainer, path, fragments, &fragments); err != nil {
			return failed("extraction_checkpoint", err)
		}
	}
	if err := validateFragments(fragments); err != nil {
		return failed("extraction_checkpoint", err)
	}

	for start := 0; start < len(fragments); start += batchSize {
		end := min(start+batchSize, len(fragments))
		batch := fragments[start:end]
		progress := int32(25 + start*65/len(fragments))
		if err := stage("embedding", progress); err != nil {
			return err
		}
		var vectors [][]float32
		batchPath := fmt.Sprintf("%s/%s/%06d.json", root, p.ModelRevision, start)
		hit, err := readCheckpoint(ctx, p.Objects, p.DerivedContainer, batchPath, &vectors)
		if err != nil {
			return failed("embedding_checkpoint", err)
		}
		if !hit {
			texts := make([]string, len(batch))
			for i, fragment := range batch {
				texts[i] = fragment.Text
				if fragment.Section != "" {
					texts[i] = fragment.Section + "\n" + texts[i]
				}
				if fragment.Sheet != "" {
					texts[i] = fragment.Sheet + "\n" + texts[i]
				}
			}
			vectors, err = p.Embed.Embed(ctx, texts)
			if err != nil {
				return failed("embedding_unavailable", err)
			}
			if err := validateVectors(vectors, len(batch)); err != nil {
				return failed("embedding_response_invalid", err)
			}
			if err := writeCheckpoint(ctx, p.Objects, p.DerivedContainer, batchPath, vectors, &vectors); err != nil {
				return failed("embedding_checkpoint", err)
			}
		}
		if err := validateVectors(vectors, len(batch)); err != nil {
			return failed("embedding_checkpoint", err)
		}
		if err := stage("indexing", progress); err != nil {
			return err
		}
		points := make([]storage.VectorPoint, len(batch))
		for i, fragment := range batch {
			id := storage.DeterministicPointID("document", resource.TenantID, resource.ResourceID, p.IndexVersion(), strconv.Itoa(start+i))
			pages := make([]any, len(fragment.Pages))
			for i, page := range fragment.Pages {
				pages[i] = page
			}
			points[i] = storage.VectorPoint{ID: id, Vectors: map[string][]float32{"dense": vectors[i]},
				Payload: map[string]any{
					storage.FieldTenantID: resource.TenantID, storage.FieldResourceID: resource.ResourceID,
					storage.FieldFragmentID: id, storage.FieldContentType: resource.ContentType,
					storage.FieldOriginalBlobPath: resource.BlobPath, storage.FieldDerivedBlobPath: path,
					storage.FieldModelName: modelName, storage.FieldModelVersion: p.ModelRevision,
					storage.FieldSchemaVersion: storage.PayloadSchemaVersion, storage.FieldIndexVersion: p.IndexVersion(),
					"text": fragment.Text, storage.FieldPageNumber: fragment.Page, "pages": pages,
					storage.FieldSection: fragment.Section, "sheet": fragment.Sheet, "cells": fragment.Cells,
				}}
		}
		if err := p.Vectors.Upsert(ctx, p.Collection, points); err != nil {
			return failed("index_unavailable", err)
		}
	}

	if err := stage("publishing", 95); err != nil {
		return err
	}
	return p.publish(ctx, resource)
}

func (p *Pipeline) publish(ctx context.Context, resource storage.Resource) error {
	for range 5 {
		current, err := p.Resources.Get(ctx, resource.TenantID, resource.ResourceID)
		if err != nil {
			return failed("publish_resource", err)
		}
		if current.Status == storage.ResourceProcessed && current.IndexVersion == p.IndexVersion() {
			return nil
		}
		if current.ContentHash != resource.ContentHash {
			return &Failure{"resource_changed", true, errors.New("el original del recurso cambió")}
		}
		current.Status, current.IndexVersion = storage.ResourceProcessed, p.IndexVersion()
		_, err = p.Resources.Update(ctx, current, current.ETag)
		if !errors.Is(err, storage.ErrConflict) {
			if err != nil {
				return failed("publish_resource", err)
			}
			return nil
		}
	}
	return failed("publish_conflict", storage.ErrConflict)
}

func failed(code string, err error) *Failure { return &Failure{Code: code, Err: err} }

func validateFragments(fragments []Fragment) error {
	if len(fragments) == 0 || len(fragments) > maxFragments {
		return errors.New("documento sin texto o con demasiados fragmentos")
	}
	for _, fragment := range fragments {
		if strings.TrimSpace(fragment.Text) == "" || len(fragment.Text) > 4800 || len(fragment.Section) > 2048 ||
			len(fragment.Sheet) > 256 || len(fragment.Cells) > 128 || fragment.Page < 0 || len(fragment.Pages) > 200 {
			return errors.New("fragmento fuera de límites")
		}
		for _, page := range fragment.Pages {
			if page < 1 || page > 200 {
				return errors.New("referencia de página fuera de límites")
			}
		}
	}
	return nil
}

func readCheckpoint[T any](ctx context.Context, objects storage.ObjectStore, container, name string, target *T) (bool, error) {
	object, err := objects.Get(ctx, container, name)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer object.Body.Close()
	data, err := io.ReadAll(io.LimitReader(object.Body, checkpointLimit+1))
	if err != nil {
		return false, err
	}
	if len(data) > checkpointLimit {
		return false, errors.New("checkpoint supera el límite")
	}
	digest := sha256.Sum256(data)
	if expected := object.Info.Metadata["content_sha256"]; expected != "" && expected != hex.EncodeToString(digest[:]) {
		return false, errors.New("SHA-256 del checkpoint no coincide")
	}
	return true, json.Unmarshal(data, target)
}

func writeCheckpoint[T any](ctx context.Context, objects storage.ObjectStore, container, name string, value T, target *T) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(body) > checkpointLimit {
		return errors.New("checkpoint supera el límite")
	}
	digest := sha256.Sum256(body)
	_, err = objects.Put(ctx, storage.PutObjectInput{
		Container: container, Name: name, Body: bytes.NewReader(body), Size: int64(len(body)),
		ContentType: "application/json", ContentHash: hex.EncodeToString(digest[:]), IfNoneMatch: true,
	})
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		return err
	}
	// Another worker committed the canonical checkpoint first. Use its value.
	hit, err := readCheckpoint(ctx, objects, container, name, target)
	if err != nil {
		return err
	}
	if !hit {
		return storage.ErrNotFound
	}
	return nil
}
