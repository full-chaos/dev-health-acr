package hosted

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type orderedUsageStore struct {
	storage.CredentialStore
	events *[]string
}

func (s orderedUsageStore) TouchLastUsed(context.Context, string, string, string, time.Time) error {
	*s.events = append(*s.events, "last_used.flush")
	return nil
}

type orderedUsageAudit struct {
	storage.AuditStore
	events *[]string
}

func (s orderedUsageAudit) Record(context.Context, storage.AuditEvent) error {
	*s.events = append(*s.events, "usage_audit.flush")
	return nil
}

func TestRuntimeClose_flushes_usage_before_closing_postgres(t *testing.T) {
	// Given
	events := []string{}
	telemetry, err := auth.NewUsageTelemetry(orderedUsageStore{events: &events}, orderedUsageAudit{events: &events}, auth.UsageTelemetryOptions{QueueCapacity: 2, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(auth.UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	runtime := &Runtime{closers: []func() error{
		telemetry.Close,
		func() error {
			events = append(events, "postgres.close")
			return nil
		},
	}}

	// When
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}

	// Then
	want := []string{"last_used.flush", "usage_audit.flush", "postgres.close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("close ordering = %#v, want %#v", events, want)
	}
}
