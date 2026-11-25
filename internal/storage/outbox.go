package storage

import (
	"context"
	"time"
)

// OutboxEntry is a durable publication in the jobs table.
type OutboxEntry struct {
	TenantID    string
	ID          string
	Body        []byte
	AvailableAt time.Time
	ETag        string
}

type IngestionJobs interface {
	JobRepository
	// Enqueue atomically inserts a job (or replaces it using its ETag) and adds
	// its corresponding outbox entry in the same partition.
	Enqueue(context.Context, Job, OutboxEntry) (Job, error)
	VisitPending(context.Context, time.Time, func(OutboxEntry) error) error
	MarkSent(context.Context, OutboxEntry) error
	VisitExpired(context.Context, time.Time, func(Job) error) error
	VisitUnclaimed(context.Context, time.Time, func(Job) error) error
}
