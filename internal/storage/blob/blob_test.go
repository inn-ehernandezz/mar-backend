package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"mar-backend/internal/storage"
)

// azuriteKey es la clave pública del emulador Azurite documentada por
// Microsoft. No es un secreto y solo se usa en las pruebas locales.
const azuriteKey = "Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=="

const testContainer = "mar-it-blob"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newTestStore construye el adaptador contra el Azurite local y omite la prueba
// si el servicio no está disponible, para no fallar sin infraestructura.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	cfg := Config{
		ConnectionString: os.Getenv("MAR_BLOB_CONNECTION_STRING"),
		Endpoint:         env("MAR_BLOB_ENDPOINT", "http://localhost:10000/devstoreaccount1"),
		AccountName:      env("MAR_BLOB_ACCOUNT_NAME", "devstoreaccount1"),
		AccountKey:       env("MAR_BLOB_ACCOUNT_KEY", azuriteKey),
		RequestTimeout:   10 * time.Second,
	}
	store, err := New(cfg)
	if err != nil {
		t.Fatalf("crear adaptador de Blob: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Skipf("Blob Storage no disponible (%v); se omite la prueba de integración", err)
	}
	if err := store.EnsureContainer(ctx, testContainer); err != nil {
		t.Fatalf("crear contenedor de prueba: %v", err)
	}
	return store
}

// objectName genera un nombre único por ejecución para que la prueba sea
// idempotente y no interfiera con objetos de ejecuciones anteriores.
func objectName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("tenant-a/%s/%s", testContainer, t.Name())
}

func putContent(t *testing.T, store *Store, name, content, contentType string) storage.ObjectInfo {
	t.Helper()
	sum := sha256.Sum256([]byte(content))
	info, err := store.Put(context.Background(), storage.PutObjectInput{
		Container:   testContainer,
		Name:        name,
		Body:        strings.NewReader(content),
		Size:        int64(len(content)),
		ContentType: contentType,
		ContentHash: hex.EncodeToString(sum[:]),
		Metadata:    map[string]string{"tenant_id": "tenant-a", "resource_id": "res-1"},
	})
	if err != nil {
		t.Fatalf("subir objeto %q: %v", name, err)
	}
	return info
}

func TestObjectStore_PutGetStatDelete(t *testing.T) {
	store := newTestStore(t)
	name := objectName(t)
	t.Cleanup(func() { _ = store.Delete(context.Background(), testContainer, name) })

	const content = "contenido sintético de prueba"
	info := putContent(t, store, name, content, "text/plain")

	if info.Size != int64(len(content)) {
		t.Errorf("tamaño devuelto = %d; se esperaba %d", info.Size, len(content))
	}
	if info.ContentType != "text/plain" {
		t.Errorf("content-type = %q; se esperaba text/plain", info.ContentType)
	}
	if info.Metadata[MetadataContentHash] == "" {
		t.Errorf("metadatos sin %s: %v", MetadataContentHash, info.Metadata)
	}

	stat, err := store.Stat(context.Background(), testContainer, name)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if stat.Size != int64(len(content)) {
		t.Errorf("Stat.Size = %d; se esperaba %d", stat.Size, len(content))
	}

	exists, err := store.Exists(context.Background(), testContainer, name)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("Exists = false tras subir el objeto")
	}

	reader, err := store.Get(context.Background(), testContainer, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(reader.Body)
	if err != nil {
		t.Fatalf("leer cuerpo: %v", err)
	}
	_ = reader.Body.Close()
	if string(got) != content {
		t.Errorf("contenido leído = %q; se esperaba %q", got, content)
	}

	if err := store.Delete(context.Background(), testContainer, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Borrar de nuevo debe ser idempotente (sin error).
	if err := store.Delete(context.Background(), testContainer, name); err != nil {
		t.Errorf("Delete idempotente devolvió error: %v", err)
	}

	exists, err = store.Exists(context.Background(), testContainer, name)
	if err != nil {
		t.Fatalf("Exists tras borrar: %v", err)
	}
	if exists {
		t.Error("Exists = true tras borrar el objeto")
	}
}

func TestObjectStore_EnsureContainerIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	for i := 0; i < 2; i++ {
		if err := store.EnsureContainer(context.Background(), testContainer); err != nil {
			t.Fatalf("EnsureContainer (intento %d): %v", i+1, err)
		}
	}
}

