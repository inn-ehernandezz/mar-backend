package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mar-backend/internal/ingestion"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/ids"
	"mar-backend/internal/platform/persistence"
	"mar-backend/internal/storage"
)

// Exercise real Azure Table batches and the streaming Blob adapter before
// passing the published event to the same consumer used by the worker.
func TestIngestionLifecycle(t *testing.T) {
	t.Setenv("MAR_TABLE_RESOURCES", "maringestionresources")
	t.Setenv("MAR_TABLE_JOBS", "maringestionjobs")
	t.Setenv("MAR_ENV", config.EnvTest)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	objects, tables, err := persistence.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pingCtx, pingCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pingCancel()
	if err := tables.Ping(pingCtx); err != nil {
		t.Skipf("Azurite Table no disponible: %v", err)
	}
	if err := objects.Ping(pingCtx); err != nil {
		t.Skipf("Azurite Blob no disponible: %v", err)
	}
	const container = "mar-it-ingestion"
	if err := objects.EnsureContainer(ctx, container); err != nil {
		t.Fatal(err)
	}
	if err := tables.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	tenant := "ingest-" + ids.New()
	service := &ingestion.Service{
		Objects: objects, Resources: tables.Resources(), Jobs: tables.Jobs(), RawContainer: container,
	}
	cfg.Server.MaxBodyBytes = 256 << 10
	r := NewRouter(RouterDeps{Config: cfg, Logger: discardLogger(), Ingestion: service})
	post := func(key, content string, status int) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, err := form.CreateFormFile("file", "original.txt")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = file.Write([]byte(content))
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/resources", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req.Header.Set("X-Tenant-ID", tenant)
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != status {
			t.Fatalf("upload: HTTP %d, expected %d: %s", rec.Code, status, rec.Body)
		}
		return rec
	}

	type submission struct {
		JobID      string `json:"job_id"`
		ResourceID string `json:"resource_id"`
	}
	readSubmission := func(rec *httptest.ResponseRecorder) submission {
		t.Helper()
		var result submission
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.JobID == "" || result.ResourceID == "" {
			t.Fatalf("missing IDs: %s", rec.Body)
		}
		return result
	}

	post("limit", strings.Repeat("x", int(cfg.Server.MaxBodyBytes)), http.StatusRequestEntityTooLarge)
	original := strings.Repeat("Proyecto MAR\n", 10000)
	first := readSubmission(post("first", original, http.StatusAccepted))
	if replay := readSubmission(post("first", original, http.StatusAccepted)); replay != first {
		t.Fatalf("different IDs on replay: %+v and %+v", first, replay)
	}
	post("first", "different", http.StatusConflict)
	second := readSubmission(post("cancel", "another file", http.StatusAccepted))
	headers := map[string]string{"X-Tenant-ID": tenant}

	if rec := do(t, r, http.MethodGet, "/v1/resources/"+first.ResourceID, headers); rec.Code != http.StatusOK {
		t.Fatalf("resource: %s", rec.Body)
	}
	if rec := do(t, r, http.MethodGet, "/v1/jobs/"+first.JobID, map[string]string{"X-Tenant-ID": tenant + "-other"}); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant lookup: HTTP %d", rec.Code)
	}
	if rec := do(t, r, http.MethodPost, "/v1/jobs/"+second.JobID+"/cancel", headers); rec.Code != http.StatusOK {
		t.Fatalf("cancel: %s", rec.Body)
	}

	events := make(map[string]ingestion.Event)
	err = service.Jobs.VisitPending(ctx, time.Now(), func(entry storage.OutboxEntry) error {
		if entry.TenantID != tenant {
			return nil
		}
		var event ingestion.Event
		if err := json.Unmarshal(entry.Body, &event); err != nil {
			return err
		}
		events[event.JobID] = event
		return service.Jobs.MarkSent(ctx, entry)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected two publications, got %d", len(events))
	}

	page, err := service.Jobs.List(ctx, storage.JobQuery{TenantID: tenant})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("jobs list includes non-job entities or is incomplete: %+v, %v", page, err)
	}
	for _, job := range page.Items {
		if reason, err := service.Process(ctx, events[job.JobID]); reason != "" || err != nil {
			t.Fatalf("process: %q, %v", reason, err)
		}
		if reason, err := service.Process(ctx, events[job.JobID]); reason != "" || err != nil {
			t.Fatalf("duplicate: %q, %v", reason, err)
		}
	}
	job, err := service.Jobs.Get(ctx, tenant, first.JobID)
	if err != nil || job.Status != storage.JobCompleted || job.Attempts != 1 || job.Progress != 100 || job.Pipeline != "document" {
		t.Fatalf("completed job: %+v, %v", job, err)
	}
	job, err = service.Jobs.Get(ctx, tenant, second.JobID)
	if err != nil || job.Status != storage.JobCancelled || job.Attempts != 0 {
		t.Fatalf("cancelled job: %+v, %v", job, err)
	}
	if rec := do(t, r, http.MethodPost, "/v1/jobs/"+first.JobID+"/cancel", headers); rec.Code != http.StatusConflict {
		t.Fatalf("cancel completed job: HTTP %d", rec.Code)
	}

	retry := readSubmission(post("retry", "original to retry", http.StatusAccepted))
	relay := &ingestion.Service{
		Objects: failingReads{ObjectStore: objects}, Resources: service.Resources, Jobs: service.Jobs, RawContainer: container,
	}
	for attempt := int32(1); attempt <= 3; attempt++ {
		var event ingestion.Event
		err = service.Jobs.VisitPending(ctx, time.Now().Add(time.Minute), func(entry storage.OutboxEntry) error {
			if entry.TenantID != tenant {
				return nil
			}
			if err := json.Unmarshal(entry.Body, &event); err != nil {
				return err
			}
			if event.JobID != retry.JobID || event.Attempt != attempt {
				t.Fatalf("retry event: %+v", event)
			}
			if attempt > 1 && !entry.AvailableAt.After(time.Now()) {
				t.Fatalf("retry available without backoff: %+v", entry)
			}
			return service.Jobs.MarkSent(ctx, entry)
		})
		if err != nil {
			t.Fatal(err)
		}
		reason, err := relay.Process(ctx, event)
		if err != nil || (attempt < 3 && reason != "") || (attempt == 3 && reason != "verification_attempts_exhausted") {
			t.Fatalf("attempt %d: %q, %v", attempt, reason, err)
		}
	}
	failed, err := service.Jobs.Get(ctx, tenant, retry.JobID)
	if err != nil || failed.Status != storage.JobFailed || failed.Attempts != 3 {
		t.Fatalf("exhausted job: %+v, %v", failed, err)
	}

	// A duplicate outbox row must roll back the first action of the batch.
	job.JobID = ids.New()
	job.ETag = ""
	_, err = service.Jobs.Enqueue(ctx, job, storage.OutboxEntry{
		TenantID: tenant, ID: first.JobID + ":1", Body: []byte("{}"), AvailableAt: time.Now(),
	})
	if !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("expected duplicate outbox conflict: %v", err)
	}
	if _, err := service.Jobs.Get(ctx, tenant, job.JobID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("job survived a failed transaction: %v", err)
	}
}

type failingReads struct {
	storage.ObjectStore
}

func (failingReads) Get(context.Context, string, string) (storage.ObjectReader, error) {
	return storage.ObjectReader{}, errors.New("temporary Blob failure")
}
