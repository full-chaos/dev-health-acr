package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type recordedUsage struct {
	credentialID string
	ip           string
	userAgent    string
	usedAt       time.Time
}

type usageStore struct {
	storage.CredentialStore

	mu      sync.Mutex
	touches []recordedUsage
	err     error
}

func (s *usageStore) TouchLastUsed(_ context.Context, credentialID, ip, userAgent string, usedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.touches = append(s.touches, recordedUsage{credentialID: credentialID, ip: ip, userAgent: userAgent, usedAt: usedAt})
	return s.err
}

func (s *usageStore) Touches() []recordedUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedUsage(nil), s.touches...)
}

type usageAuditStore struct {
	storage.AuditStore

	mu     sync.Mutex
	events []storage.AuditEvent
	err    error
}

func (s *usageAuditStore) Record(_ context.Context, event storage.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return s.err
}

func (s *usageAuditStore) Events() []storage.AuditEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storage.AuditEvent(nil), s.events...)
}

type blockingUsageStore struct {
	storage.CredentialStore

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type uncooperativeUsageStore struct {
	storage.CredentialStore

	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *uncooperativeUsageStore) TouchLastUsed(context.Context, string, string, string, time.Time) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func (s *blockingUsageStore) TouchLastUsed(ctx context.Context, _ string, _, _ string, _ time.Time) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestUsageTelemetry_coalesces_latest_successful_uses_when_flushed(t *testing.T) {
	// Given
	store := &usageStore{}
	audit := &usageAuditStore{}
	telemetry, err := NewUsageTelemetry(store, audit, UsageTelemetryOptions{QueueCapacity: 4, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })
	older := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)

	// When
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", ClientIP: "127.0.0.1", UserAgent: "first", RequestID: "req_first", UsedAt: newer})
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", ClientIP: "127.0.0.2", UserAgent: "second", RequestID: "req_second", UsedAt: older})
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := telemetry.Flush(flushContext); err != nil {
		t.Fatal(err)
	}

	// Then
	touches := store.Touches()
	if len(touches) != 1 {
		t.Fatalf("touches = %d, want 1", len(touches))
	}
	if touches[0].usedAt != newer || touches[0].ip != "127.0.0.1" || touches[0].userAgent != "first" {
		t.Fatalf("last-used write = %#v, want latest successful use", touches[0])
	}
	events := audit.Events()
	if len(events) != 1 || events[0].Action != "credential_used" {
		t.Fatalf("events = %#v, want one credential_used event", events)
	}
	if uses, ok := events[0].Metadata["successful_use_count"].(int64); !ok || uses != 2 {
		t.Fatalf("successful_use_count = %#v, want 2", events[0].Metadata["successful_use_count"])
	}
	stats := telemetry.Stats()
	if stats.Coalesced != 1 || stats.Delivered != 1 || stats.Dropped != 0 {
		t.Fatalf("stats = %#v, want one coalesced delivered batch with no drops", stats)
	}
}

func TestUsageTelemetry_drops_when_bounded_queue_is_full(t *testing.T) {
	// Given
	store := &blockingUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := NewUsageTelemetry(store, &usageAuditStore{}, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })
	usedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: usedAt})
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	flushResult := make(chan error, 1)
	go func() { flushResult <- telemetry.Flush(flushContext) }()
	select {
	case <-store.started:
	case <-flushContext.Done():
		t.Fatal("usage delivery did not begin")
	}

	// When
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-2", UsedAt: usedAt})
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-3", UsedAt: usedAt})
	close(store.release)
	if err := <-flushResult; err != nil {
		t.Fatal(err)
	}

	// Then
	stats := telemetry.Stats()
	if stats.Dropped != 1 || stats.QueueCapacity != 1 {
		t.Fatalf("stats = %#v, want exactly one observable bounded-queue drop", stats)
	}
}

