// Package httpapi compone la capa de transporte HTTP: router, middleware,
// respuestas y ciclo de vida del servidor. No contiene lógica de negocio.
package httpapi

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/logging"
)

// ErrorResponse es el esquema único de error de la API. Los detalles internos
// (causas, errores de SDK) nunca se serializan.
type ErrorResponse struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// respondError traduce cualquier error al esquema de la API y lo registra.
// El registro conserva la causa completa; la respuesta, solo lo seguro.
func respondError(c *gin.Context, logger *slog.Logger, err error) {
	ctx := c.Request.Context()
	apiErr := apierr.From(err)
	if logger == nil {
		logger = logging.From(ctx)
	}

	switch {
	case apiErr.Code == apierr.CodeInternal:
		logger.ErrorContext(ctx, "error interno del servicio",
			slog.String("op", apiErr.Op),
			slog.Any("error", apiErr.Cause),
		)
	case apiErr.Cause != nil:
		logger.WarnContext(ctx, "petición rechazada",
			slog.String("code", string(apiErr.Code)),
			slog.String("op", apiErr.Op),
			slog.Any("error", apiErr.Cause),
		)
	}

	c.AbortWithStatusJSON(apierr.HTTPStatus(apiErr.Code), ErrorResponse{
		Code:      string(apiErr.Code),
		Message:   apiErr.Message,
		Details:   apiErr.Details,
		RequestID: logging.RequestID(ctx),
	})
}

// respondStatusError emite un error con un estado HTTP y un código explícitos.
func respondStatusError(c *gin.Context, status int, code apierr.Code, message string) {
	c.AbortWithStatusJSON(status, ErrorResponse{
		Code:      string(code),
		Message:   message,
		RequestID: logging.RequestID(c.Request.Context()),
	})
}
