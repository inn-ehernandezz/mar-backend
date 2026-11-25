package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/ingestion"
	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/ids"
	"mar-backend/internal/platform/logging"
	"mar-backend/internal/storage"
)

func (deps RouterDeps) upload(c *gin.Context) {
	r := c.Request
	r.Body = http.MaxBytesReader(c.Writer, r.Body, deps.Config.Server.MaxBodyBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) || errors.Is(err, multipart.ErrMessageTooLarge) {
			respondError(c, deps.Logger, apierr.ResourceExhausted("la petición excede el límite de subida"))
		} else {
			respondError(c, deps.Logger, apierr.InvalidArgument("se requiere multipart/form-data con un campo file"))
		}
		return
	}
	defer r.MultipartForm.RemoveAll()

	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 || len(r.MultipartForm.Value) != 0 {
		respondError(c, deps.Logger, apierr.InvalidArgument("se requiere exactamente un archivo en el campo file"))
		return
	}
	header := files[0]
	filename := filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if header.Size <= 0 || filename == "." || filename == ".." || filename == "/" ||
		len(filename) > 255 || !utf8.ValidString(filename) || strings.ContainsFunc(filename, unicode.IsControl) {
		respondError(c, deps.Logger, apierr.InvalidArgument("el archivo debe tener un nombre válido y contenido"))
		return
	}

	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" {
		key = ids.New()
	}
	if len(key) > 128 || strings.ContainsFunc(key, func(r rune) bool { return r < 33 || r > 126 }) {
		respondError(c, deps.Logger, apierr.InvalidArgument("Idempotency-Key debe tener hasta 128 caracteres ASCII visibles"))
		return
	}

	file, err := header.Open()
	if err != nil {
		respondError(c, deps.Logger, err)
		return
	}
	defer file.Close()

	var prefix [512]byte
	size, err := io.ReadFull(file, prefix[:])
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		respondError(c, deps.Logger, err)
		return
	}
	contentType := uploadType(filename, prefix[:size])
	if contentType == "" {
		respondError(c, deps.Logger, apierr.New(apierr.CodeUnsupportedMediaType, "tipo de archivo no admitido"))
		return
	}
	digest := sha256.New()
	_, _ = digest.Write(prefix[:size])
	if _, err := io.Copy(digest, file); err != nil {
		respondError(c, deps.Logger, err)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		respondError(c, deps.Logger, err)
		return
	}

	ctx := r.Context()
	c.Header("Idempotency-Key", key)
	job, err := deps.Ingestion.Submit(ctx, ingestion.Upload{
		TenantID: logging.TenantID(ctx), RequestID: logging.RequestID(ctx), Key: key,
		FileName: filename, ContentType: contentType, Size: header.Size,
		SHA256: hex.EncodeToString(digest.Sum(nil)), Body: file,
	})
	if errors.Is(err, storage.ErrNotFound) {
		respondError(c, deps.Logger, apierr.Unavailable("almacenamiento de ingesta no disponible").Wrap(err))
		return
	}
	if err != nil {
		deps.ingestionError(c, err)
		return
	}
	c.Header("Location", "/v1/jobs/"+job.JobID)
	c.JSON(http.StatusAccepted, gin.H{"resource_id": job.ResourceID, "job_id": job.JobID, "status": job.Status})
}

func uploadType(filename string, prefix []byte) string {
	kind, _, _ := mime.ParseMediaType(http.DetectContentType(prefix))
	family, _, _ := strings.Cut(kind, "/")
	switch family {
	case "text", "image", "audio", "video":
		return kind
	}
	switch kind {
	case "application/pdf", "application/ogg":
		return kind
	case "application/zip":
		switch strings.ToLower(filepath.Ext(filename)) {
		case ".docx":
			return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case ".xlsx":
			return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case ".pptx":
			return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		}
	}
	return ""
}

func (deps RouterDeps) getResource(c *gin.Context) {
	id := c.Param("id")
	if !ids.Valid(id) {
		respondError(c, deps.Logger, apierr.InvalidArgument("id debe ser un UUID"))
		return
	}
	ctx := c.Request.Context()
	resource, err := deps.Ingestion.Resources.Get(ctx, logging.TenantID(ctx), strings.ToLower(id))
	if err != nil {
		deps.ingestionError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"resource_id": resource.ResourceID, "file_name": resource.FileName,
		"content_type": resource.ContentType, "file_size": resource.FileSize,
		"content_sha256": resource.ContentHash, "status": resource.Status,
		"created_at": resource.CreatedAt, "updated_at": resource.UpdatedAt,
	})
}

func (deps RouterDeps) getJob(c *gin.Context) {
	deps.job(c, false)
}

func (deps RouterDeps) cancelJob(c *gin.Context) {
	deps.job(c, true)
}

func (deps RouterDeps) job(c *gin.Context, cancel bool) {
	id := c.Param("id")
	if !ids.Valid(id) {
		respondError(c, deps.Logger, apierr.InvalidArgument("id debe ser un UUID"))
		return
	}
	ctx := c.Request.Context()
	tenant := logging.TenantID(ctx)
	var job storage.Job
	var err error
	if cancel {
		job, err = deps.Ingestion.Cancel(ctx, tenant, strings.ToLower(id))
	} else {
		job, err = deps.Ingestion.Jobs.Get(ctx, tenant, strings.ToLower(id))
	}
	if err != nil {
		deps.ingestionError(c, err)
		return
	}
	c.JSON(http.StatusOK, jobResponse{
		JobID: job.JobID, ResourceID: job.ResourceID, Status: job.Status,
		Stage: job.Stage, Pipeline: job.Pipeline, Progress: job.Progress, Attempts: job.Attempts,
		ErrorCode: job.ErrorCode, ErrorMessage: job.ErrorMessage, CreatedAt: job.CreatedAt,
		UpdatedAt: job.UpdatedAt, StartedAt: job.StartedAt, CompletedAt: job.CompletedAt,
	})
}

type jobResponse struct {
	JobID        string            `json:"job_id"`
	ResourceID   string            `json:"resource_id"`
	Status       storage.JobStatus `json:"status"`
	Stage        string            `json:"stage,omitempty"`
	Pipeline     string            `json:"pipeline,omitempty"`
	Progress     int32             `json:"progress"`
	Attempts     int32             `json:"attempts"`
	ErrorCode    string            `json:"error_code,omitempty"`
	ErrorMessage string            `json:"error_message,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	CompletedAt  *time.Time        `json:"completed_at,omitempty"`
}

func (deps RouterDeps) ingestionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		err = apierr.NotFound("recurso o trabajo no encontrado").Wrap(err)
	case errors.Is(err, storage.ErrConflict), errors.Is(err, storage.ErrInvalidTransition):
		err = apierr.Conflict("la clave de idempotencia o el estado actual no permiten la operación").Wrap(err)
	case errors.Is(err, storage.ErrInvalidArgument):
		err = apierr.InvalidArgument("los datos de la petición no son válidos").Wrap(err)
	}
	respondError(c, deps.Logger, err)
}
