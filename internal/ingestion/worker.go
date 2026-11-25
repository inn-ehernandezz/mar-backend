package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"strings"
	"time"

	"mar-backend/internal/platform/ids"
	"mar-backend/internal/storage"
)

const (
	maxAttempts    = 3
	processTimeout = 30 * time.Second
	leaseDuration  = 45 * time.Second
)

var ErrBusy = errors.New("ingestion attempt is already running")

// Process handles one delivery. The returned reason sends the message to the
// DLQ. An error leaves the delivery available for redelivery.
func (s *Service) Process(ctx context.Context, event Event) (reason string, err error) {
	if !event.Valid() {
		return "invalid_ingestion_event", nil
	}
	job, err := s.Jobs.Get(ctx, event.TenantID, event.JobID)
	if errors.Is(err, storage.ErrNotFound) {
		return "job_not_found", nil
	}
	if err != nil {
		return "", err
	}
	if job.ResourceID != event.ResourceID || job.JobType != jobType {
		return "event_job_mismatch", nil
	}
	if job.Status.IsTerminal() {
		if job.Status == storage.JobFailed && event.Attempt == job.Attempts {
			return job.ErrorCode, nil
		}
		return "", nil
	}
	if event.Attempt <= job.Attempts {
		if event.Attempt == job.Attempts && job.LeaseUntil != nil && job.LeaseUntil.After(time.Now()) {
			return "", ErrBusy
		}
		// Expired attempts are recovered by the relay under an ETag.
		return "", nil
	}
	if event.Attempt != job.Attempts+1 || job.LeaseUntil != nil {
		return "unexpected_attempt", nil
	}

	now := time.Now().UTC()
	lease := now.Add(leaseDuration)
	job.Status, job.Stage, job.Attempts = storage.JobProcessing, "verifying_original", event.Attempt
	job.LeaseUntil = &lease
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
	job, err = s.Jobs.Update(ctx, job, job.ETag)
	if err != nil {
		return "", err
	}

	if job.Status != storage.JobProcessing || job.LeaseUntil == nil || !job.LeaseUntil.Equal(lease) {
		return "", nil
	}

	processCtx, cancel := context.WithTimeout(ctx, processTimeout)
	pipeline, failure, verifyErr := s.verifyOriginal(processCtx, job)
	cancel()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if verifyErr != nil {
		if failure != "" || job.Attempts >= maxAttempts {
			if failure == "" {
				failure = "verification_attempts_exhausted"
			}
			finish(&job, storage.JobFailed, "failed")
			job.ErrorCode, job.ErrorMessage = failure, "No se pudo verificar el original almacenado"
		} else {
			return "", s.retry(ctx, job, "storage_error")
		}
	} else {
		finish(&job, storage.JobCompleted, "original_verified")
		job.Progress, job.Pipeline = 100, pipeline
		job.ErrorCode, job.ErrorMessage = "", ""
	}
	_, err = s.Jobs.Update(ctx, job, job.ETag)
	return failure, err
}

func (s *Service) verifyOriginal(ctx context.Context, job storage.Job) (pipeline, failure string, err error) {
	resource, err := s.Resources.Get(ctx, job.TenantID, job.ResourceID)
	if errors.Is(err, storage.ErrNotFound) {
		return "", "resource_not_found", err
	}
	if err != nil {
		return "", "", err
	}
	source, err := s.Objects.Get(ctx, s.RawContainer, resource.BlobPath)
	if errors.Is(err, storage.ErrNotFound) {
		return "", "original_not_found", err
	}
	if err != nil {
		return "", "", err
	}
	defer source.Body.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(source.Body, resource.FileSize+1))
	if err != nil {
		return "", "", err
	}
	if size != resource.FileSize || hex.EncodeToString(hash.Sum(nil)) != resource.ContentHash {
		return "", "original_integrity_error", errors.New("original size or SHA-256 does not match")
	}
	switch family, _, _ := strings.Cut(resource.ContentType, "/"); family {
	case "image", "audio", "video":
		return family, "", nil
	default:
		if resource.ContentType == "application/ogg" {
			return "audio", "", nil
		}
		return "document", "", nil
	}
}

