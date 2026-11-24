// Comando api: servidor HTTP de MAR. Compone las dependencias de la fase
// actual y gestiona el ciclo de vida con cierre ordenado.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/health"
	"mar-backend/internal/platform/httpapi"
	"mar-backend/internal/platform/logging"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mar-api: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuración inválida: %w", err)
	}

	logger := logging.New(logging.Config{
		Level:       cfg.Log.Level,
		Format:      cfg.Log.Format,
		ServiceName: cfg.ServiceName,
		Env:         cfg.Env,
	}, os.Stdout)

	logger.Info("iniciando servicio", slog.Any("config", cfg.SafeSummary()))

	// Registro de disponibilidad con las dependencias obligatorias de la Fase 2
	// (Qdrant, Blob y Table). Si alguna no responde, /health/ready devuelve 503.
	checks := health.NewRegistry(5 * time.Second)

	deps, err := buildDependencies(cfg, checks)
	if err != nil {
		return err
	}
	defer func() {
		if err := deps.Qdrant.Close(); err != nil {
			logger.Error("cierre del cliente vectorial", slog.String("error", err.Error()))
		}
	}()

	// Inicialización idempotente de contenedores, tablas y colecciones. No es
	// fatal: si un servicio no está disponible, el arranque continúa y la sonda
	// de disponibilidad lo refleja.
	initCtx, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
	ensureStorage(initCtx, cfg, deps, logger)
	cancelInit()

	router := httpapi.NewRouter(httpapi.RouterDeps{
		Config: cfg,
		Logger: logger,
		Health: checks,
	})
	server := httpapi.NewServer(cfg, router, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("servidor HTTP escuchando", slog.String("addr", server.Addr()))
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("servidor HTTP: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info("señal de parada recibida; iniciando cierre ordenado")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("cierre ordenado: %w", err)
	}
	// Se recoge el resultado del servidor ya cerrado para no dejar la goroutine
	// huérfana.
	if err := <-serveErr; err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("servidor HTTP: %w", err)
	}

	logger.Info("servicio detenido")
	return nil
}
