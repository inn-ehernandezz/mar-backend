package health

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestReportOK(t *testing.T) {
	registry := NewRegistry(time.Second)
	registry.Register(
		Check{Name: "a", Fn: func(context.Context) error { return nil }},
		Check{Name: "b", Fn: func(context.Context) error { return nil }},
	)

	report := registry.Report(context.Background())

	if report.Status != StatusOK {
		t.Fatalf("estado = %q, se esperaba %q", report.Status, StatusOK)
	}
	if len(report.Checks) != 2 {
		t.Fatalf("se esperaban 2 comprobaciones, hay %d", len(report.Checks))
	}
	for name, result := range report.Checks {
		if result.Status != StatusOK {
			t.Errorf("check %q: estado = %q, se esperaba %q", name, result.Status, StatusOK)
		}
	}
}

func TestReportDegradedKeepsRunningChecks(t *testing.T) {
	registry := NewRegistry(time.Second)
	registry.Register(
		Check{Name: "ok", Fn: func(context.Context) error { return nil }},
		Check{Name: "fail", Fn: func(context.Context) error { return errors.New("sin conexión") }},
	)

	report := registry.Report(context.Background())

	if report.Status != StatusDegraded {
		t.Fatalf("estado = %q, se esperaba %q", report.Status, StatusDegraded)
	}
	if got := report.Checks["fail"].Error; got != "sin conexión" {
		t.Errorf("error del check degradado = %q", got)
	}
	if report.Checks["ok"].Status != StatusOK {
		t.Errorf("el check correcto debe seguir marcándose como %q", StatusOK)
	}
}

func TestReportAppliesTimeout(t *testing.T) {
	registry := NewRegistry(10 * time.Millisecond)
	registry.Register(Check{Name: "lento", Fn: func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}})

	report := registry.Report(context.Background())

	if report.Status != StatusDegraded {
		t.Fatalf("estado = %q, se esperaba %q", report.Status, StatusDegraded)
	}
	if report.Checks["lento"].Error == "" {
		t.Error("se esperaba un error por superar el tiempo límite")
	}
}

func TestNewRegistryDefaultTimeout(t *testing.T) {
	if got := NewRegistry(0).timeout; got != 5*time.Second {
		t.Fatalf("timeout por defecto = %v, se esperaba 5s", got)
	}
}
