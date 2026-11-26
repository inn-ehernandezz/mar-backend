package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/ids"
	"mar-backend/internal/platform/logging"
)

func (deps RouterDeps) queryDocuments(c *gin.Context) {
	if deps.Queries == nil {
		respondError(c, deps.Logger, apierr.Unavailable("las consultas con fuentes no están configuradas"))
		return
	}
	var request struct {
		Query      string `json:"query"`
		ResourceID string `json:"resource_id"`
	}
	if err := decodeDocumentRequest(c, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			respondError(c, deps.Logger, apierr.ResourceExhausted("la petición de consulta excede el límite"))
		} else {
			respondError(c, deps.Logger, apierr.InvalidArgument("se requiere un objeto JSON de consulta válido"))
		}
		return
	}
	question := strings.TrimSpace(request.Query)
	if question == "" || utf8.RuneCountInString(question) > 1000 || (request.ResourceID != "" && !ids.Valid(request.ResourceID)) {
		respondError(c, deps.Logger, apierr.InvalidArgument("query admite entre 1 y 1000 caracteres y resource_id debe ser un UUID"))
		return
	}

	ctx := c.Request.Context()
	response, err := deps.Queries.Query(ctx, logging.TenantID(ctx), question, strings.ToLower(request.ResourceID))
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		respondError(c, deps.Logger, apierr.New(apierr.CodeDeadlineExceeded, "se agotó el tiempo para responder la consulta").Wrap(err))
	case errors.Is(err, context.Canceled):
		respondError(c, deps.Logger, apierr.New(apierr.CodeCanceled, "la consulta fue cancelada").Wrap(err))
	case err != nil:
		respondError(c, deps.Logger, apierr.Unavailable("la consulta con fuentes no está disponible").Wrap(err))
	default:
		c.JSON(http.StatusOK, response)
	}
}
