// Package health implementa sondas de vida y disponibilidad con un registro
// de comprobaciones. No depende del framework HTTP: produce un informe que la
// capa de transporte serializa.
package health

import (
	"context"
	"sync"
	"time"
)

// Estados posibles de una sonda o de una comprobación.
const (
	StatusOK       = "ok"
	StatusDegraded = "degraded"
)

// Check es una comprobación de disponibilidad con nombre.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

// CheckResult es el resultado de una comprobación individual.
type CheckResult struct {
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// Report es el informe agregado de disponibilidad.
type Report struct {
	Status string                 `json:"status"`
	Checks map[string]CheckResult `json:"checks"`
}

// Registry agrupa comprobaciones y las ejecuta con un tiempo límite común.
type Registry struct {
	timeout time.Duration

	mu     sync.RWMutex
	checks []Check
}

// NewRegistry crea un registro. timeout acota la ejecución del conjunto; si no
// es positivo se aplica un valor por defecto.
func NewRegistry(timeout time.Duration) *Registry {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Registry{timeout: timeout}
}

// Register añade comprobaciones al registro. Es seguro llamarlo antes de
// exponer el servidor; usar durante el servicio requiere sincronización.
func (r *Registry) Register(checks ...Check) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks = append(r.checks, checks...)
}

// Report ejecuta las comprobaciones registradas y agrega el resultado.
// Se ejecutan en paralelo y el conjunto se acota con el timeout del registro.
// Una comprobación que supera el límite se marca como degradada.
func (r *Registry) Report(ctx context.Context) Report {
	r.mu.RLock()
	checks := make([]Check, len(r.checks))
	copy(checks, r.checks)
	r.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	report := Report{
		Status: StatusOK,
		Checks: make(map[string]CheckResult, len(checks)),
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	for _, chk := range checks {
		wg.Add(1)
		go func(chk Check) {
			defer wg.Done()

			start := time.Now()
			err := chk.Fn(ctx)
			result := CheckResult{
				Status:    StatusOK,
				LatencyMS: time.Since(start).Milliseconds(),
			}
			if err != nil {
				result.Status = StatusDegraded
				result.Error = err.Error()
			}

			mu.Lock()
			report.Checks[chk.Name] = result
			if err != nil {
				report.Status = StatusDegraded
			}
			mu.Unlock()
		}(chk)
	}
	wg.Wait()

	return report
}
