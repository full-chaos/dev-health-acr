package auth

import (
	"context"
	"maps"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type usageBatch struct {
	record UsageRecord
	count  int64
}

type flushRequest struct {
	context context.Context
	done    chan<- error
}

func (u *UsageTelemetry) run() {
	ticker := time.NewTicker(u.flushInterval)
	defer ticker.Stop()
	defer close(u.done)
	pending := make(map[string]usageBatch, u.queueCapacity)
	for {
		select {
		case record := <-u.queue:
			u.collect(pending, record)
			if len(pending) == u.queueCapacity {
				u.flush(context.Background(), pending)
			}
		case request := <-u.flushRequests:
			u.drain(pending)
			request.done <- u.flush(request.context, pending)
		case <-ticker.C:
			u.flush(context.Background(), pending)
		case <-u.stop:
			u.drain(pending)
			shutdownContext, cancel := context.WithTimeout(context.Background(), u.shutdownTimeout)
			err := u.flush(shutdownContext, pending)
			cancel()
			u.closeResult <- err
			return
		}
	}
}

func (u *UsageTelemetry) drain(pending map[string]usageBatch) {
	for {
		select {
		case record := <-u.queue:
			u.collect(pending, record)
		default:
			return
		}
	}
}

func (u *UsageTelemetry) collect(pending map[string]usageBatch, record UsageRecord) {
	key := record.OrgID + "\x00" + usageActorID(record) + "\x00" + usageAction(record) + "\x00" + usageResourceType(record) + "\x00" + usageResourceID(record)
	batch, exists := pending[key]
	if !exists {
		pending[key] = usageBatch{record: record, count: 1}
		return
	}
	batch.count++
	if record.UsedAt.After(batch.record.UsedAt) {
		batch.record = record
	}
	pending[key] = batch
	u.metrics.coalesced.Add(1)
}

func (u *UsageTelemetry) flush(ctx context.Context, pending map[string]usageBatch) error {
	for key, batch := range pending {
		delete(pending, key)
		if err := ctx.Err(); err != nil {
			u.metrics.shutdownDropped.Add(batch.count)
			for _, remaining := range pending {
				u.metrics.shutdownDropped.Add(remaining.count)
			}
			clear(pending)
			return err
		}
		u.deliver(ctx, batch)
	}
	return nil
}

func (u *UsageTelemetry) deliver(parent context.Context, batch usageBatch) {
	deliveryFailed := false
	if usageAction(batch.record) == "credential_used" {
		deliveryContext, cancel := context.WithTimeout(parent, u.deliveryTimeout)
		err := u.store.TouchLastUsed(deliveryContext, batch.record.CredentialID, batch.record.ClientIP, batch.record.UserAgent, batch.record.UsedAt.UTC())
		cancel()
		deliveryFailed = err != nil
		if err != nil {
			u.metrics.deliveryFailures.Add(1)
			u.logger.Warn("credential usage telemetry delivery failed", "sink", "last_used")
		}
	}
	if u.audit != nil {
		deliveryContext, cancel := context.WithTimeout(parent, u.deliveryTimeout)
		metadata := make(map[string]any, len(batch.record.Metadata)+1)
		maps.Copy(metadata, batch.record.Metadata)
		metadata["successful_use_count"] = batch.count
		err := u.audit.Record(deliveryContext, storage.AuditEvent{
			OrgID: batch.record.OrgID, ActorType: usageActorType(batch.record), ActorID: usageActorID(batch.record),
			Action: usageAction(batch.record), ResourceType: usageResourceType(batch.record), ResourceID: usageResourceID(batch.record),
			Status: "success", RequestID: batch.record.RequestID,
			Metadata: metadata, CreatedAt: batch.record.UsedAt.UTC(),
		})
		cancel()
		if err != nil {
			deliveryFailed = true
			u.metrics.deliveryFailures.Add(1)
			u.logger.Warn("credential usage telemetry delivery failed", "sink", "usage_audit")
		}
	}
	if !deliveryFailed {
		u.metrics.delivered.Add(1)
	}
}

func usageActorType(record UsageRecord) string {
	if record.ActorType != "" {
		return record.ActorType
	}
	return "credential"
}

func usageActorID(record UsageRecord) string {
	if record.ActorID != "" {
		return record.ActorID
	}
	return record.CredentialID
}

func usageAction(record UsageRecord) string {
	if record.Action != "" {
		return record.Action
	}
	return "credential_used"
}

func usageResourceType(record UsageRecord) string {
	if record.ResourceType != "" {
		return record.ResourceType
	}
	return "acr_credential"
}

func usageResourceID(record UsageRecord) string {
	if record.ResourceID != "" {
		return record.ResourceID
	}
	return usageActorID(record)
}