func TestUsageTelemetry_records_delivery_failures_without_retrying(t *testing.T) {
	// Given
	store := &usageStore{err: errors.New("injected store outage")}
	audit := &usageAuditStore{}
	telemetry, err := NewUsageTelemetry(store, audit, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Close() })

	// When
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	flushContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := telemetry.Flush(flushContext); err != nil {
		t.Fatal(err)
	}

	// Then
	if len(store.Touches()) != 1 || len(audit.Events()) != 1 {
		t.Fatalf("best-effort batch should make one write attempt per sink, touches=%d audits=%d", len(store.Touches()), len(audit.Events()))
	}
	stats := telemetry.Stats()
	if stats.DeliveryFailures != 1 || stats.Delivered != 0 {
		t.Fatalf("stats = %#v, want one delivery failure and no retry", stats)
	}
}

func TestUsageTelemetry_shutdown_flushes_pending_work(t *testing.T) {
	// Given
	store := &usageStore{}
	audit := &usageAuditStore{}
	telemetry, err := NewUsageTelemetry(store, audit, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})

	// When
	if err := telemetry.Close(); err != nil {
		t.Fatal(err)
	}

	// Then
	if len(store.Touches()) != 1 || len(audit.Events()) != 1 {
		t.Fatalf("shutdown must flush queued usage, touches=%d audits=%d", len(store.Touches()), len(audit.Events()))
	}
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-2", UsedAt: time.Date(2026, 7, 15, 12, 1, 0, 0, time.UTC)})
	if len(store.Touches()) != 1 || telemetry.Stats().Dropped != 1 {
		t.Fatalf("enqueue after worker completion touched=%d stats=%#v, want no store call and one drop", len(store.Touches()), telemetry.Stats())
	}
}

func TestUsageTelemetry_close_returns_when_a_store_ignores_its_deadline(t *testing.T) {
	// Given
	store := &uncooperativeUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := NewUsageTelemetry(store, &usageAuditStore{}, UsageTelemetryOptions{QueueCapacity: 1, FlushInterval: time.Hour, ShutdownTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("usage delivery did not begin")
	}

	// When
	started := time.Now()
	err = telemetry.Close()

	// Then
	if !errors.Is(err, ErrUsageTelemetryShutdownTimeout) || time.Since(started) > time.Second {
		t.Fatalf("Close() = %v after %s, want bounded shutdown timeout", err, time.Since(started))
	}
	close(store.release)
	select {
	case <-telemetry.done:
	case <-time.After(time.Second):
		t.Fatal("telemetry worker did not exit after the blocked store released")
	}
}

func TestUsageTelemetry_close_cancels_active_capacity_flush_and_joins_worker(t *testing.T) {
	// Given
	store := &blockingUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := NewUsageTelemetry(store, &usageAuditStore{}, UsageTelemetryOptions{
		QueueCapacity: 1, FlushInterval: time.Hour, ShutdownTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("capacity flush did not begin")
	}

	// When
	err = telemetry.Close()

	// Then
	if err != nil {
		t.Fatalf("Close() = %v, want joined cancellation", err)
	}
	select {
	case <-telemetry.done:
	default:
		t.Fatal("Close() returned before the worker joined")
	}
}

func TestUsageTelemetry_close_reports_timeout_only_until_uncooperative_worker_joins(t *testing.T) {
	// Given
	store := &uncooperativeUsageStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := NewUsageTelemetry(store, &usageAuditStore{}, UsageTelemetryOptions{
		QueueCapacity: 1, FlushInterval: time.Hour, ShutdownTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry.Enqueue(UsageRecord{OrgID: "org-1", CredentialID: "credential-1", UsedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)})
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("usage delivery did not begin")
	}

	// When
	first := telemetry.Close()
	select {
	case <-telemetry.done:
		t.Fatal("uncooperative worker joined before release")
	default:
	}
	second := telemetry.Close()
	close(store.release)
	select {
	case <-telemetry.done:
	case <-time.After(time.Second):
		t.Fatal("telemetry worker did not exit after the blocked store released")
	}
	third := telemetry.Close()

	// Then
	if !errors.Is(first, ErrUsageTelemetryShutdownTimeout) || !errors.Is(second, ErrUsageTelemetryShutdownTimeout) {
		t.Fatalf("Close() before join = (%v, %v), want shutdown timeout", first, second)
	}
	if third != nil {
		t.Fatalf("Close() after join = %v, want terminal result", third)
	}
}
