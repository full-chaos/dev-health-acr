// Package pgclarification is the production Postgres implementation of
// contextfabric.ClarificationSelectionSink (CHAOS-3859, capture-only
// phase): it persists clarification-selection events to
// acr.context_fabric_clarification_selections (migration 0016) and nothing
// else -- no read path, no feedback into ranking or thresholds. That table
// is org-scoped exactly like acr.context_fabric_investigation_results; a
// future read path MUST filter by org_id the same way every other Context
// Fabric store call does (internal/storage/AGENTS.md's convention).
package pgclarification

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	// defaultQueueCapacity bounds the in-memory backlog between Enqueue
	// (RecordSelection) and the single background worker that performs the
	// actual INSERT. Sized like auth.UsageTelemetry's own default (256):
	// generous for this event's low expected rate (one per resolved
	// clarification, not one per request) without holding an unbounded
	// amount of memory if Postgres falls behind or is briefly unreachable.
	defaultQueueCapacity = 256
	maximumQueueCapacity = 4096
	// defaultInsertTimeout bounds ONE background INSERT. It is deliberately
	// NOT derived from the request context that produced the event --
	// RecordSelection's ctx parameter only governs the instant, non-blocking
	// enqueue; by the time the worker dequeues and inserts, the original
	// HTTP/MCP request may already have returned. Deriving each insert's
	// context from the Sink's own long-lived workerCtx (canceled only by
	// Close, see below) rather than the caller's request context is what
	// makes this genuinely fire-and-forget rather than accidentally tying
	// delivery to a caller who has already gone away.
	defaultInsertTimeout = 5 * time.Second
	// dropSummaryInterval bounds how often the WORKER goroutine (never the
	// caller) logs a queue-full summary -- sol review F4: RecordSelection's
	// full-queue branch used to call the logger synchronously from the
	// CALLER's own goroutine, so a blocking slog.Handler could delay a real
	// investigation. The caller path now only ever increments an atomic
	// counter; this interval is how often the worker checks it for
	// anything new to report.
	dropSummaryInterval = 30 * time.Second
)

// SinkOptions configures Sink. Every field has a sane default; the zero
// value is a fully usable configuration.
type SinkOptions struct {
	QueueCapacity int
	InsertTimeout time.Duration
	Logger        *slog.Logger
}

// SinkMetrics exposes low-cardinality counters for queue saturation and
// delivery health, the same shape auth.UsageTelemetryStats offers for its
// own bounded-queue sink -- callers can export these through their metrics
// pipeline without attaching org, question, or subject identifiers.
type SinkMetrics struct {
	Enqueued         int64
	Dropped          int64
	Delivered        int64
	DeliveryFailures int64
}

// sinkMetrics' four counters are atomic.Int64, not mutex-guarded (sol
// review F4-a): RecordSelection's full-queue branch increments dropped on
// the CALLER's own goroutine (see the package doc on dropSummaryInterval),
// while Metrics() and the worker's periodic summary read from a completely
// different goroutine. A shared sync.Mutex would let a caller's
// RecordSelection contend with -- and however briefly, block behind -- a
// concurrent Metrics() read, which is exactly the kind of caller-path
// latency this sink exists to avoid. Independent atomics give every
// counter lock-free increments and reads with no cross-goroutine
// contention at all.
type sinkMetrics struct {
	enqueued         atomic.Int64
	dropped          atomic.Int64
	delivered        atomic.Int64
	deliveryFailures atomic.Int64
}

func (m *sinkMetrics) addEnqueued()  { m.enqueued.Add(1) }
func (m *sinkMetrics) addDropped()   { m.dropped.Add(1) }
func (m *sinkMetrics) addDelivered() { m.delivered.Add(1) }
func (m *sinkMetrics) addFailure()   { m.deliveryFailures.Add(1) }
func (m *sinkMetrics) snapshot() SinkMetrics {
	return SinkMetrics{
		Enqueued: m.enqueued.Load(), Dropped: m.dropped.Load(),
		Delivered: m.delivered.Load(), DeliveryFailures: m.deliveryFailures.Load(),
	}
}
func (m *sinkMetrics) droppedCount() int64 {
	return m.dropped.Load()
}

