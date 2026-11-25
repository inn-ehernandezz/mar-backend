// Package apierr define una taxonomía única de errores de la API y su mapeo
// a códigos HTTP. Los detalles internos (causas, errores de SDK) nunca se
// exponen al cliente.
package apierr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code identifica la clase de error de forma estable para los clientes.
type Code string

const (
	CodeInvalidArgument      Code = "invalid_argument"
	CodeUnauthenticated      Code = "unauthenticated"
	CodePermissionDenied     Code = "permission_denied"
	CodeNotFound             Code = "not_found"
	CodeMethodNotAllowed     Code = "method_not_allowed"
	CodeConflict             Code = "conflict"
	CodeCanceled             Code = "canceled"
	CodeDeadlineExceeded     Code = "deadline_exceeded"
	CodeUnsupportedMediaType Code = "unsupported_media_type"
	CodeResourceExhausted    Code = "resource_exhausted"
	CodeFailedPrecondition   Code = "failed_precondition"
	CodeNotImplemented       Code = "not_implemented"
	CodeUnavailable          Code = "unavailable"
	CodeInternal             Code = "internal"
)

// Error es el error de la API. Message y Details son seguros para el cliente;
// Cause se conserva únicamente para el registro de trazas.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
	Op      string
	Cause   error
}

func (e *Error) Error() string {
	if e.Op != "" {
		return fmt.Sprintf("%s: %s: %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// Is permite comparar por código con errors.Is.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) {
		return false
	}
	return e.Code == other.Code
}

// New crea un error con código y mensaje seguro para el cliente.
func New(code Code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Newf es la variante con formato.
func Newf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap adjunta la causa interna, que nunca se serializa.
func (e *Error) Wrap(cause error) *Error {
	e.Cause = cause
	return e
}

// WithOp anota la operación para facilitar el diagnóstico en los logs.
func (e *Error) WithOp(op string) *Error {
	e.Op = op
	return e
}

// WithDetail añade un detalle seguro para el cliente.
func (e *Error) WithDetail(key string, value any) *Error {
	if e.Details == nil {
		e.Details = make(map[string]any, 1)
	}
	e.Details[key] = value
	return e
}

// Constructores habituales.

func InvalidArgument(message string) *Error {
	return New(CodeInvalidArgument, message)
}

func NotFound(message string) *Error {
	return New(CodeNotFound, message)
}

func Conflict(message string) *Error {
	return New(CodeConflict, message)
}

func FailedPrecondition(message string) *Error {
	return New(CodeFailedPrecondition, message)
}

func NotImplemented(message string) *Error {
	return New(CodeNotImplemented, message)
}

func ResourceExhausted(message string) *Error {
	return New(CodeResourceExhausted, message)
}

func Unavailable(message string) *Error {
	return New(CodeUnavailable, message)
}

func Internal() *Error {
	// Mensaje deliberadamente genérico: no se filtra el detalle interno.
	return New(CodeInternal, "error interno del servicio")
}

// HTTPStatus traduce el código de error a un estado HTTP.
func HTTPStatus(code Code) int {
	switch code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed
	case CodeConflict:
		return http.StatusConflict
	case CodeCanceled:
		return http.StatusRequestTimeout
	case CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case CodeUnsupportedMediaType:
		return http.StatusUnsupportedMediaType
	case CodeResourceExhausted:
		return http.StatusRequestEntityTooLarge
	case CodeFailedPrecondition:
		return http.StatusConflict
	case CodeNotImplemented:
		return http.StatusNotImplemented
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	case CodeInternal:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// From convierte cualquier error en *Error. Los errores que ya son *Error se
// devuelven tal cual; el resto se consideran internos.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Internal().Wrap(err)
}
