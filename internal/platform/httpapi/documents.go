package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/ids"
	"mar-backend/internal/platform/logging"
)

func (deps RouterDeps) indexResource(c *gin.Context) {
	if deps.Ingestion == nil || !deps.Ingestion.DocumentsEnabled {
		respondError(c, deps.Logger, apierr.Unavailable("el procesamiento documental está deshabilitado"))
		return
	}
	id := c.Param("id")
	if !ids.Valid(id) {
		respondError(c, deps.Logger, apierr.InvalidArgument("id debe ser un UUID"))
		return
	}
	key, valid := requestKey(c, deps)
	if !valid {
		return
	}
	ctx := c.Request.Context()
	c.Header("Idempotency-Key", key)
	job, err := deps.Ingestion.Index(ctx, logging.TenantID(ctx), strings.ToLower(id), key, logging.RequestID(ctx))
	if err != nil {
		deps.ingestionError(c, err)
		return
	}
	c.Header("Location", "/v1/jobs/"+job.JobID)
	c.JSON(http.StatusAccepted, gin.H{"resource_id": job.ResourceID, "job_id": job.JobID, "status": job.Status})
}

func (deps RouterDeps) searchDocuments(c *gin.Context) {
	if deps.Ingestion == nil || deps.Ingestion.Documents == nil {
		respondError(c, deps.Logger, apierr.Unavailable("la búsqueda documental está deshabilitada"))
		return
	}
	var request struct {
		Query      string `json:"query"`
		Limit      *int   `json:"limit"`
		ResourceID string `json:"resource_id"`
	}
	err := decodeDocumentRequest(c, &request)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			respondError(c, deps.Logger, apierr.ResourceExhausted("la petición de búsqueda excede el límite"))
		} else {
			respondError(c, deps.Logger, apierr.InvalidArgument("se requiere un objeto JSON de búsqueda válido"))
		}
		return
	}
	query := strings.TrimSpace(request.Query)
	limit := 10
	if request.Limit != nil {
		limit = *request.Limit
	}
	if query == "" || utf8.RuneCountInString(query) > 1000 || limit < 1 || limit > 50 ||
		(request.ResourceID != "" && !ids.Valid(request.ResourceID)) {
		respondError(c, deps.Logger, apierr.InvalidArgument("query admite entre 1 y 1000 caracteres, limit entre 1 y 50 y resource_id debe ser un UUID"))
		return
	}
	ctx := c.Request.Context()
	results, err := deps.Ingestion.Documents.Search(ctx, logging.TenantID(ctx), query, strings.ToLower(request.ResourceID), limit)
	if err != nil {
		respondError(c, deps.Logger, apierr.Unavailable("la búsqueda documental no está disponible").Wrap(err))
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

func decodeDocumentRequest(c *gin.Context, target any) error {
	body := http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("varios valores JSON")
	}
	return err
}
