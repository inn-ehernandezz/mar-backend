package ingestion

import (
	"context"
	"errors"
	"sync"
	"time"

	"mar-backend/internal/documents"
	"mar-backend/internal/storage"
)

const documentLease = 60 * time.Second
const documentDeadline = 20 * time.Minute

func supportedJob(kind string) bool { return kind == jobType || kind == documentJobType }

func (s *Service) processDocument(ctx context.Context, job storage.Job, attempt int32) (string, error) {
	if s.Documents == nil {
		return "", errors.New("worker sin procesamiento documental habilitado")
	}
	now := time.Now().UTC()
	lease := now.Add(documentLease)
	job.Status, job.Attempts, job.LeaseUntil = storage.JobProcessing, attempt, &lease
	job.Pipeline, job.Stage = "document", "extracting"
	if job.StartedAt == nil {
		job.StartedAt = &now
	}
	saved, err := s.Jobs.Update(ctx, job, job.ETag)
	if err != nil {
		return "", err
	}
	if !ownsAttempt(saved, attempt, &lease) {
		return "", nil
	}

	workCtx, cancel := context.WithTimeout(ctx, documentDeadline)
	defer cancel()
	state := &documentAttempt{service: s, job: saved}
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-tick.C:
				if err := state.update(workCtx, "", 0); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	resource, runErr := s.Resources.Get(workCtx, job.TenantID, job.ResourceID)
	if runErr == nil {
		runErr = s.Documents.Run(workCtx, resource, func(stage string, progress int32) error {
			return state.update(workCtx, stage, progress)
		})
	} else if errors.Is(runErr, storage.ErrNotFound) {
		runErr = &documents.Failure{Code: "resource_not_found", Permanent: true, Err: runErr}
	}
	timedOut := errors.Is(workCtx.Err(), context.DeadlineExceeded)
	cancel()
	<-done
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	job = state.job
	current, err := s.Jobs.Get(ctx, job.TenantID, job.JobID)
	if err != nil {
		return "", err
	}
	if !ownsAttempt(current, attempt, job.LeaseUntil) {
		return "", nil
	}
	job = current

	if runErr == nil {
		finish(&job, storage.JobCompleted, "document_indexed")
		job.Progress, job.ErrorCode, job.ErrorMessage, job.ErrorStage = 100, "", "", ""
		_, err = s.Jobs.Update(ctx, job, job.ETag)
		return "", err
	}
	code, permanent := "document_processing_failed", false
	var failure *documents.Failure
	if errors.As(runErr, &failure) {
		code, permanent = failure.Code, failure.Permanent
	}
	if timedOut {
		code = "document_timeout"
	}
	if !permanent && attempt < maxAttempts {
		return "", s.retry(ctx, job, code)
	}
	job.ErrorStage = job.Stage
	finish(&job, storage.JobFailed, "failed")
	job.ErrorCode, job.ErrorMessage = code, "No se pudo completar el procesamiento documental"
	_, err = s.Jobs.Update(ctx, job, job.ETag)
	return code, err
}

type documentAttempt struct {
	mu      sync.Mutex
	service *Service
	job     storage.Job
}

func (a *documentAttempt) update(ctx context.Context, stage string, progress int32) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	next := a.job
	if stage == next.Stage && progress <= next.Progress {
		return nil
	}
	lease := time.Now().UTC().Add(documentLease)
	next.LeaseUntil = &lease
	if stage != "" {
		next.Stage, next.Progress = stage, max(next.Progress, progress)
		next.CommitStarted = next.CommitStarted || stage == "publishing"
	}
	job, err := a.service.Jobs.Update(ctx, next, next.ETag)
	if err != nil {
		return err
	}
	if !ownsAttempt(job, next.Attempts, &lease) {
		return storage.ErrConflict
	}
	a.job = job
	return nil
}

func ownsAttempt(job storage.Job, attempt int32, lease *time.Time) bool {
	return job.Status == storage.JobProcessing && job.Attempts == attempt &&
		job.LeaseUntil != nil && lease != nil && job.LeaseUntil.Equal(*lease)
}
