package pgclarification

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
)

// newClarificationTestDatabase mirrors pginvestigation's own
// newInvestigationTestDatabase exactly: a fresh testcontainers Postgres,
// migrated to head (through 0016, this package's own migration), scoped to
// one test via t.Cleanup.
func newClarificationTestDatabase(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	container, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(ctx)) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.Up(ctx, db))
	return db
}

// validEvent returns a well-formed ClarificationSelectionEvent, scoped to
// orgID, for tests that want a baseline to mutate. Deliberately no
// SubjectLabel anywhere (sol review F2): ClarificationOfferedCandidate has
// no such field at all -- see TestInsertContext_NeverPersistsSubjectLabelContent
// for the red-first proof that this table's persisted JSONB stays that way.
func validEvent(orgID string) contextfabric.ClarificationSelectionEvent {
	offered := []contextfabric.ClarificationOfferedCandidate{
		{ReceiptID: "receipt-committed-01", SubjectKind: "project", SubjectCanonicalID: "project-ask-dev", State: "proposed", Confidence: 0.91, Rank: 0},
		{ReceiptID: "receipt-committed-02", SubjectKind: "project", SubjectCanonicalID: "project-ask-web", State: "proposed", Confidence: 0.62, Rank: 1},
	}
	return contextfabric.ClarificationSelectionEvent{
		OrgID: orgID, CapturedAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC),
		QuestionHash:  contextfabric.QuestionHash("Was Ask Dev ready to ship?"),
		PriorResultID: "result_clarify_00001", OfferedCandidates: offered, Selected: offered[0],
		SelectionProvenance: "web_assertion",
		ProjectionVersion:   "projection-v1", ModelIdentities: []string{"openai-compatible/gpt-5-nano"},
		RetrievalIdentity: contextfabric.ReuseRetrievalIdentity{EmbedRetrievalIdentity: "none", RetrievalPolicyVersion: "rp1"},
		PromptVersions: contextfabric.ReusePromptVersions{
			InterpretationPromptVersion: "context-fabric-interpretation.v7", SynthesisPromptVersion: "context-fabric-synthesis.v9",
		},
		VersionAuthorities: contextfabric.ReuseVersionAuthorities{
			QueryVersion: "devhealthfacts.clickhouse.v1", CanonicalServiceVersion: "context-fabric-facts.v1", ModelOutputSchemaVersion: "context-fabric-model-output.v1",
		},
	}
}

type storedSelectionRow struct {
	SelectionID                string
	OrgID                      string
	CapturedAt                 time.Time
	QuestionHash               string
	PriorResultID              string
	SelectedReceiptID          string
	SelectedSubjectKind        string
	SelectedSubjectCanonicalID string
	SelectionProvenance        string
	OfferedCandidatesRaw       []byte
	OfferedCandidates          []contextfabric.ClarificationOfferedCandidate
	PipelineContext            pipelineContextPayload
}

func mustLoadSelectionRow(t *testing.T, ctx context.Context, db *sql.DB, priorResultID string) storedSelectionRow {
	t.Helper()
	var row storedSelectionRow
	var pipelineJSON []byte
	require.NoError(t, db.QueryRowContext(ctx, `
SELECT selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id, selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context
FROM acr.context_fabric_clarification_selections WHERE prior_result_id = $1`, priorResultID).Scan(
		&row.SelectionID, &row.OrgID, &row.CapturedAt, &row.QuestionHash, &row.PriorResultID, &row.SelectedReceiptID,
		&row.SelectedSubjectKind, &row.SelectedSubjectCanonicalID, &row.SelectionProvenance, &row.OfferedCandidatesRaw, &pipelineJSON,
	))
	require.NotEmpty(t, row.SelectionID, "selection_id must be a real application-generated primary key, not left blank")
	require.NoError(t, json.Unmarshal(row.OfferedCandidatesRaw, &row.OfferedCandidates))
	require.NoError(t, json.Unmarshal(pipelineJSON, &row.PipelineContext))
	return row
}

