package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/ingestion"
	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/health"
)

// RouterDeps agrupa las dependencias necesarias para construir el enrutador.
type RouterDeps struct {
	Config    config.Config
	Logger    *slog.Logger
	Health    *health.Registry
	Ingestion *ingestion.Service
}

// NewRouter construye el enrutador HTTP con los middleware transversales y las
// rutas de la API. Las rutas cuyo caso de uso aún no existe responden de forma
// explícita con 501 Not Implemented: nunca se devuelven resultados ficticios.
func NewRouter(deps RouterDeps) *gin.Engine {
	if deps.Config.Env != config.EnvDevelopment {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.RedirectTrailingSlash = false
	router.HandleMethodNotAllowed = true

	router.Use(
		RequestIDMiddleware(),
		RecoveryMiddleware(deps.Logger),
		AccessLogMiddleware(deps.Logger),
		TimeoutMiddleware(deps.Config.Server.RequestTimeout),
	)

	router.GET("/health/live", liveHandler())
	router.GET("/health/ready", readyHandler(deps.Health))

	// El grupo /v1 exige identificar el tenant: es un requisito transversal de
	// todas las operaciones de negocio.
	v1 := router.Group("/v1", TenantMiddleware(deps.Config.Tenancy))
	{
		v1.POST("/resources", deps.upload)
		v1.GET("/resources/:id", deps.getResource)
		v1.POST("/resources/:id/index", deps.indexResource)
		v1.GET("/jobs/:id", deps.getJob)
		v1.POST("/jobs/:id/cancel", deps.cancelJob)
		v1.POST("/search", deps.searchDocuments)
		v1.POST("/queries", notImplementedHandler(deps.Logger, "POST /v1/queries"))
	}

	router.NoRoute(noRouteHandler(deps.Logger))
	router.NoMethod(noMethodHandler())

	return router
}

func liveHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": health.StatusOK})
	}
}

func readyHandler(registry *health.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		if registry == nil {
			c.JSON(http.StatusOK, health.Report{
				Status: health.StatusOK,
				Checks: map[string]health.CheckResult{},
			})
			return
		}

		report := registry.Report(c.Request.Context())
		status := http.StatusOK
		if report.Status != health.StatusOK {
			status = http.StatusServiceUnavailable
		}
		c.JSON(status, report)
	}
}

func notImplementedHandler(logger *slog.Logger, endpoint string) gin.HandlerFunc {
	return func(c *gin.Context) {
		respondError(c, logger, apierr.
			NotImplemented(endpoint+" aún no está implementado").
			WithOp(endpoint))
	}
}

func noRouteHandler(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		respondError(c, logger, apierr.NotFound("recurso no encontrado").
			WithOp("router"))
	}
}

func noMethodHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		respondStatusError(c, http.StatusMethodNotAllowed, apierr.CodeMethodNotAllowed,
			"método no permitido para esta ruta")
	}
}
