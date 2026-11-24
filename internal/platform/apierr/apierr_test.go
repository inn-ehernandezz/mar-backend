package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

func TestHTTPStatus(t *testing.T) {
	cases := []struct {
		code Code
		want int
	}{
		{CodeInvalidArgument, http.StatusBadRequest},
		{CodeUnauthenticated, http.StatusUnauthorized},
		{CodePermissionDenied, http.StatusForbidden},
		{CodeNotFound, http.StatusNotFound},
		{CodeConflict, http.StatusConflict},
		{CodeCanceled, http.StatusRequestTimeout},
		{CodeDeadlineExceeded, http.StatusGatewayTimeout},
		{CodeResourceExhausted, http.StatusRequestEntityTooLarge},
		{CodeFailedPrecondition, http.StatusConflict},
		{CodeNotImplemented, http.StatusNotImplemented},
		{CodeUnavailable, http.StatusServiceUnavailable},
		{CodeInternal, http.StatusInternalServerError},
		{Code("desconocido"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := HTTPStatus(tc.code); got != tc.want {
			t.Errorf("HTTPStatus(%q) = %d, se esperaba %d", tc.code, got, tc.want)
		}
	}
}

func TestFromWrapsUnknownError(t *testing.T) {
	base := errors.New("error de SDK con datos sensibles")
	got := From(base)

	if got.Code != CodeInternal {
		t.Fatalf("código = %q, se esperaba %q", got.Code, CodeInternal)
	}
	if !errors.Is(got, base) {
		t.Fatal("la causa debe conservarse para los logs")
	}
	if got.Message == base.Error() {
		t.Fatal("el mensaje público no debe contener el detalle interno")
	}
}

func TestFromKeepsAPIError(t *testing.T) {
	original := NotFound("el recurso no existe")
	got := From(original)
	if got != original {
		t.Fatal("un *Error debe devolverse sin envolver")
	}
}

func TestIsMatchesByCode(t *testing.T) {
	err := fmt.Errorf("contexto: %w", NotFound("no existe"))
	if !errors.Is(err, &Error{Code: CodeNotFound}) {
		t.Fatal("errors.Is debe comparar por código")
	}
	if errors.Is(err, &Error{Code: CodeInternal}) {
		t.Fatal("no debe coincidir con otro código")
	}
}

func TestWrapAndOpPreserveDiagnostics(t *testing.T) {
	cause := errors.New("causa")
	err := Unavailable("servicio no disponible").Wrap(cause).WithOp("qdrant.search")

	if !errors.Is(err, cause) {
		t.Fatal("Wrap debe conservar la causa")
	}
	if err.Op != "qdrant.search" {
		t.Fatalf("Op = %q", err.Op)
	}
	if err.Error() == "" || err.Error() == cause.Error() {
		t.Fatal("Error() debe describir la operación y no solo la causa")
	}
}
