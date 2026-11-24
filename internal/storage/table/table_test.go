package table

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"mar-backend/internal/storage"
)

const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newTestStore construye el adaptador contra el Azurite local y omite la prueba
// si el servicio no está disponible.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := New(Config{
		ConnectionString: os.Getenv("MAR_TABLE_CONNECTION_STRING"),
		Endpoint:         env("MAR_TABLE_ENDPOINT", "http://localhost:10002/devstoreaccount1"),
		AccountName:      env("MAR_TABLE_ACCOUNT_NAME", "devstoreaccount1"),
		AccountKey:       env("MAR_TABLE_ACCOUNT_KEY", azuriteKey),
		TableResources:   "marresources",
		TableJobs:        "marjobs",
		RequestTimeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("crear adaptador de Table: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Table Storage no disponible (%v); se omite la prueba de integración", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("preparar esquema: %v", err)
	}
	return store
}

// tenantID genera un identificador de tenant único por prueba para aislar los
// datos y permitir ejecuciones repetidas.
func tenantID(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("tenant-%s-%d", t.Name(), time.Now().UnixNano())
}

func sampleResource(tenant, id string) storage.Resource {
	return storage.Resource{
		TenantID:    tenant,
		ResourceID:  id,
		FileName:    "documento.pdf",
		ContentType: "application/pdf",
		FileSize:    2048,
		BlobPath:    tenant + "/" + id + "/original.pdf",
		ContentHash: "abc123",
		Status:      storage.ResourcePending,
	}
}

func TestTable_EnsureSchemaIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 2; i++ {
		if err := store.EnsureSchema(context.Background()); err != nil {
			t.Fatalf("EnsureSchema (intento %d): %v", i+1, err)
		}
	}
}

func TestResourceRepository_CreateGetUpdate(t *testing.T) {
	store := newTestStore(t)
	repo := store.Resources()
	ctx := context.Background()
	tenant := tenantID(t)

	created, err := repo.Create(ctx, sampleResource(tenant, "res-1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ETag == "" {
		t.Fatal("Create no devolvió ETag")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("Create no asignó marcas de tiempo")
	}
	if created.SchemaVersion != storage.EntitySchemaVersion {
		t.Errorf("SchemaVersion = %d; se esperaba %d", created.SchemaVersion, storage.EntitySchemaVersion)
	}

	// Crear de nuevo con el mismo identificador es idempotente y no modifica el
	// registro existente.
	again, err := repo.Create(ctx, sampleResource(tenant, "res-1"))
	if err != nil {
		t.Fatalf("Create idempotente: %v", err)
	}
	if again.FileName != created.FileName {
		t.Errorf("el registro existente se modificó: FileName = %q", again.FileName)
	}

	got, err := repo.Get(ctx, tenant, "res-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ResourceID != "res-1" || got.BlobPath != created.BlobPath {
		t.Errorf("Get devolvió un recurso inesperado: %+v", got)
	}

	got.Status = storage.ResourceStored
	updated, err := repo.Update(ctx, got, got.ETag)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != storage.ResourceStored {
		t.Errorf("Status = %q; se esperaba STORED", updated.Status)
	}
	if updated.ETag == got.ETag {
		t.Error("el ETag no cambió tras actualizar")
	}

	// Un ETag obsoleto debe provocar un conflicto de concurrencia.
	updated.FileName = "otro.pdf"
	if _, err := repo.Update(ctx, updated, got.ETag); !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("Update con ETag obsoleto, error = %v; se esperaba ErrConflict", err)
	}
}

func TestResourceRepository_GetNotFound(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Resources().Get(context.Background(), tenantID(t), "inexistente"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("error = %v; se esperaba ErrNotFound", err)
	}
}

