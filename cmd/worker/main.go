// Command worker processes ingestion jobs from Azure Service Bus.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"mar-backend/internal/ingestion"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/platform/logging"
	"mar-backend/internal/platform/persistence"
	"mar-backend/internal/platform/servicebus"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mar-worker:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.Bus.Enabled {
		return fmt.Errorf("se requiere MAR_SERVICEBUS_ENABLED=true")
	}
	logger := logging.New(logging.Config{
		Level: cfg.Log.Level, Format: cfg.Log.Format, ServiceName: cfg.ServiceName + "-worker", Env: cfg.Env,
	}, os.Stdout)
	objects, tables, err := persistence.New(cfg)
	if err != nil {
		return err
	}
	bus, err := servicebus.New(cfg.Bus)
	if err != nil {
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = bus.Close(ctx)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	service := &ingestion.Service{
		Objects: objects, Resources: tables.Resources(), Jobs: tables.Jobs(), RawContainer: cfg.Blob.ContainerRaw,
	}
	relay := ingestion.Relay{Service: service, Publish: bus.Publish, Logger: logger}
	var workers sync.WaitGroup
	workers.Go(func() { relay.Run(ctx) })

	consumerErrors := make(chan error, cfg.Bus.MaxConcurrent)
	for range cfg.Bus.MaxConcurrent {
		workers.Go(func() {
			if err := bus.Consume(ctx, service, logger); err != nil {
				consumerErrors <- err
			}
		})
	}
	logger.Info("worker de ingesta iniciado", "queue", cfg.Bus.QueueIngestion, "concurrency", cfg.Bus.MaxConcurrent)
	select {
	case <-ctx.Done():
	case err = <-consumerErrors:
	}
	stop()
	workers.Wait()
	return err
}