// Sink is a single-worker, bounded-queue contextfabric.ClarificationSelectionSink.
// It never creates a goroutine per request or per event -- one background
// worker, started once at construction, drains a bounded channel for the
// life of the Sink (auth.UsageTelemetry's own documented reason: unbounded
// per-request goroutines are themselves a resource-exhaustion hazard under
// load, not just a delivery-ordering nuisance).
type Sink struct {
	db       *sql.DB
	queue    chan contextfabric.ClarificationSelectionEvent
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	// workerCtx/cancelWorker (sol review F6) bound every background
	// INSERT this Sink's worker attempts, for as long as the worker is
	// running. Close cancels it on its OWN timeout (not just on a clean
	// stop) specifically so a worker that is still mid-INSERT when the
	// caller gives up waiting abandons that INSERT immediately, rather
	// than racing whatever the caller does next (in
	// internal/runtime/hosted's case, closing the very Postgres pool this
	// context's query is using).
	workerCtx     context.Context
	cancelWorker  context.CancelFunc
	insertTimeout time.Duration
	logger        *slog.Logger
	metrics       sinkMetrics
	generateID    func() (string, error)
}

// NewSink builds a Sink around a caller-owned *sql.DB and starts its
// background worker. The caller owns db's lifecycle (open/close); Sink
// never closes it. Call Close to stop the worker and drain whatever is
// already queued before the caller closes db out from under it.
func NewSink(db *sql.DB, options SinkOptions) (*Sink, error) {
	if db == nil {
		return nil, errors.New("pgclarification: sink requires a database")
	}
	if options.QueueCapacity == 0 {
		options.QueueCapacity = defaultQueueCapacity
	}
	if options.QueueCapacity < 1 || options.QueueCapacity > maximumQueueCapacity {
		return nil, errors.New("pgclarification: queue capacity is invalid")
	}
	if options.InsertTimeout <= 0 {
		options.InsertTimeout = defaultInsertTimeout
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	sink := &Sink{
		db:            db,
		queue:         make(chan contextfabric.ClarificationSelectionEvent, options.QueueCapacity),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		workerCtx:     workerCtx,
		cancelWorker:  cancelWorker,
		insertTimeout: options.InsertTimeout,
		logger:        options.Logger,
		generateID:    generateUUID,
	}
	go sink.run()
	return sink, nil
}

// RecordSelection implements contextfabric.ClarificationSelectionSink.
// It NEVER blocks and NEVER returns an error to the caller (the interface
// itself has no error return) -- a full queue drops the newest event and
// increments a low-cardinality counter ONLY (sol review F4: this branch
// runs in the CALLER's own goroutine, so it must never touch the logger --
// a blocking slog.Handler on this path would delay a real investigation.
// The worker goroutine, never the caller, is what turns a nonzero drop
// count into a log line -- see run()'s periodic summary below). This is
// the fail-open contract Engine.Investigate depends on: capture must
// never break or delay an investigation.
func (s *Sink) RecordSelection(_ context.Context, event contextfabric.ClarificationSelectionEvent) {
	if s == nil {
		return
	}
	select {
	case s.queue <- event:
		s.metrics.addEnqueued()
	default:
		s.metrics.addDropped()
	}
}

// Metrics returns a point-in-time snapshot of queue/delivery counters.
func (s *Sink) Metrics() SinkMetrics {
	if s == nil {
		return SinkMetrics{}
	}
	return s.metrics.snapshot()
}

// Close stops the background worker and waits for it to drain whatever was
// already queued, or for ctx to expire, whichever comes first. On a
// timeout, Close cancels the worker's own context (sol review F6) so a
// still-running INSERT abandons immediately rather than being left to race
// whatever the caller does next -- callers that own a Runtime-style
// shutdown sequence should still Close the Sink BEFORE closing the
// underlying *sql.DB (mirroring auth.UsageTelemetry's own ordering in
// internal/runtime/hosted), but this cancellation is the second,
// independent line of defense for the case where that ordering is not
// enough on its own. Safe to call more than once.
func (s *Sink) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		s.cancelWorker()
		return nil
	case <-ctx.Done():
		s.cancelWorker()
		return ctx.Err()
	}
}

