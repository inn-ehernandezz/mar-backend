package storage

import (
	"context"
	"fmt"
	"time"
)

// JobStatus es el estado operativo de un trabajo de procesamiento.
type JobStatus string

// Estados posibles de un trabajo.
const (
	JobPending    JobStatus = "PENDING"
	JobQueued     JobStatus = "QUEUED"
	JobProcessing JobStatus = "PROCESSING"
	JobCompleted  JobStatus = "COMPLETED"
	JobFailed     JobStatus = "FAILED"
	JobCancelled  JobStatus = "CANCELLED"
)

// jobTransitions define las transiciones permitidas desde cada estado.
// COMPLETED, FAILED y CANCELLED son estados terminales: no admiten salida.
// Los reintentos de un trabajo en curso incrementan Attempts sin cambiar de
// estado, por eso no aparecen transiciones «hacia atrás».
var jobTransitions = map[JobStatus][]JobStatus{
	JobPending:    {JobQueued, JobCancelled, JobFailed},
	JobQueued:     {JobProcessing, JobCancelled, JobFailed},
	JobProcessing: {JobCompleted, JobFailed, JobCancelled},
	JobCompleted:  {},
	JobFailed:     {},
	JobCancelled:  {},
}

// IsTerminal indica si un estado no admite más transiciones.
func (s JobStatus) IsTerminal() bool {
	switch s {
	case JobCompleted, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}

// ValidateJobTransition valida un cambio de estado. Una transición al mismo
// estado es válida (una actualización de progreso no cambia el estado).
func ValidateJobTransition(from, to JobStatus) error {
	if from == to {
		return nil
	}
	allowed, ok := jobTransitions[from]
	if !ok {
		return fmt.Errorf("%w: estado de origen desconocido %q", ErrInvalidTransition, from)
	}
	for _, candidate := range allowed {
		if candidate == to {
			return nil
		}
	}
	return fmt.Errorf("%w: %s → %s", ErrInvalidTransition, from, to)
}

// Job es un trabajo de procesamiento asociado a un recurso. Es un tipo de
// dominio: no contiene ninguna dependencia de SDK.
type Job struct {
	TenantID      string
	JobID         string
	ResourceID    string
	JobType       string
	Status        JobStatus
	Progress      int32
	Attempts      int32
	ErrorCode     string
	ErrorMessage  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	CompletedAt   *time.Time
	SchemaVersion int
	// ETag es el token de concurrencia optimista. No forma parte de la lógica
	// de negocio: lo asigna el almacenamiento y se exige en las actualizaciones
	// condicionales.
	ETag string
}

// Validate comprueba los campos obligatorios de un trabajo.
func (j Job) Validate() error {
	if j.TenantID == "" {
		return fmt.Errorf("%w: tenant_id obligatorio en el trabajo", ErrInvalidArgument)
	}
	if j.JobID == "" {
		return fmt.Errorf("%w: job_id obligatorio", ErrInvalidArgument)
	}
	if j.ResourceID == "" {
		return fmt.Errorf("%w: resource_id obligatorio", ErrInvalidArgument)
	}
	if j.JobType == "" {
		return fmt.Errorf("%w: job_type obligatorio", ErrInvalidArgument)
	}
	if _, ok := jobTransitions[j.Status]; !ok {
		return fmt.Errorf("%w: estado de trabajo desconocido %q", ErrInvalidArgument, j.Status)
	}
	if j.Progress < 0 || j.Progress > 100 {
		return fmt.Errorf("%w: progress fuera de rango [0,100]: %d", ErrInvalidArgument, j.Progress)
	}
	return nil
}

// JobQuery describe un listado paginado de trabajos de un tenant.
type JobQuery struct {
	// TenantID es obligatorio: el listado siempre se aísla por tenant.
	TenantID string
	// ResourceID, si se indica, filtra los trabajos de un recurso concreto.
	ResourceID string
	// Status, si se indica, filtra por estado.
	Status JobStatus
	// Limit acota el número de trabajos por página. Si es 0 se aplica un valor
	// por defecto del adaptador.
	Limit int32
	// Continuation es el token devuelto por una llamada anterior; vacío para
	// la primera página.
	Continuation string
}

// JobPage es una página de trabajos.
type JobPage struct {
	Items []Job
	// Continuation es el token para solicitar la siguiente página. Vacío si no
	// hay más resultados.
	Continuation string
}

// JobRepository es el puerto de persistencia de trabajos. Table Storage es la
// fuente de verdad de su estado operativo.
type JobRepository interface {
	// Create persiste un trabajo nuevo. Es idempotente: si ya existe un trabajo
	// con el mismo identificador, devuelve el existente sin modificarlo.
	Create(ctx context.Context, j Job) (Job, error)
	// Get recupera un trabajo por identificador. El tenant es obligatorio para
	// garantizar el aislamiento incluso en accesos directos.
	Get(ctx context.Context, tenantID, jobID string) (Job, error)
	// Update actualiza un trabajo aplicando concurrencia optimista mediante
	// ETag y validando la transición de estado. Un ETag vacío fuerza la
	// escritura incondicional.
	Update(ctx context.Context, j Job, ifMatch string) (Job, error)
	// List enumera los trabajos de un tenant de forma paginada.
	List(ctx context.Context, q JobQuery) (JobPage, error)
}
