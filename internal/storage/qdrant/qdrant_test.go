package qdrant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"mar-backend/internal/storage"
)

// newTestStore construye el adaptador contra el Qdrant local y omite la prueba
// si el servicio no está disponible.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := New(Config{
		URL:            env("MAR_QDRANT_URL", "http://localhost:6334"),
		RequestTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("crear adaptador de Qdrant: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Qdrant no disponible (%v); se omite la prueba de integración", err)
	}
	return store
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// testCollection crea una colección con nombre único y la elimina al terminar.
func testCollection(t *testing.T, store *Store, size uint64) string {
	t.Helper()

	name := fmt.Sprintf("mar_it_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = store.client.DeleteCollection(context.Background(), name)
	})

	if _, err := store.EnsureCollection(context.Background(), storage.CollectionSpec{
		Name: name,
		Vectors: map[string]storage.VectorParams{
			"dense": {Size: size, Distance: storage.DistanceCosine},
		},
		Indexes: []storage.PayloadIndex{
			{Field: storage.FieldTenantID, Type: storage.IndexKeyword},
			{Field: storage.FieldResourceID, Type: storage.IndexKeyword},
			{Field: storage.FieldContentType, Type: storage.IndexKeyword},
		},
	}); err != nil {
		t.Fatalf("crear colección de prueba: %v", err)
	}
	return name
}

func vec(seed float32) []float32 {
	return []float32{seed, seed + 1, seed + 2, seed + 3, seed + 4, seed + 5, seed + 6, seed + 7}
}

func TestQdrant_EnsureCollectionIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	name := testCollection(t, store, 8)

	// Volver a asegurar la misma colección no falla (idempotente).
	info, err := store.EnsureCollection(context.Background(), storage.CollectionSpec{
		Name: name,
		Vectors: map[string]storage.VectorParams{
			"dense": {Size: 8, Distance: storage.DistanceCosine},
		},
	})
	if err != nil {
		t.Fatalf("EnsureCollection idempotente: %v", err)
	}
	if info.VectorSizes["dense"] != 8 {
		t.Errorf("dimensión real = %d; se esperaba 8", info.VectorSizes["dense"])
	}
	if info.VectorDistances["dense"] != storage.DistanceCosine {
		t.Errorf("distancia real = %v; se esperaba coseno", info.VectorDistances["dense"])
	}
}

func TestQdrant_IncompatibleCollectionIsReportedNotDeleted(t *testing.T) {
	store := newTestStore(t)
	name := testCollection(t, store, 8)
	ctx := context.Background()

	_, err := store.EnsureCollection(ctx, storage.CollectionSpec{
		Name: name,
		Vectors: map[string]storage.VectorParams{
			"dense": {Size: 16, Distance: storage.DistanceCosine},
		},
	})
	if !errors.Is(err, storage.ErrIncompatibleCollection) {
		t.Fatalf("error = %v; se esperaba ErrIncompatibleCollection", err)
	}

	exists, err := store.client.CollectionExists(ctx, name)
	if err != nil {
		t.Fatalf("CollectionExists: %v", err)
	}
	if !exists {
		t.Error("la colección incompatible se eliminó; no debe modificarse ni eliminarse")
	}
}

func TestQdrant_UpsertSearchFilterAndDelete(t *testing.T) {
	store := newTestStore(t)
	name := testCollection(t, store, 8)
	ctx := context.Background()

	points := []storage.VectorPoint{
		{ID: "11111111-1111-1111-1111-111111111111", Vectors: map[string][]float32{"dense": vec(1)}, Payload: map[string]any{
			storage.FieldTenantID: "tenant-a", storage.FieldResourceID: "res-1", storage.FieldContentType: "text/plain",
		}},
		{ID: "22222222-2222-2222-2222-222222222222", Vectors: map[string][]float32{"dense": vec(2)}, Payload: map[string]any{
			storage.FieldTenantID: "tenant-a", storage.FieldResourceID: "res-2", storage.FieldContentType: "text/plain",
		}},
		{ID: "33333333-3333-3333-3333-333333333333", Vectors: map[string][]float32{"dense": vec(3)}, Payload: map[string]any{
			storage.FieldTenantID: "tenant-b", storage.FieldResourceID: "res-3", storage.FieldContentType: "text/plain",
		}},
	}
	if err := store.Upsert(ctx, name, points); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Reinsertar los mismos puntos con identificadores deterministas no
	// duplica el contenido.
	if err := store.Upsert(ctx, name, points); err != nil {
		t.Fatalf("Upsert idempotente: %v", err)
	}
	total, err := store.Count(ctx, name, nil)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if total != 3 {
		t.Fatalf("total de puntos = %d; se esperaban 3 (upsert no idempotente)", total)
	}

	// Búsqueda aislada por tenant: solo devuelve puntos de tenant-a.
	results, err := store.Search(ctx, name, storage.SearchQuery{
		VectorName:  "dense",
		Vector:      vec(1),
		Limit:       10,
		Filter:      storage.TenantFilter("tenant-a"),
		WithPayload: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("Search devolvió %d resultados; se esperaban 2", len(results))
	}
	for _, r := range results {
		if got := r.Payload[storage.FieldTenantID]; got != "tenant-a" {
			t.Errorf("la búsqueda devolvió un punto de otro tenant: %v", got)
		}
	}

	// Scroll devuelve todos los puntos del tenant.
	scrolled, err := store.Scroll(ctx, name, storage.ScrollQuery{
		Limit:       10,
		Filter:      storage.TenantFilter("tenant-a"),
		WithPayload: true,
	})
	if err != nil {
		t.Fatalf("Scroll: %v", err)
	}
	if len(scrolled.Points) != 2 {
		t.Errorf("Scroll devolvió %d puntos; se esperaban 2", len(scrolled.Points))
	}

	// Borrado por filtro: se elimina únicamente el tenant-a.
	if _, err := store.DeleteByFilter(ctx, name, *storage.TenantFilter("tenant-a")); err != nil {
		t.Fatalf("DeleteByFilter: %v", err)
	}
	remaining, err := store.Count(ctx, name, nil)
	if err != nil {
		t.Fatalf("Count tras borrado: %v", err)
	}
	if remaining != 1 {
		t.Errorf("puntos restantes = %d; se esperaba 1", remaining)
	}
}

func TestQdrant_RejectsInvalidPointID(t *testing.T) {
	store := newTestStore(t)
	name := testCollection(t, store, 8)

	err := store.Upsert(context.Background(), name, []storage.VectorPoint{
		{ID: "no-es-un-uuid", Vectors: map[string][]float32{"dense": vec(1)}},
	})
	if !errors.Is(err, storage.ErrInvalidArgument) {
		t.Fatalf("error = %v; se esperaba ErrInvalidArgument", err)
	}
}

func TestQdrant_RejectsEmptyCollectionName(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Count(context.Background(), "", nil); !errors.Is(err, storage.ErrInvalidArgument) {
		t.Fatalf("error = %v; se esperaba ErrInvalidArgument", err)
	}
}
