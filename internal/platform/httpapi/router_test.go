package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/health"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestRouter(t *testing.T, registry *health.Registry) *gin.Engine {
	t.Helper()

	cfg := config.Config{
		Env:         config.EnvTest,
		ServiceName: "mar",
		Tenancy: config.TenancyConfig{
			Header:        "X-Tenant-ID",
			RequireHeader: true,
		},
	}
	cfg.Server.RequestTimeout = time.Second

	return NewRouter(RouterDeps{Config: cfg, Logger: discardLogger(), Health: registry})
}

func do(t *testing.T, router *gin.Engine, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()
	var body ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("cuerpo no es un ErrorResponse válido: %v (%s)", err, rec.Body.String())
	}
	return body
}

func TestLiveIsPublic(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodGet, "/health/live", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("estado = %d, se esperaba 200", rec.Code)
	}
}

func TestReadyReflectsRegistry(t *testing.T) {
	registry := health.NewRegistry(time.Second)
	router := newTestRouter(t, registry)

	if rec := do(t, router, http.MethodGet, "/health/ready", nil); rec.Code != http.StatusOK {
		t.Fatalf("sin checks el estado debe ser 200, se obtuvo %d", rec.Code)
	}

	registry.Register(health.Check{Name: "qdrant", Fn: func(context.Context) error {
		return errors.New("no disponible")
	}})

	rec := do(t, router, http.MethodGet, "/health/ready", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("con un check fallando el estado debe ser 503, se obtuvo %d", rec.Code)
	}
}

func TestV1RequiresTenant(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodPost, "/v1/resources", nil)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("estado = %d, se esperaba 401", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != "unauthenticated" {
		t.Errorf("código = %q, se esperaba unauthenticated", got)
	}
}

func TestQueriesWithoutModels(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodPost, "/v1/queries", map[string]string{"X-Tenant-ID": "acme"})

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("estado = %d, se esperaba 503", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != "unavailable" {
		t.Errorf("código = %q, se esperaba unavailable", got)
	}
}

func TestUnknownRouteReturnsNotFound(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodGet, "/no-existe", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("estado = %d, se esperaba 404", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != "not_found" {
		t.Errorf("código = %q, se esperaba not_found", got)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodGet, "/v1/search", map[string]string{"X-Tenant-ID": "acme"})

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("estado = %d, se esperaba 405", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != "method_not_allowed" {
		t.Errorf("código = %q, se esperaba method_not_allowed", got)
	}
}

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodGet, "/health/live", nil)
	generated := rec.Header().Get(HeaderRequestID)
	if generated == "" {
		t.Fatal("se esperaba un X-Request-ID en la respuesta")
	}

	rec = do(t, router, http.MethodGet, "/health/live", map[string]string{HeaderRequestID: generated})
	if got := rec.Header().Get(HeaderRequestID); got != generated {
		t.Errorf("X-Request-ID = %q, se esperaba conservar %q", got, generated)
	}
}

func TestRequestIDIgnoresInvalidValue(t *testing.T) {
	router := newTestRouter(t, health.NewRegistry(time.Second))

	rec := do(t, router, http.MethodGet, "/health/live", map[string]string{HeaderRequestID: "no-es-uuid"})

	if got := rec.Header().Get(HeaderRequestID); got == "no-es-uuid" || got == "" {
		t.Errorf("X-Request-ID = %q; se esperaba un identificador nuevo", got)
	}
}