// TestInsertContext_PersistsEveryField is the red-first proof for the write
// path itself: every field on a well-formed event lands in the row exactly,
// including the full (not just-the-selection) offered candidate set and the
// composed pipeline_context blob.
func TestInsertContext_PersistsEveryField(t *testing.T) {
	ctx := context.Background()
	db := newClarificationTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-clarify-persist")
	require.NoError(t, sink.insertContext(ctx, event))

	row := mustLoadSelectionRow(t, ctx, db, event.PriorResultID)
	require.Equal(t, event.OrgID, row.OrgID)
	require.True(t, event.CapturedAt.Equal(row.CapturedAt), "captured_at = %v, want %v", row.CapturedAt, event.CapturedAt)
	require.Equal(t, event.QuestionHash, row.QuestionHash)
	require.Equal(t, event.Selected.ReceiptID, row.SelectedReceiptID)
	require.Equal(t, event.Selected.SubjectKind, row.SelectedSubjectKind)
	require.Equal(t, event.Selected.SubjectCanonicalID, row.SelectedSubjectCanonicalID)
	require.Equal(t, event.SelectionProvenance, row.SelectionProvenance)
	require.Equal(t, event.OfferedCandidates, row.OfferedCandidates, "offered_candidates must persist the COMPLETE candidate set, not just the selection")
	require.Equal(t, pipelineContextPayload{
		ProjectionVersion: event.ProjectionVersion, ModelIdentities: event.ModelIdentities,
		EmbedRetrievalIdentity: event.RetrievalIdentity.EmbedRetrievalIdentity, RetrievalPolicyVersion: event.RetrievalIdentity.RetrievalPolicyVersion,
		InterpretationPromptVersion: event.PromptVersions.InterpretationPromptVersion, SynthesisPromptVersion: event.PromptVersions.SynthesisPromptVersion,
		QueryVersion: event.VersionAuthorities.QueryVersion, CanonicalServiceVersion: event.VersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion: event.VersionAuthorities.ModelOutputSchemaVersion,
	}, row.PipelineContext)
}

// TestInsertContext_NeverPersistsSubjectLabelContent is the red-first proof
// for sol review F2: offered_candidates' RAW marshaled JSON must never
// carry subject display-label content or even a "label" key at all.
// ClarificationOfferedCandidate has no SubjectLabel field (the Go type
// system already makes the leak impossible today), so this asserts the
// STORED SHAPE directly, on the raw bytes actually written to Postgres --
// the assertion a future PR that re-added a label field to the JSON
// payload (without also reintroducing the Go struct field, e.g. via a
// custom MarshalJSON) would still be caught by.
func TestInsertContext_NeverPersistsSubjectLabelContent(t *testing.T) {
	ctx := context.Background()
	db := newClarificationTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	event := validEvent("org-clarify-no-label")
	require.NoError(t, sink.insertContext(ctx, event))

	row := mustLoadSelectionRow(t, ctx, db, event.PriorResultID)
	raw := strings.ToLower(string(row.OfferedCandidatesRaw))
	require.NotContains(t, raw, "label", "offered_candidates must never carry subject display-label content or key (sol review F2)")
}

