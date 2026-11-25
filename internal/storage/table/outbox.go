package table

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"

	"mar-backend/internal/storage"
)

var _ storage.IngestionJobs = (*JobRepository)(nil)

// Fixed precision preserves timestamp order in OData string comparisons.
const sortableTime = "2006-01-02T15:04:05.000000000Z"

func formatSortableTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(sortableTime)
}

type outboxEntity struct {
	PartitionKey string
	RowKey       string
	JobType      string
	OutboxState  string
	AvailableAt  string
	Body         string
}

func (r *JobRepository) Enqueue(ctx context.Context, job storage.Job, event storage.OutboxEntry) (storage.Job, error) {
	if err := job.Validate(); err != nil {
		return storage.Job{}, err
	}
	if job.TenantID != event.TenantID || event.ID == "" || len(event.Body) == 0 {
		return storage.Job{}, storage.ErrInvalidArgument
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.DispatchID = event.ID
	available := event.AvailableAt.UTC()
	job.DispatchAt = &available
	if job.SchemaVersion == 0 {
		job.SchemaVersion = storage.EntitySchemaVersion
	}
	jobBody, err := json.Marshal(toJobEntity(job))
	if err != nil {
		return storage.Job{}, err
	}
	eventBody, err := json.Marshal(outboxEntity{
		PartitionKey: event.TenantID,
		RowKey:       "outbox:" + event.ID,
		OutboxState:  "pending",
		AvailableAt:  event.AvailableAt.UTC().Format(sortableTime),
		Body:         string(event.Body),
	})
	if err != nil {
		return storage.Job{}, err
	}
	action := aztables.TransactionAction{ActionType: aztables.TransactionTypeAdd, Entity: jobBody}
	if job.ETag != "" {
		action.ActionType = aztables.TransactionTypeUpdateReplace
		action.IfMatch = etagOrWildcard(job.ETag)
	}
	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	_, err = r.client.SubmitTransaction(c, []aztables.TransactionAction{
		action, {ActionType: aztables.TransactionTypeAdd, Entity: eventBody},
	}, nil)
	if err != nil {
		return storage.Job{}, fmt.Errorf("table: guardar trabajo y outbox: %w", mapTransactionError(err))
	}
	return job, nil
}

// The Azure SDK reports the outer 202 for a batch with an unsuccessful action.
// Recover the inner status from the multipart response to distinguish conflicts.
func mapTransactionError(err error) error {
	var response *azcore.ResponseError
	if !errors.As(err, &response) || response.StatusCode != http.StatusAccepted || response.RawResponse == nil {
		return mapError(err)
	}
	raw := response.RawResponse
	_, params, parseErr := mime.ParseMediaType(raw.Header.Get("Content-Type"))
	body, bodyErr := runtime.Payload(raw)
	if parseErr != nil || bodyErr != nil || params["boundary"] == "" {
		return err
	}
	changeset, parseErr := multipart.NewReader(bytes.NewReader(body), params["boundary"]).NextPart()
	if parseErr != nil {
		return err
	}
	defer changeset.Close()
	_, params, parseErr = mime.ParseMediaType(changeset.Header.Get("Content-Type"))
	if parseErr != nil || params["boundary"] == "" {
		return err
	}
	reader := multipart.NewReader(changeset, params["boundary"])
	for {
		part, parseErr := reader.NextPart()
		if parseErr != nil {
			return err
		}
		action, parseErr := http.ReadResponse(bufio.NewReader(part), raw.Request)
		if parseErr == nil {
			_ = action.Body.Close()
			switch action.StatusCode {
			case http.StatusConflict, http.StatusPreconditionFailed:
				_ = part.Close()
				return fmt.Errorf("%w: %w", storage.ErrConflict, err)
			}
		}
		_ = part.Close()
	}
}

func (r *JobRepository) VisitPending(ctx context.Context, now time.Time, visit func(storage.OutboxEntry) error) error {
	filter := fmt.Sprintf("OutboxState eq 'pending' and AvailableAt le '%s'", now.UTC().Format(sortableTime))
	return r.visit(ctx, filter, func(raw []byte) error {
		var entity outboxEntity
		if err := json.Unmarshal(raw, &entity); err != nil {
			return err
		}
		c, cancel := context.WithTimeout(ctx, r.timeout)
		defer cancel()
		resp, err := r.client.GetEntity(c, entity.PartitionKey, entity.RowKey, nil)
		if err != nil {
			return mapError(err)
		}
		if err := json.Unmarshal(resp.Value, &entity); err != nil {
			return err
		}
		if entity.OutboxState != "pending" {
			return nil
		}
		id, ok := strings.CutPrefix(entity.RowKey, "outbox:")
		if !ok {
			return fmt.Errorf("table: fila de outbox inválida %q", entity.RowKey)
		}
		available, err := parseTime(entity.AvailableAt)
		if err != nil {
			return err
		}
		return visit(storage.OutboxEntry{
			TenantID: entity.PartitionKey, ID: id,
			Body: []byte(entity.Body), AvailableAt: available, ETag: string(resp.ETag),
		})
	})
}

func (r *JobRepository) MarkSent(ctx context.Context, event storage.OutboxEntry) error {
	body, err := json.Marshal(map[string]string{
		"PartitionKey": event.TenantID, "RowKey": "outbox:" + event.ID, "OutboxState": "sent",
	})
	if err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	_, err = r.client.UpdateEntity(c, body, &aztables.UpdateEntityOptions{
		IfMatch: etagOrWildcard(event.ETag), UpdateMode: aztables.UpdateModeMerge,
	})
	return mapError(err)
}

func (r *JobRepository) VisitExpired(ctx context.Context, now time.Time, visit func(storage.Job) error) error {
	filter := fmt.Sprintf("Status eq 'PROCESSING' and LeaseUntil gt '' and LeaseUntil le '%s'", now.UTC().Format(sortableTime))
	return r.visit(ctx, filter, func(raw []byte) error {
		job, err := fromJobEntityBytes(raw)
		if err != nil {
			return err
		}
		job, err = r.Get(ctx, job.TenantID, job.JobID)
		if err != nil {
			return err
		}
		if job.Status != storage.JobProcessing || job.LeaseUntil == nil || job.LeaseUntil.After(now) {
			return nil
		}
		return visit(job)
	})
}

// VisitUnclaimed finds jobs with an old publication that was never claimed.
// A pending outbox entry is already durable and needs no replacement.
func (r *JobRepository) VisitUnclaimed(ctx context.Context, cutoff time.Time, visit func(storage.Job) error) error {
	filter := fmt.Sprintf("(Status eq 'QUEUED' or Status eq 'PROCESSING') and LeaseUntil eq '' and DispatchAt gt '' and DispatchAt le '%s'",
		cutoff.UTC().Format(sortableTime))
	return r.visit(ctx, filter, func(raw []byte) error {
		var entity jobEntity
		if err := json.Unmarshal(raw, &entity); err != nil {
			return err
		}
		job, err := r.Get(ctx, entity.PartitionKey, entity.RowKey)
		if err != nil {
			return err
		}
		if (job.Status != storage.JobQueued && job.Status != storage.JobProcessing) ||
			job.LeaseUntil != nil || job.DispatchAt == nil || job.DispatchAt.After(cutoff) || job.DispatchID == "" {
			return nil
		}
		c, cancel := context.WithTimeout(ctx, r.timeout)
		defer cancel()
		resp, err := r.client.GetEntity(c, job.TenantID, "outbox:"+job.DispatchID, nil)
		if err != nil {
			return mapError(err)
		}
		var entry outboxEntity
		if err := json.Unmarshal(resp.Value, &entry); err != nil {
			return err
		}
		if entry.OutboxState != "sent" {
			return nil
		}
		return visit(job)
	})
}

func (r *JobRepository) visit(ctx context.Context, filter string, visit func([]byte) error) error {
	pager := r.client.NewListEntitiesPager(&aztables.ListEntitiesOptions{Filter: &filter, Top: ptr(int32(defaultPageLimit))})
	for pager.More() {
		c, cancel := context.WithTimeout(ctx, r.timeout)
		page, err := pager.NextPage(c)
		cancel()
		if err != nil {
			return mapError(err)
		}
		for _, entity := range page.Entities {
			if err := visit(entity); err != nil {
				return err
			}
		}
	}
	return nil
}
