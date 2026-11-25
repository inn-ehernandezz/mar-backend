package servicebus

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azservicebus"

	"mar-backend/internal/ingestion"
	"mar-backend/internal/platform/config"
	"mar-backend/internal/storage"
)

type Bus struct {
	client *azservicebus.Client
	sender *azservicebus.Sender
	queue  string
}

func New(cfg config.BusConfig) (*Bus, error) {
	client, err := azservicebus.NewClientFromConnectionString(cfg.ConnectionString, nil)
	if err != nil {
		return nil, err
	}
	sender, err := client.NewSender(cfg.QueueIngestion, nil)
	if err != nil {
		_ = client.Close(context.Background())
		return nil, err
	}
	return &Bus{client: client, sender: sender, queue: cfg.QueueIngestion}, nil
}

func (b *Bus) Close(ctx context.Context) error {
	return b.client.Close(ctx)
}

func (b *Bus) Publish(ctx context.Context, entry storage.OutboxEntry) error {
	contentType := "application/json"
	return b.sender.SendMessage(ctx, &azservicebus.Message{
		MessageID: &entry.ID, ContentType: &contentType, Body: entry.Body,
	}, nil)
}

func (b *Bus) Consume(ctx context.Context, service *ingestion.Service, logger *slog.Logger) error {
	receiver, err := b.client.NewReceiverForQueue(b.queue, nil)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = receiver.Close(closeCtx)
	}()

	for ctx.Err() == nil {
		messages, err := receiver.ReceiveMessages(ctx, 1, nil)
		if err != nil {
			if ctx.Err() == nil {
				logger.Error("error al recibir de Service Bus", "error", err)
				wait(ctx)
			}
			continue
		}
		for _, message := range messages {
			handle(ctx, receiver, message, service, logger)
		}
	}
	return nil
}

func handle(ctx context.Context, receiver *azservicebus.Receiver, message *azservicebus.ReceivedMessage, service *ingestion.Service, logger *slog.Logger) {
	var event ingestion.Event
	reason := "invalid_ingestion_event"
	var processErr error
	if json.Unmarshal(message.Body, &event) == nil {
		logger = logger.With("job_id", event.JobID, "tenant_id", event.TenantID, "request_id", event.RequestID)
		processCtx, cancel := context.WithCancel(ctx)
		renewed := make(chan struct{})
		go func() {
			defer close(renewed)
			ticker := time.NewTicker(15 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-processCtx.Done():
					return
				case <-ticker.C:
					lockCtx, stop := context.WithTimeout(processCtx, 5*time.Second)
					err := receiver.RenewMessageLock(lockCtx, message, nil)
					stop()
					if err != nil && processCtx.Err() == nil {
						logger.Warn("se perdió el bloqueo de Service Bus", "error", err)
						cancel()
						return
					}
				}
			}
		}()
		reason, processErr = service.Process(processCtx, event)
		cancel()
		<-renewed
	}

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	switch {
	case processErr != nil:
		logger.Info("entrega de ingesta pendiente de reintento", "error", processErr)
		wait(ctx)
		err = receiver.AbandonMessage(settleCtx, message, nil)
	case reason != "":
		logger.Warn("mensaje de ingesta enviado a DLQ", "reason", reason)
		err = receiver.DeadLetterMessage(settleCtx, message, &azservicebus.DeadLetterOptions{Reason: &reason})
	default:
		err = receiver.CompleteMessage(settleCtx, message, nil)
	}
	if err != nil {
		logger.Warn("no se pudo liquidar el mensaje de ingesta", "error", err)
	}
}

func wait(ctx context.Context) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