func TestResourceRepository_ListPaginationAndTenantIsolation(t *testing.T) {
	store := newTestStore(t)
	repo := store.Resources()
	ctx := context.Background()
	tenantA := tenantID(t) + "-a"
	tenantB := tenantID(t) + "-b"

	for i := 0; i < 3; i++ {
		if _, err := repo.Create(ctx, sampleResource(tenantA, fmt.Sprintf("a-%d", i))); err != nil {
			t.Fatalf("Create tenant A: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := repo.Create(ctx, sampleResource(tenantB, fmt.Sprintf("b-%d", i))); err != nil {
			t.Fatalf("Create tenant B: %v", err)
		}
	}

	page, err := repo.List(ctx, storage.ResourceQuery{TenantID: tenantA, Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("primera página con %d elementos; se esperaban 2", len(page.Items))
	}
	for _, item := range page.Items {
		if item.TenantID != tenantA {
			t.Errorf("el listado devolvió un recurso de otro tenant: %q", item.TenantID)
		}
	}

	rest, err := repo.List(ctx, storage.ResourceQuery{TenantID: tenantA, Limit: 2, Continuation: page.Continuation})
	if err != nil {
		t.Fatalf("List (página 2): %v", err)
	}
	if len(rest.Items) != 1 {
		t.Errorf("segunda página con %d elementos; se esperaba 1", len(rest.Items))
	}
}

func TestResourceRepository_RequiresTenant(t *testing.T) {
	store := newTestStore(t)
	_, err := store.Resources().List(context.Background(), storage.ResourceQuery{})
	if !errors.Is(err, storage.ErrInvalidArgument) {
		t.Fatalf("error = %v; se esperaba ErrInvalidArgument", err)
	}
}

func sampleJob(tenant, jobID, resourceID string, status storage.JobStatus) storage.Job {
	return storage.Job{
		TenantID:   tenant,
		JobID:      jobID,
		ResourceID: resourceID,
		JobType:    "ingestion",
		Status:     status,
	}
}

func TestJobRepository_Transitions(t *testing.T) {
	store := newTestStore(t)
	repo := store.Jobs()
	ctx := context.Background()
	tenant := tenantID(t)

	job, err := repo.Create(ctx, sampleJob(tenant, "job-1", "res-1", storage.JobPending))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// PENDING → PROCESSING no está permitido: debe pasar por QUEUED.
	job.Status = storage.JobProcessing
	if _, err := repo.Update(ctx, job, job.ETag); !errors.Is(err, storage.ErrInvalidTransition) {
		t.Fatalf("transición PENDING→PROCESSING, error = %v; se esperaba ErrInvalidTransition", err)
	}

	// Secuencia válida: PENDING → QUEUED → PROCESSING → COMPLETED.
	for _, next := range []storage.JobStatus{storage.JobQueued, storage.JobProcessing, storage.JobCompleted} {
		current, err := repo.Get(ctx, tenant, "job-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		current.Status = next
		if _, err := repo.Update(ctx, current, current.ETag); err != nil {
			t.Fatalf("transición a %s: %v", next, err)
		}
	}

	// COMPLETED es terminal: no admite salida.
	completed, err := repo.Get(ctx, tenant, "job-1")
	if err != nil {
		t.Fatalf("Get final: %v", err)
	}
	completed.Status = storage.JobProcessing
	if _, err := repo.Update(ctx, completed, completed.ETag); !errors.Is(err, storage.ErrInvalidTransition) {
		t.Fatalf("transición desde estado terminal, error = %v; se esperaba ErrInvalidTransition", err)
	}
}

func TestJobRepository_CreateRejectsInvalidProgress(t *testing.T) {
	store := newTestStore(t)
	job := sampleJob(tenantID(t), "job-x", "res-x", storage.JobProcessing)
	job.Progress = 150
	if _, err := store.Jobs().Create(context.Background(), job); !errors.Is(err, storage.ErrInvalidArgument) {
		t.Fatalf("error = %v; se esperaba ErrInvalidArgument", err)
	}
}

func TestJobRepository_ListWithFilters(t *testing.T) {
	store := newTestStore(t)
	repo := store.Jobs()
	ctx := context.Background()
	tenant := tenantID(t)

	if _, err := repo.Create(ctx, sampleJob(tenant, "j-1", "res-1", storage.JobPending)); err != nil {
		t.Fatalf("Create j-1: %v", err)
	}
	if _, err := repo.Create(ctx, sampleJob(tenant, "j-2", "res-1", storage.JobQueued)); err != nil {
		t.Fatalf("Create j-2: %v", err)
	}
	if _, err := repo.Create(ctx, sampleJob(tenant, "j-3", "res-2", storage.JobPending)); err != nil {
		t.Fatalf("Create j-3: %v", err)
	}

	byResource, err := repo.List(ctx, storage.JobQuery{TenantID: tenant, ResourceID: "res-1"})
	if err != nil {
		t.Fatalf("List por recurso: %v", err)
	}
	if len(byResource.Items) != 2 {
		t.Errorf("List por recurso devolvió %d elementos; se esperaban 2", len(byResource.Items))
	}

	byStatus, err := repo.List(ctx, storage.JobQuery{TenantID: tenant, Status: storage.JobPending})
	if err != nil {
		t.Fatalf("List por estado: %v", err)
	}
	if len(byStatus.Items) != 2 {
		t.Errorf("List por estado devolvió %d elementos; se esperaban 2", len(byStatus.Items))
	}
}