func TestObjectStore_HashMismatchDiscardsObject(t *testing.T) {
	store := newTestStore(t)
	name := fmt.Sprintf("tenant-a/%s/hash-mismatch", testContainer)
	t.Cleanup(func() { _ = store.Delete(context.Background(), testContainer, name) })

	_, err := store.Put(context.Background(), storage.PutObjectInput{
		Container:   testContainer,
		Name:        name,
		Body:        strings.NewReader("contenido real"),
		Size:        int64(len("contenido real")),
		ContentHash: strings.Repeat("0", 64),
	})
	if !errors.Is(err, storage.ErrInvalidArgument) {
		t.Fatalf("error = %v; se esperaba ErrInvalidArgument", err)
	}

	exists, err := store.Exists(context.Background(), testContainer, name)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("el objeto con hash incorrecto no se descartó")
	}
}

func TestObjectStore_IfNoneMatchRejectsOverwrite(t *testing.T) {
	store := newTestStore(t)
	name := fmt.Sprintf("tenant-a/%s/if-none-match", testContainer)
	t.Cleanup(func() { _ = store.Delete(context.Background(), testContainer, name) })

	putContent(t, store, name, "primera versión", "text/plain")

	_, err := store.Put(context.Background(), storage.PutObjectInput{
		Container:   testContainer,
		Name:        name,
		Body:        strings.NewReader("segunda versión"),
		Size:        int64(len("segunda versión")),
		IfNoneMatch: true,
	})
	if !errors.Is(err, storage.ErrPreconditionFailed) {
		t.Fatalf("error = %v; se esperaba ErrPreconditionFailed", err)
	}
}

func TestObjectStore_ListPagination(t *testing.T) {
	store := newTestStore(t)
	prefix := fmt.Sprintf("tenant-a/%s/list/%d/", testContainer, time.Now().UnixNano())
	names := []string{prefix + "a", prefix + "b", prefix + "c"}
	for _, n := range names {
		putContent(t, store, n, "x", "application/octet-stream")
	}
	t.Cleanup(func() {
		for _, n := range names {
			_ = store.Delete(context.Background(), testContainer, n)
		}
	})

	page, err := store.List(context.Background(), storage.ListOptions{
		Container:  testContainer,
		Prefix:     prefix,
		MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("primera página con %d elementos; se esperaban 2", len(page.Items))
	}
	if page.Continuation == "" {
		t.Fatal("se esperaba un token de continuación con más resultados pendientes")
	}

	next, err := store.List(context.Background(), storage.ListOptions{
		Container:    testContainer,
		Prefix:       prefix,
		MaxResults:   2,
		Continuation: page.Continuation,
	})
	if err != nil {
		t.Fatalf("List (página 2): %v", err)
	}
	if len(next.Items) != 1 {
		t.Errorf("segunda página con %d elementos; se esperaba 1", len(next.Items))
	}
}

func TestObjectStore_RejectsInvalidPaths(t *testing.T) {
	store := newTestStore(t)

	cases := []storage.PutObjectInput{
		{Container: "", Name: "a", Body: bytes.NewReader([]byte("x"))},
		{Container: testContainer, Name: "", Body: bytes.NewReader([]byte("x"))},
		{Container: testContainer, Name: "../escape", Body: bytes.NewReader([]byte("x"))},
		{Container: testContainer, Name: "/absoluta", Body: bytes.NewReader([]byte("x"))},
	}
	for _, in := range cases {
		if _, err := store.Put(context.Background(), in); !errors.Is(err, storage.ErrInvalidArgument) {
			t.Errorf("Put(%q/%q) error = %v; se esperaba ErrInvalidArgument", in.Container, in.Name, err)
		}
	}
}
