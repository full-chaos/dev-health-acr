package hosted

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
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

type uncooperativeTelemetryStore struct {
	storage.CredentialStore

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type cancellationThenSuccessUsageStore struct {
	storage.CredentialStore

	started chan struct{}
	once    sync.Once
	calls   atomic.Int64
}

func (s *cancellationThenSuccessUsageStore) TouchLastUsed(ctx context.Context, _ string, _ string, _ string, _ time.Time) error {
	if s.calls.Add(1) == 1 {
		s.once.Do(func() { close(s.started) })
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (s *uncooperativeTelemetryStore) TouchLastUsed(context.Context, string, string, string, time.Time) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
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

func TestRuntimeClose_skips_postgres_until_an_unjoined_telemetry_worker_stops(t *testing.T) {
	// Given
	events := []string{}
	store := &uncooperativeTelemetryStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := auth.NewUsageTelemetry(store, orderedUsageAudit{events: &events}, auth.UsageTelemetryOptions{
		QueueCapacity: 1, FlushInterval: time.Hour, ShutdownTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(auth.UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("usage delivery did not begin")
	}
	runtime := &Runtime{
		usageTelemetry: telemetry,
		closers: []func() error{func() error {
			events = append(events, "independent.close")
			return nil
		}},
		postgresClose: func() error {
			events = append(events, "postgres.close")
			return nil
		},
	}

	// When
	firstErr := runtime.Close()
	close(store.release)
	select {
	case <-telemetry.Done():
	case <-time.After(time.Second):
		t.Fatal("telemetry worker did not join after release")
	}
	secondErr := runtime.Close()

	// Then
	if !errors.Is(firstErr, auth.ErrUsageTelemetryShutdownTimeout) {
		t.Fatalf("first Close() = %v, want telemetry shutdown timeout", firstErr)
	}
	if secondErr != nil {
		t.Fatalf("second Close() = %v, want joined shutdown", secondErr)
	}
	want := []string{"independent.close", "usage_audit.flush", "postgres.close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("close ordering = %#v, want %#v", events, want)
	}
}

func TestRuntimeClose_closes_postgres_after_joined_worker_reports_a_queue_drop(t *testing.T) {
	// Given
	store := &cancellationThenSuccessUsageStore{started: make(chan struct{})}
	telemetry, err := auth.NewUsageTelemetry(store, nil, auth.UsageTelemetryOptions{
		QueueCapacity: 1, FlushInterval: time.Hour, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	usedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	telemetry.Enqueue(auth.UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: usedAt})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("usage delivery did not begin")
	}
	telemetry.Enqueue(auth.UsageRecord{OrgID: "org-1", CredentialID: "credential-2", UsedAt: usedAt})
	telemetry.Enqueue(auth.UsageRecord{OrgID: "org-1", CredentialID: "credential-3", UsedAt: usedAt})
	postgresClosed := false
	runtime := &Runtime{usageTelemetry: telemetry, postgresClose: func() error {
		postgresClosed = true
		return nil
	}}

	// When
	err = runtime.Close()

	// Then
	if err != nil || !postgresClosed || telemetry.Stats().Dropped != 1 {
		t.Fatalf("Close() = %v postgres_closed=%t stats=%#v, want joined shutdown with queue drop before postgres close", err, postgresClosed, telemetry.Stats())
	}
}