func (s *Sink) run() {
	defer close(s.done)
	ticker := time.NewTicker(dropSummaryInterval)
	defer ticker.Stop()
	var lastLoggedDropped int64
	for {
		select {
		case event := <-s.queue:
			s.insert(event)
		case <-ticker.C:
			lastLoggedDropped = s.logDroppedSummary(lastLoggedDropped)
		case <-s.stop:
			s.drain()
			s.logDroppedSummary(lastLoggedDropped)
			return
		}
	}
}

// logDroppedSummary is called ONLY from the worker goroutine (run/drain),
// never from RecordSelection's caller path -- see RecordSelection's own
// doc comment for why that split exists. Returns the dropped count it just
// logged against, so the caller can update its own "last reported"
// baseline.
// logDroppedSummary calls s.logger.Warn synchronously, on the WORKER's own
// goroutine, not the caller's -- sol review F4-b, ruled an accepted
// residual rather than something to also make async. The original P2 this
// fixes was RecordSelection logging on the ENGINE path (the goroutine that
// serves an actual investigation), which this Sink no longer does at all;
// that path is now provably clean (see TestRecordSelection_DropsOnFullQueueWithoutBlocking's
// blockingHandler, which would hang the TEST if it ever regressed). A
// pathologically blocking slog.Handler here can only stall THIS worker's
// own loop: run() will not drain the next queued insert, or check the
// ticker again, until this call returns, so drops accumulate in the queue
// (bounded, so callers keep getting the same non-blocking enqueue-or-drop
// behavior) and the periodic summary itself falls behind -- capture
// degrades, but no investigation anywhere is delayed by so much as one
// tick. Making this async too would add a second unbounded queue (log
// messages instead of events) with no caller-latency benefit to justify
// the complexity, so it stays synchronous.
func (s *Sink) logDroppedSummary(lastLogged int64) int64 {
	current := s.metrics.droppedCount()
	if current == lastLogged {
		return lastLogged
	}
	s.logger.Warn("clarification selection capture dropped (periodic summary)",
		"dropped_since_last_summary", current-lastLogged, "dropped_total", current)
	return current
}

// drain flushes whatever is already sitting in the queue at Close time,
// non-blocking once the queue is empty -- Close's own ctx bounds the total
// wait, not this loop.
func (s *Sink) drain() {
	for {
		select {
		case event := <-s.queue:
			s.insert(event)
		default:
			return
		}
	}
}

func (s *Sink) insert(event contextfabric.ClarificationSelectionEvent) {
	ctx, cancel := context.WithTimeout(s.workerCtx, s.insertTimeout)
	defer cancel()
	if err := s.insertContext(ctx, event); err != nil {
		s.metrics.addFailure()
		s.logger.Warn("clarification selection capture failed", "error", err)
		return
	}
	s.metrics.addDelivered()
}

// pipelineContextPayload is the JSON shape persisted into the
// pipeline_context column -- a flat composition of every CHAOS-3833/3862
// reuse-key dimension Engine carried at capture time. Deliberately a
// standalone struct (not contextfabric.ReuseRetrievalIdentity/
// ReusePromptVersions/ReuseVersionAuthorities marshaled as nested objects):
// a flat JSONB shape is what a future ad hoc SQL/BI query over this
// capture-only table can actually index into with `->>'field'` without
// needing to know today's Go struct nesting, which is exactly the kind of
// internal refactor that must not be able to change this table's stored
// shape.
type pipelineContextPayload struct {
	ProjectionVersion           string   `json:"projection_version"`
	ModelIdentities             []string `json:"model_identities"`
	EmbedRetrievalIdentity      string   `json:"embed_retrieval_identity"`
	RetrievalPolicyVersion      string   `json:"retrieval_policy_version"`
	InterpretationPromptVersion string   `json:"interpretation_prompt_version"`
	SynthesisPromptVersion      string   `json:"synthesis_prompt_version"`
	QueryVersion                string   `json:"query_version"`
	CanonicalServiceVersion     string   `json:"canonical_service_version"`
	ModelOutputSchemaVersion    string   `json:"model_output_schema_version"`
}

