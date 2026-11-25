// Package ingestion implements the upload and job lifecycle independently of Azure SDKs.
package ingestion

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"mar-backend/internal/platform/ids"
	"mar-backend/internal/storage"
)

const jobType = "ingestion"

type Service struct {
	Objects      storage.ObjectStore
	Resources    storage.ResourceRepository
	Jobs         storage.IngestionJobs
	RawContainer string
	WakeRelay    func()
}

type Upload struct {
	TenantID    string
	Key         string
	RequestID   string
	FileName    string
	ContentType string
	Size        int64
	SHA256      string
	Body        io.Reader
}

func (s *Service) Submit(ctx context.Context, upload Upload) (storage.Job, error) {
	resourceID := storage.DeterministicPointID("mar-resource", upload.TenantID, upload.Key)
	jobID := storage.DeterministicPointID("mar-ingestion", upload.TenantID, upload.Key)
	tenantPath := base64.RawURLEncoding.EncodeToString([]byte(upload.TenantID))
	resource, err := s.Resources.Create(ctx, storage.Resource{
		TenantID: upload.TenantID, ResourceID: resourceID, FileName: upload.FileName,
		ContentType: upload.ContentType, FileSize: upload.Size, ContentHash: upload.SHA256,
		BlobPath: "tenants/" + tenantPath + "/" + resourceID + "/original", Status: storage.ResourcePending,
	})
	if err != nil {
		return storage.Job{}, err
	}
	if resource.FileName != upload.FileName || resource.ContentType != upload.ContentType ||
		resource.FileSize != upload.Size || resource.ContentHash != upload.SHA256 {
		return storage.Job{}, storage.ErrConflict
	}
	if job, err := s.Jobs.Get(ctx, upload.TenantID, jobID); !errors.Is(err, storage.ErrNotFound) {
		return job, err
	}

	if resource.Status == storage.ResourcePending {
		_, err = s.Objects.Put(ctx, storage.PutObjectInput{
			Container: s.RawContainer, Name: resource.BlobPath, Body: upload.Body,
			ContentType: upload.ContentType, Size: upload.Size, ContentHash: upload.SHA256, IfNoneMatch: true,
		})
		if errors.Is(err, storage.ErrPreconditionFailed) {
			// A concurrent upload or a previous request may already have committed the Blob.
			var info storage.ObjectInfo
			info, err = s.Objects.Stat(ctx, s.RawContainer, resource.BlobPath)
			if err == nil && (info.Size != upload.Size || info.Metadata["content_sha256"] != upload.SHA256) {
				return storage.Job{}, storage.ErrConflict
			}
		}
		if err != nil {
			return storage.Job{}, err
		}
		resource.Status = storage.ResourceStored
		if _, err = s.Resources.Update(ctx, resource, resource.ETag); err != nil {
			if !errors.Is(err, storage.ErrConflict) {
				return storage.Job{}, err
			}
			latest, err := s.Resources.Get(ctx, resource.TenantID, resource.ResourceID)
			if err != nil {
				return storage.Job{}, err
			}
			if latest.Status != storage.ResourceStored {
				return storage.Job{}, storage.ErrConflict
			}
		}
	}

	job := storage.Job{
		TenantID: upload.TenantID, JobID: jobID, ResourceID: resourceID, JobType: jobType,
		Status: storage.JobQueued, Stage: "queued", RequestID: upload.RequestID,
	}
	job, err = s.enqueue(ctx, job, 1, time.Now(), "")
	if errors.Is(err, storage.ErrConflict) {
		job, err = s.Jobs.Get(ctx, upload.TenantID, jobID)
	}
	return job, err
}

func (s *Service) Cancel(ctx context.Context, tenant, id string) (storage.Job, error) {
	for range 5 {
		job, err := s.Jobs.Get(ctx, tenant, id)
		if err != nil {
			return storage.Job{}, err
		}
		if job.Status == storage.JobCancelled {
			return job, nil
		}
		if job.Status.IsTerminal() {
			return storage.Job{}, storage.ErrConflict
		}
		finish(&job, storage.JobCancelled, "cancelled")
		job, err = s.Jobs.Update(ctx, job, job.ETag)
		if !errors.Is(err, storage.ErrConflict) && !errors.Is(err, storage.ErrInvalidTransition) {
			return job, err
		}
	}
	return storage.Job{}, storage.ErrConflict
}

// Event is the v1 message sent to the ingestion queue.
type Event struct {
	Version    int    `json:"schema_version"`
	TenantID   string `json:"tenant_id"`
	JobID      string `json:"job_id"`
	ResourceID string `json:"resource_id"`
	RequestID  string `json:"request_id"`
	Attempt    int32  `json:"attempt"`
}

func (e Event) Valid() bool {
	return e.Version == 1 && e.TenantID != "" && ids.Valid(e.JobID) &&
		ids.Valid(e.ResourceID) && e.Attempt > 0 && e.Attempt <= maxAttempts
}

func (s *Service) enqueue(ctx context.Context, job storage.Job, attempt int32, available time.Time, suffix string) (storage.Job, error) {
	body, err := json.Marshal(Event{
		Version: 1, TenantID: job.TenantID, JobID: job.JobID,
		ResourceID: job.ResourceID, RequestID: job.RequestID, Attempt: attempt,
	})
	if err != nil {
		return storage.Job{}, err
	}
	result, err := s.Jobs.Enqueue(ctx, job, storage.OutboxEntry{
		TenantID: job.TenantID, ID: fmt.Sprintf("%s:%d%s", job.JobID, attempt, suffix),
		Body: body, AvailableAt: available,
	})
	if err == nil && s.WakeRelay != nil {
		s.WakeRelay()
	}
	return result, err
}

func finish(job *storage.Job, status storage.JobStatus, stage string) {
	now := time.Now().UTC()
	job.Status, job.Stage = status, stage
	job.CompletedAt, job.LeaseUntil = &now, nil
}