func (s *Service) retry(ctx context.Context, job storage.Job, reason string) error {
	job.LeaseUntil = nil
	job.Stage = "retry_scheduled"
	job.ErrorCode, job.ErrorMessage = reason, "Se reintentará la verificación del original"
	delay := 5 * time.Second * (1 << (job.Attempts - 1))
	_, err := s.enqueue(ctx, job, job.Attempts+1, time.Now().Add(delay), "")
	return err
}

// Relay publishes pending messages and recovers attempts interrupted by a
// worker crash. Both operations can run safely in multiple worker replicas.
type Relay struct {
	Service *Service
	Publish func(context.Context, storage.OutboxEntry) error
	Logger  *slog.Logger
	Wake    <-chan struct{}
}

func (r Relay) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	nextRecovery := time.Time{}
	for ctx.Err() == nil {
		now := time.Now()
		if now.After(nextRecovery) {
			if err := r.recover(ctx, now); err != nil && ctx.Err() == nil {
				r.Logger.Error("no se pudieron recuperar intentos de ingesta", "error", err)
			}
			if err := r.republish(ctx, now); err != nil && ctx.Err() == nil {
				r.Logger.Error("no se pudieron reconciliar mensajes sin consumir", "error", err)
			}
			nextRecovery = now.Add(10 * time.Second)
		}
		if err := r.flush(ctx, now); err != nil && ctx.Err() == nil {
			r.Logger.Error("no se pudo consultar el outbox", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-r.Wake:
		case <-tick.C:
		}
	}
}

func (r Relay) flush(ctx context.Context, now time.Time) error {
	return r.Service.Jobs.VisitPending(ctx, now, func(event storage.OutboxEntry) error {
		sendCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		if err := r.Publish(sendCtx, event); err != nil {
			// Stop this sweep on a transport failure. All pending rows remain
			// available and the next sweep restarts after the poll interval.
			return err
		}
		if err := r.Service.Jobs.MarkSent(ctx, event); err != nil && !errors.Is(err, storage.ErrConflict) {
			r.Logger.Error("no se pudo confirmar la publicación", "event_id", event.ID, "error", err)
		}
		return nil
	})
}

func (r Relay) recover(ctx context.Context, now time.Time) error {
	return r.Service.Jobs.VisitExpired(ctx, now, func(job storage.Job) error {
		if job.JobType != jobType || job.Attempts < 1 {
			return nil
		}
		var err error
		if job.Attempts < maxAttempts {
			err = r.Service.retry(ctx, job, "worker_lease_expired")
		} else {
			finish(&job, storage.JobFailed, "failed")
			job.ErrorCode, job.ErrorMessage = "worker_lease_expired", "Se agotaron los intentos de ingesta"
			// Publish a terminal delivery to retain the exhausted job in the DLQ.
			_, err = r.Service.enqueue(ctx, job, job.Attempts, now, ":expired")
		}
		if errors.Is(err, storage.ErrConflict) || errors.Is(err, storage.ErrInvalidTransition) {
			return nil
		}
		return err
	})
}

// Service Bus can expire or dead-letter a message before it is claimed, for
// example if Table Storage was unreachable across all broker deliveries.
func (r Relay) republish(ctx context.Context, now time.Time) error {
	return r.Service.Jobs.VisitUnclaimed(ctx, now.Add(-10*time.Minute), func(job storage.Job) error {
		if job.JobType != jobType || job.Attempts >= maxAttempts {
			return nil
		}
		_, err := r.Service.enqueue(ctx, job, job.Attempts+1, now, ":"+ids.New())
		if errors.Is(err, storage.ErrConflict) {
			return nil
		}
		return err
	})
}
