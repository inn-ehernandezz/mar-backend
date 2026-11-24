package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"mar-backend/internal/platform/config"
)

// Server encapsula el servidor HTTP y su ciclo de vida.
type Server struct {
	http   *http.Server
	logger *slog.Logger
}

// NewServer construye el servidor con los timeouts de la configuración.
func NewServer(cfg config.Config, handler http.Handler, logger *slog.Logger) *Server {
	return &Server{
		http: &http.Server{
			Addr:              cfg.Server.Addr,
			Handler:           handler,
			ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
			ReadTimeout:       cfg.Server.ReadTimeout,
			WriteTimeout:      cfg.Server.WriteTimeout,
			IdleTimeout:       cfg.Server.IdleTimeout,
		},
		logger: logger,
	}
}

// Addr devuelve la dirección de escucha configurada.
func (s *Server) Addr() string { return s.http.Addr }

// ListenAndServe arranca el servidor. Devuelve nil cuando el servidor se cierra
// de forma ordenada.
func (s *Server) ListenAndServe() error {
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown realiza un cierre ordenado con el tiempo límite indicado.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
