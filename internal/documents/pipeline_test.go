package documents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"mar-backend/internal/storage"
)

func TestPipelineResumesAfterDependencyFailures(t *testing.T) {
	ctx := context.Background()
	resource := storage.Resource{
		TenantID: "demo", ResourceID: "resource", FileName: "sample.pdf",
		BlobPath: "tenants/demo/resource/original", ContentHash: "hash",
		ContentType: "application/pdf", Status: storage.ResourceStored,
	}
	objects := &testObjects{blobs: map[string][]byte{"raw/" + resource.BlobPath: []byte("original")}}
	repo := &testResources{resource: resource}
	embed := &testEmbedder{failCall: 2}
	extractor := &testExtractor{}
	index := &testIndex{points: make(map[string]storage.VectorPoint)}
	pipeline := &Pipeline{
		Objects: objects, Resources: repo, Vectors: index, Extract: extractor, Embed: embed,
		RawContainer: "raw", DerivedContainer: "derived", Collection: "test", ModelRevision: "test-revision",
	}
	stage := func(string, int32) error { return nil }
	assertFailure(t, pipeline.Run(ctx, resource, stage), "embedding_unavailable")
	if extractor.calls != 1 || embed.calls != 2 || len(index.points) != batchSize {
		t.Fatalf("extraction calls %d, embedding calls %d, points %d", extractor.calls, embed.calls, len(index.points))
	}
	if hits, err := pipeline.Search(ctx, "demo", "relevant", "", 10); err != nil || len(hits) != 0 {
		t.Fatalf("partial index visible: %v, %v", hits, err)
	}
	before := embed.calls
	index.failPut = true
	assertFailure(t, pipeline.Run(ctx, resource, stage), "index_unavailable")
	if extractor.calls != 1 || embed.calls != before {
		t.Fatalf("index outage repeated durable computation: extract=%d embed=%d", extractor.calls, embed.calls)
	}
	index.failPut = false
	if err := pipeline.Run(ctx, resource, stage); err != nil {
		t.Fatal(err)
	}
	if extractor.calls != 1 || embed.calls != before+1 || len(index.points) != batchSize+1 {
		t.Fatalf("resume recomputed or duplicated data: extract=%d embed=%d points=%d", extractor.calls, embed.calls, len(index.points))
	}
	if repo.resource.Status != storage.ResourceProcessed {
		t.Fatal("resource not published")
	}
	if hits, err := pipeline.Search(ctx, "demo", "relevant", resource.ResourceID, 20); err != nil || len(hits) != batchSize+1 || hits[0].Page != 2 {
		t.Fatalf("published search: %v, %v", hits, err)
	}
	if hits, err := pipeline.Search(ctx, "other-tenant", "relevant", "", 20); err != nil || len(hits) != 0 {
		t.Fatalf("tenant leak: %v, %v", hits, err)
	}
}

func TestCancelledBeforePublishingRemainsInvisible(t *testing.T) {
	ctx := context.Background()
	resource := storage.Resource{
		TenantID: "demo", ResourceID: "resource", BlobPath: "one/original",
		ContentHash: "hash", Status: storage.ResourceStored,
	}
	repo := &testResources{resource: resource}
	pipeline := &Pipeline{
		Objects:   &testObjects{blobs: map[string][]byte{"raw/one/original": []byte("test")}},
		Resources: repo, Vectors: &testIndex{points: make(map[string]storage.VectorPoint)},
		Extract: &testExtractor{}, Embed: &testEmbedder{},
		RawContainer: "raw", DerivedContainer: "derived", Collection: "test", ModelRevision: "test-revision",
	}
	err := pipeline.Run(ctx, resource, func(stage string, _ int32) error {
		if stage == "publishing" {
			return context.Canceled
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || repo.resource.Status != storage.ResourceStored {
		t.Fatalf("cancelled run published a resource: %v, %v", repo.resource.Status, err)
	}
	if hits, err := pipeline.Search(ctx, "demo", "test", "", 10); err != nil || len(hits) != 0 {
		t.Fatalf("cancelled document visible: %v, %v", hits, err)
	}
}

func assertFailure(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

type testObjects struct {
	storage.ObjectStore
	blobs map[string][]byte
}

func (s *testObjects) Get(_ context.Context, container, key string) (storage.ObjectReader, error) {
	data, ok := s.blobs[container+"/"+key]
	if !ok {
		return storage.ObjectReader{}, storage.ErrNotFound
	}
	return storage.ObjectReader{Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (s *testObjects) Put(_ context.Context, in storage.PutObjectInput) (storage.ObjectInfo, error) {
	key := in.Container + "/" + in.Name
	if _, exists := s.blobs[key]; exists && in.IfNoneMatch {
		return storage.ObjectInfo{}, storage.ErrPreconditionFailed
	}
	data, err := io.ReadAll(in.Body)
	if err == nil {
		s.blobs[key] = data
	}
	return storage.ObjectInfo{}, err
}

type testResources struct {
	storage.ResourceRepository
	resource storage.Resource
}

func (r *testResources) Get(_ context.Context, tenant, id string) (storage.Resource, error) {
	if r.resource.TenantID != tenant || r.resource.ResourceID != id {
		return storage.Resource{}, storage.ErrNotFound
	}
	return r.resource, nil
}

func (r *testResources) Update(_ context.Context, resource storage.Resource, _ string) (storage.Resource, error) {
	r.resource = resource
	return resource, nil
}

type testExtractor struct{ calls int }

func (e *testExtractor) Extract(context.Context, storage.Resource, io.Reader) ([]Fragment, error) {
	e.calls++
	out := make([]Fragment, batchSize+1)
	for i := range out {
		out[i] = Fragment{Text: fmt.Sprint("fragment ", i), Page: 2}
	}
	return out, nil
}

type testEmbedder struct{ calls, failCall int }

func (e *testEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.calls++
	if e.calls == e.failCall {
		return nil, errors.New("model temporarily unavailable")
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, vectorSize)
		out[i][0] = 1
	}
	return out, nil
}

type testIndex struct {
	storage.VectorStore
	points  map[string]storage.VectorPoint
	failPut bool
}

func (q *testIndex) Upsert(_ context.Context, _ string, points []storage.VectorPoint) error {
	if q.failPut {
		return errors.New("Qdrant unavailable")
	}
	for _, point := range points {
		q.points[point.ID] = point
	}
	return nil
}

func (q *testIndex) Search(_ context.Context, _ string, query storage.SearchQuery) ([]storage.ScoredPoint, error) {
	var out []storage.ScoredPoint
	for _, point := range q.points {
		matches := true
		for _, match := range query.Filter.Must {
			matches = matches && point.Payload[match.Field] == match.Value
		}
		if matches {
			out = append(out, storage.ScoredPoint{ID: point.ID, Payload: point.Payload, Score: 1})
		}
	}
	return out, nil
}