// TestInsertContext_RejectsMalformedEventsWithoutTouchingTheDatabase is the
// red-first proof for validateEvent: a malformed event must fail BEFORE
// any SQL runs, and must insert nothing.
func TestInsertContext_RejectsMalformedEventsWithoutTouchingTheDatabase(t *testing.T) {
	ctx := context.Background()
	db := newClarificationTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	cases := map[string]func(*contextfabric.ClarificationSelectionEvent){
		"empty org_id":                        func(e *contextfabric.ClarificationSelectionEvent) { e.OrgID = "" },
		"empty question_hash":                 func(e *contextfabric.ClarificationSelectionEvent) { e.QuestionHash = "" },
		"short question_hash":                 func(e *contextfabric.ClarificationSelectionEvent) { e.QuestionHash = "abc123" },
		"empty prior_result_id":               func(e *contextfabric.ClarificationSelectionEvent) { e.PriorResultID = "" },
		"empty selected receipt id":           func(e *contextfabric.ClarificationSelectionEvent) { e.Selected.ReceiptID = "" },
		"empty selected subject kind":         func(e *contextfabric.ClarificationSelectionEvent) { e.Selected.SubjectKind = "" },
		"empty selected subject canonical id": func(e *contextfabric.ClarificationSelectionEvent) { e.Selected.SubjectCanonicalID = "" },
		"zero captured_at":                    func(e *contextfabric.ClarificationSelectionEvent) { e.CapturedAt = time.Time{} },
		"provenance outside the closed vocabulary": func(e *contextfabric.ClarificationSelectionEvent) {
			e.SelectionProvenance = "not-a-real-provenance-value"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			event := validEvent("org-clarify-reject-" + name)
			mutate(&event)
			err := sink.insertContext(ctx, event)
			require.Error(t, err, "expected %s to be rejected", name)

			var count int
			require.NoError(t, db.QueryRowContext(ctx,
				`SELECT count(*) FROM acr.context_fabric_clarification_selections WHERE org_id = $1`, event.OrgID).Scan(&count))
			require.Zero(t, count, "a rejected event must not insert a row")
		})
	}
}

// TestFindSelections_OrgScoped proves the org-scoping requirement directly
// against Postgres: two organizations' rows never cross when queried by
// org_id, the same non-negotiable boundary
// acr.context_fabric_investigation_results enforces.
func TestFindSelections_OrgScoped(t *testing.T) {
	ctx := context.Background()
	db := newClarificationTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = sink.Close(closeCtx)
	})

	orgA := validEvent("org-clarify-scope-a")
	orgA.PriorResultID = "result_scope_a_0001"
	orgB := validEvent("org-clarify-scope-b")
	orgB.PriorResultID = "result_scope_b_0001"
	require.NoError(t, sink.insertContext(ctx, orgA))
	require.NoError(t, sink.insertContext(ctx, orgB))

	var orgACount, crossCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM acr.context_fabric_clarification_selections WHERE org_id = $1`, orgA.OrgID).Scan(&orgACount))
	require.Equal(t, 1, orgACount)
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM acr.context_fabric_clarification_selections WHERE org_id = $1 AND prior_result_id = $2`, orgA.OrgID, orgB.PriorResultID).Scan(&crossCount))
	require.Zero(t, crossCount, "org A's scoped query must never see org B's row")
}

