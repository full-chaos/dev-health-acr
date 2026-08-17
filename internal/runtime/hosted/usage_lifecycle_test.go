package hosted

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgclarification"
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

// TestRuntimeClose_attemptsClarificationSinkCloseEvenWhenUsageTelemetryTimesOut
// is CHAOS-3859 sol review F6's red-first proof: the bug this fixes is
// usageTelemetry timing out used to return EARLY, before
// clarificationSink.Close was ever called at all -- a queued clarification
// selection would then never even get a chance to drain, indefinitely,
// every time usageTelemetry's own worker happened to be slow. This test
// uses idleConnector (investigator_composition_test.go's own fixture --
// Connect always errors immediately, no real network I/O) as the sink's
// *sql.DB, so its worker's one queued INSERT attempt fails FAST and
// deterministically; what's under test is only whether the ATTEMPT
// happened at all before Close returned, not whether it succeeded.
func TestRuntimeClose_attemptsClarificationSinkCloseEvenWhenUsageTelemetryTimesOut(t *testing.T) {
	// Given an usageTelemetry worker that will NOT finish before its own
	// ShutdownTimeout (the exact fixture TestRuntimeClose_skips_postgres_
	// until_an_unjoined_telemetry_worker_stops above already established).
	store := &uncooperativeTelemetryStore{started: make(chan struct{}), release: make(chan struct{})}
	telemetry, err := auth.NewUsageTelemetry(store, nil, auth.UsageTelemetryOptions{
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

	// And a clarification sink with one event already queued.
	sinkDB := sql.OpenDB(idleConnector{})
	defer sinkDB.Close()
	sink, err := pgclarification.NewSink(sinkDB, pgclarification.SinkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sink.RecordSelection(context.Background(), contextfabric.ClarificationSelectionEvent{
		OrgID: "org-1", CapturedAt: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
		QuestionHash:        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		PriorResultID:       "result_1",
		Selected:            contextfabric.ClarificationOfferedCandidate{ReceiptID: "receipt_1", SubjectKind: "project", SubjectCanonicalID: "project_1"},
		SelectionProvenance: "web_assertion",
	})

	postgresClosed := false
	runtime := &Runtime{
		usageTelemetry: telemetry, clarificationSink: sink,
		postgresClose: func() error {
			postgresClosed = true
			return nil
		},
	}

	// When
	closeErr := runtime.Close()
	close(store.release)
	select {
	case <-telemetry.Done():
	case <-time.After(time.Second):
		t.Fatal("telemetry worker did not join after release")
	}

	// Then: usageTelemetry's own timeout is still reported, and postgres
	// still does not close early (both unchanged from the existing
	// skips_postgres test above) -- but the clarification sink's worker
	// MUST have been given its own chance to run in the meantime, proven
	// by it having actually attempted (and, via idleConnector, failed) its
	// one queued INSERT rather than being left dangling.
	if !errors.Is(closeErr, auth.ErrUsageTelemetryShutdownTimeout) {
		t.Fatalf("Close() = %v, want telemetry shutdown timeout", closeErr)
	}
	if postgresClosed {
		t.Fatal("postgres must not close while a worker may still be using it")
	}
	metrics := sink.Metrics()
	if metrics.DeliveryFailures != 1 {
		t.Fatalf("clarificationSink.Metrics() = %#v, want the queued event to have been attempted (and failed, via idleConnector) even though usageTelemetry timed out -- this is the exact bug sol review F6 found: clarificationSink.Close used to never be called at all in this branch", metrics)
	}
}
