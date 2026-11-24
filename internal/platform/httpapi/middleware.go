package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"mar-backend/internal/platform/apierr"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/ids"
	"mar-backend/internal/platform/logging"
)

// HeaderRequestID es la cabecera de correlación, de entrada y de salida.
const HeaderRequestID = "X-Request-ID"

const maxTenantLen = 128

// RequestIDMiddleware resuelve el identificador de petición: reutiliza el de
// entrada si es un UUID válido y, en caso contrario, genera uno nuevo. El valor
// se propaga en el contexto y se devuelve en la cabecera de respuesta.
func RequestIDMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := strings.TrimSpace(c.GetHeader(HeaderRequestID))
		if !ids.Valid(requestID) {
			requestID = ids.New()
		}

		ctx := logging.WithRequestID(c.Request.Context(), requestID)
		c.Request = c.Request.WithContext(ctx)
		c.Header(HeaderRequestID, requestID)

		c.Next()
	}
}

// TenantMiddleware resuelve el tenant de la petición a partir de la cabecera
// configurada. Es un punto de extensión: cuando los requisitos corporativos de
// autenticación estén definidos, se sustituirá por la identidad autenticada.
// La ausencia del tenant es un error de autenticación (401).
func TenantMiddleware(cfg config.TenancyConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant := strings.TrimSpace(c.GetHeader(cfg.Header))
		if tenant == "" && !cfg.RequireHeader {
			tenant = strings.TrimSpace(cfg.DevTenant)
		}

		if tenant == "" {
			respondError(c, nil, apierr.New(apierr.CodeUnauthenticated,
				"falta la cabecera "+cfg.Header+" que identifica al tenant").
				WithOp("tenant"))
			return
		}
		if !validTenant(tenant) {
			respondError(c, nil, apierr.New(apierr.CodeInvalidArgument,
				"el tenant no tiene un formato válido").
				WithDetail("header", cfg.Header))
			return
		}

		ctx := logging.WithTenantID(c.Request.Context(), tenant)
		c.Request = c.Request.WithContext(ctx)

		c.Next()
	}
}

// RecoveryMiddleware convierte cualquier pánico en una respuesta 500 según el
// esquema de errores, sin filtrar la traza al cliente.
func RecoveryMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(nil, func(c *gin.Context, recovered any) {
		logger.ErrorContext(c.Request.Context(), "pánico recuperado",
			slog.Any("panic", recovered),
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
		)
		respondError(c, logger, apierr.Internal())
	})
}

// AccessLogMiddleware registra cada petición atendida con sus campos de
// correlación y una severidad acorde al estado de la respuesta.
func AccessLogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		ctx := c.Request.Context()
		status := c.Writer.Status()
		logging.Enrich(ctx, logger).LogAttrs(ctx, levelForStatus(status),
			"petición atendida",
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", status),
			slog.Int("bytes", c.Writer.Size()),
			slog.Duration("latency", time.Since(start)),
		)
	}
}

// TimeoutMiddleware acota el tiempo de cada petición. Los manejadores deben
// respetar el contexto cancelado para detener el trabajo en curso.
func TimeoutMiddleware(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		if timeout <= 0 {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
		defer cancel()

		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func levelForStatus(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

// validTenant acepta identificadores de tenant cortos y sin caracteres que
// puedan inyectarse en rutas de almacenamiento.
func validTenant(s string) bool {
	if len(s) == 0 || len(s) > maxTenantLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'a' && ch <= 'z',
			ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.':
		default:
			return false
		}
	}
	return true
}
