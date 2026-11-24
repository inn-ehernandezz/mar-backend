// Package logging centraliza el registro estructurado y la propagación de
// campos de correlación (request_id, tenant_id, job_id) por contexto.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Config describe el registro.
type Config struct {
	Level       string
	Format      string
	ServiceName string
	Env         string
}

// New construye un logger con la configuración indicada. Escribe en out.
func New(cfg Config, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}

	opts := &slog.HandlerOptions{Level: parseLevel(cfg.Level)}

	var handler slog.Handler
	if strings.EqualFold(cfg.Format, "text") {
		handler = slog.NewTextHandler(out, opts)
	} else {
		handler = slog.NewJSONHandler(out, opts)
	}

	logger := slog.New(handler)
	attrs := make([]any, 0, 2)
	if cfg.ServiceName != "" {
		attrs = append(attrs, slog.String("service", cfg.ServiceName))
	}
	if cfg.Env != "" {
		attrs = append(attrs, slog.String("env", cfg.Env))
	}
	if len(attrs) > 0 {
		logger = logger.With(attrs...)
	}
	return logger
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey int

const (
	requestIDKey ctxKey = iota
	tenantIDKey
	jobIDKey
	loggerKey
)

// WithRequestID anota el identificador de petición en el contexto.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID recupera el identificador de petición, o cadena vacía si no existe.
func RequestID(ctx context.Context) string { return stringFrom(ctx, requestIDKey) }

// WithTenantID anota el tenant en el contexto.
func WithTenantID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, tenantIDKey, id)
}

// TenantID recupera el tenant, o cadena vacía si no existe.
func TenantID(ctx context.Context) string { return stringFrom(ctx, tenantIDKey) }

// WithJobID anota el trabajo en el contexto.
func WithJobID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, jobIDKey, id)
}

// JobID recupera el trabajo, o cadena vacía si no existe.
func JobID(ctx context.Context) string { return stringFrom(ctx, jobIDKey) }

// With asocia un logger al contexto.
func With(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// From recupera el logger asociado al contexto; si no hay ninguno, devuelve el
// logger por defecto de slog.
func From(ctx context.Context) *slog.Logger {
	if logger, ok := ctx.Value(loggerKey).(*slog.Logger); ok && logger != nil {
		return logger
	}
	return slog.Default()
}

// Enrich añade a base los campos de correlación presentes en el contexto.
// Los campos ausentes se omiten para no generar ruido.
func Enrich(ctx context.Context, base *slog.Logger) *slog.Logger {
	if base == nil {
		base = slog.Default()
	}
	attrs := make([]any, 0, 3)
	if id := RequestID(ctx); id != "" {
		attrs = append(attrs, slog.String("request_id", id))
	}
	if id := TenantID(ctx); id != "" {
		attrs = append(attrs, slog.String("tenant_id", id))
	}
	if id := JobID(ctx); id != "" {
		attrs = append(attrs, slog.String("job_id", id))
	}
	if len(attrs) == 0 {
		return base
	}
	return base.With(attrs...)
}

func stringFrom(ctx context.Context, key ctxKey) string {
	value, ok := ctx.Value(key).(string)
	if !ok {
		return ""
	}
	return value
}