// knownSelectionProvenanceValues mirrors migration 0016's
// ck_acr_cf_clarification_selections_provenance_vocabulary CHECK exactly
// (sol review F5's closed vocabulary) -- kept here, not imported from
// contextfabric, so this package's own validation does not silently drift
// from what the DATABASE actually enforces even if contextfabric's own
// constant set ever changes shape.
var knownSelectionProvenanceValues = map[string]struct{}{
	"web_assertion":        {},
	"credential_mcp":       {},
	"credential_workbench": {},
	"credential_other":     {},
}

// validateEvent fails closed on a malformed event -- constructing a
// well-formed contextfabric.ClarificationSelectionEvent is Engine's
// responsibility (captureClarificationSelection always supplies every
// field), but this sink must never let a construction bug reach Postgres as
// a confusing CHECK-constraint error when a clear, named validation error
// says the same thing better.
func validateEvent(event contextfabric.ClarificationSelectionEvent) error {
	for name, value := range map[string]string{
		"org_id": event.OrgID, "question_hash": event.QuestionHash,
		"prior_result_id": event.PriorResultID, "selected_receipt_id": event.Selected.ReceiptID,
		"selected_subject_kind": event.Selected.SubjectKind, "selected_subject_canonical_id": event.Selected.SubjectCanonicalID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("pgclarification: %s is required", name)
		}
	}
	if len(event.QuestionHash) != 64 {
		return errors.New("pgclarification: question_hash must be a 64-character hex digest")
	}
	if event.CapturedAt.IsZero() {
		return errors.New("pgclarification: captured_at is required")
	}
	if _, known := knownSelectionProvenanceValues[event.SelectionProvenance]; !known {
		return fmt.Errorf("pgclarification: selection_provenance %q is not in the closed vocabulary", event.SelectionProvenance)
	}
	return nil
}

func (s *Sink) insertContext(ctx context.Context, event contextfabric.ClarificationSelectionEvent) error {
	if err := validateEvent(event); err != nil {
		return err
	}
	selectionID, err := s.generateID()
	if err != nil {
		return fmt.Errorf("pgclarification: generate selection id: %w", err)
	}
	offeredJSON, err := json.Marshal(event.OfferedCandidates)
	if err != nil {
		return fmt.Errorf("pgclarification: marshal offered candidates: %w", err)
	}
	pipelineJSON, err := json.Marshal(pipelineContextPayload{
		ProjectionVersion: event.ProjectionVersion, ModelIdentities: event.ModelIdentities,
		EmbedRetrievalIdentity: event.RetrievalIdentity.EmbedRetrievalIdentity, RetrievalPolicyVersion: event.RetrievalIdentity.RetrievalPolicyVersion,
		InterpretationPromptVersion: event.PromptVersions.InterpretationPromptVersion, SynthesisPromptVersion: event.PromptVersions.SynthesisPromptVersion,
		QueryVersion: event.VersionAuthorities.QueryVersion, CanonicalServiceVersion: event.VersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion: event.VersionAuthorities.ModelOutputSchemaVersion,
	})
	if err != nil {
		return fmt.Errorf("pgclarification: marshal pipeline context: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_clarification_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, selected_receipt_id, selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		selectionID, event.OrgID, event.CapturedAt, event.QuestionHash, event.PriorResultID,
		event.Selected.ReceiptID, event.Selected.SubjectKind, event.Selected.SubjectCanonicalID,
		event.SelectionProvenance, offeredJSON, pipelineJSON)
	if err != nil {
		return fmt.Errorf("pgclarification: insert selection: %w", err)
	}
	return nil
}

// generateUUID mirrors internal/contextfabric/pgmodelreceipts' own
// generateUUID (and internal/storage/postgres/audit.go's) exactly -- this
// repo's established idiom for an application-generated primary key
// (crypto/rand, RFC 4122 version/variant bits set, hex-formatted), not a
// shared helper: this table's own package owns its own copy, the same way
// those two already independently do.
func generateUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