// TestRecordSelection_DeliversAsynchronously proves the public enqueue ->
// background-worker -> insert path actually lands a row, not just the
// directly-tested insertContext helper.
func TestRecordSelection_DeliversAsynchronously(t *testing.T) {
	ctx := context.Background()
	db := newClarificationTestDatabase(t, ctx)
	sink, err := NewSink(db, SinkOptions{})
	require.NoError(t, err)

	event := validEvent("org-clarify-async")
	sink.RecordSelection(ctx, event)

	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, sink.Close(closeCtx), "Close must drain the queued event before returning")

	var count int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM acr.context_fabric_clarification_selections WHERE org_id = $1`, event.OrgID).Scan(&count))
	require.Equal(t, 1, count)
	require.Equal(t, int64(1), sink.Metrics().Delivered)
}

// blockingHandler is a slog.Handler whose Handle call never returns (it
// blocks forever on an unbuffered, never-received channel receive). Wired
// as a Sink's logger, it turns "the caller path called the logger" into an
// immediate, unmistakable test timeout rather than a silent pass -- sol
// review F4's own instruction: strengthen the saturation test with a
// deliberately-blocking handler, since discardLogger() cannot distinguish
// "never called" from "called and silently swallowed."
type blockingHandler struct{}

func (blockingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (blockingHandler) Handle(context.Context, slog.Record) error {
	select {}
}
func (blockingHandler) WithAttrs([]slog.Attr) slog.Handler { return blockingHandler{} }
func (blockingHandler) WithGroup(string) slog.Handler      { return blockingHandler{} }

// TestRecordSelection_DropsOnFullQueueWithoutBlocking is the fail-open
// proof CHAOS-3859 demands: RecordSelection must never delay a caller, even
// when the queue is saturated and Postgres is never given a chance to
// drain it (the worker is never started for this test's Sink). The logger
// is a blockingHandler (sol review F4), so if RecordSelection's drop path
// ever touches it again, this test hangs and times out rather than passing
// vacuously the way discardLogger() would.
func TestRecordSelection_DropsOnFullQueueWithoutBlocking(t *testing.T) {
	sink := &Sink{
		queue: make(chan contextfabric.ClarificationSelectionEvent, 2),
		// stop/done are deliberately left nil/unused: this Sink's worker is
		// never started, so RecordSelection's non-blocking select is the
		// only thing under test.
		logger: slog.New(blockingHandler{}),
	}
	event := validEvent("org-clarify-drop")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			sink.RecordSelection(context.Background(), event)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordSelection blocked past a full queue -- capture must never delay a caller (or touched the logger, which this test's blockingHandler would also manifest as a hang)")
	}
	metrics := sink.Metrics()
	require.Equal(t, int64(2), metrics.Enqueued, "exactly the queue capacity should have enqueued")
	require.Equal(t, int64(8), metrics.Dropped, "the remainder must be dropped, not blocked on")
}

// capturingHandler records every log record's Message, for
// TestWorker_LogsPeriodicDropSummary -- a real (non-discarding,
// non-blocking) slog.Handler, since that test needs to observe what the
// WORKER goroutine actually logs.
type capturingHandler struct {
	mu       sync.Mutex
	messages []string
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, record slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.messages = append(h.messages, record.Message)
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }
func (h *capturingHandler) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.messages...)
}

// TestWorker_LogsPeriodicDropSummary proves the OTHER half of sol review
// F4: dropping the synchronous caller-path log call must not mean drops
// become invisible -- the WORKER goroutine is what turns a nonzero drop
// count into a log line. Drops are injected by incrementing the metrics
// counter DIRECTLY (bypassing RecordSelection/the queue entirely) so no
// event is ever handed to the worker's insert() path -- this Sink has no
// real *sql.DB, and insert() would otherwise be the thing under test
// rather than logDroppedSummary. Close's own stop-path call to
// logDroppedSummary (run()'s `case <-s.stop`) is what this test observes,
// which is deterministic and needs no ticker wait.
func TestWorker_LogsPeriodicDropSummary(t *testing.T) {
	handler := &capturingHandler{}
	sink := &Sink{
		queue: make(chan contextfabric.ClarificationSelectionEvent, 1),
		stop:  make(chan struct{}), done: make(chan struct{}),
		workerCtx: context.Background(), cancelWorker: func() {},
		logger: slog.New(handler),
	}
	sink.metrics.addDropped()
	sink.metrics.addDropped()
	go sink.run()

	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, sink.Close(closeCtx), "Close's own final logDroppedSummary call must fire even without waiting for the ticker")

	found := false
	for _, message := range handler.snapshot() {
		if strings.Contains(message, "periodic summary") {
			found = true
			break
		}
	}
	require.True(t, found, "expected the worker to log a periodic drop summary, got messages: %#v", handler.snapshot())
	require.Equal(t, int64(2), sink.Metrics().Dropped)
}

// TestClose_CancelsWorkerContextOnTimeout is the red-first proof for sol
// review F6's second, independent line of defense: if Close's own ctx
// expires before the worker signals <-s.done> (a worker that is still
// mid-INSERT, or simply never finishes), Close must cancel workerCtx so
// that INSERT -- and any future one this worker might still attempt --
// abandons immediately rather than racing whatever the caller does next
// (in production, closing the very Postgres pool that INSERT's context
// derives from). done is deliberately never closed here, simulating
// exactly that "worker still running" case without needing a real slow
// database call.
func TestClose_CancelsWorkerContextOnTimeout(t *testing.T) {
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	sink := &Sink{
		stop: make(chan struct{}), done: make(chan struct{}),
		workerCtx: workerCtx, cancelWorker: cancelWorker,
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := sink.Close(closeCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded, "Close should report its own timeout, not swallow it")
	require.Error(t, workerCtx.Err(), "Close's timeout must cancel the worker's own context so an in-flight or future INSERT abandons immediately")
}
