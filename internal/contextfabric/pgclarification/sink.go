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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
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
	// HTTP/MCP request may already have returned. Using context.Background()
	// plus this fixed timeout is what makes this genuinely fire-and-forget
	// rather than accidentally tying delivery to a caller who has already
	// gone away.
	defaultInsertTimeout = 5 * time.Second
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

type sinkMetrics struct {
	enqueued         int64
	dropped          int64
	delivered        int64
	deliveryFailures int64
	mu               sync.Mutex
}

func (m *sinkMetrics) addEnqueued()  { m.mu.Lock(); m.enqueued++; m.mu.Unlock() }
func (m *sinkMetrics) addDropped()   { m.mu.Lock(); m.dropped++; m.mu.Unlock() }
func (m *sinkMetrics) addDelivered() { m.mu.Lock(); m.delivered++; m.mu.Unlock() }
func (m *sinkMetrics) addFailure()   { m.mu.Lock(); m.deliveryFailures++; m.mu.Unlock() }
func (m *sinkMetrics) snapshot() SinkMetrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	return SinkMetrics{Enqueued: m.enqueued, Dropped: m.dropped, Delivered: m.delivered, DeliveryFailures: m.deliveryFailures}
}

// Sink is a single-worker, bounded-queue contextfabric.ClarificationSelectionSink.
// It never creates a goroutine per request or per event -- one background
// worker, started once at construction, drains a bounded channel for the
// life of the Sink (auth.UsageTelemetry's own documented reason: unbounded
// per-request goroutines are themselves a resource-exhaustion hazard under
// load, not just a delivery-ordering nuisance).
type Sink struct {
	db            *sql.DB
	queue         chan contextfabric.ClarificationSelectionEvent
	stop          chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
	insertTimeout time.Duration
	logger        *slog.Logger
	metrics       sinkMetrics
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
	sink := &Sink{
		db:            db,
		queue:         make(chan contextfabric.ClarificationSelectionEvent, options.QueueCapacity),
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		insertTimeout: options.InsertTimeout,
		logger:        options.Logger,
	}
	go sink.run()
	return sink, nil
}

// RecordSelection implements contextfabric.ClarificationSelectionSink.
// It NEVER blocks and NEVER returns an error to the caller (the interface
// itself has no error return) -- a full queue drops the newest event and
// logs a low-cardinality warning, exactly like
// auth.UsageTelemetry.Enqueue's own documented "intentionally lossy" queue-
// full behavior. This is the fail-open contract Engine.Investigate depends
// on: capture must never break or delay an investigation.
func (s *Sink) RecordSelection(_ context.Context, event contextfabric.ClarificationSelectionEvent) {
	if s == nil {
		return
	}
	select {
	case s.queue <- event:
		s.metrics.addEnqueued()
	default:
		s.metrics.addDropped()
		s.logger.Warn("clarification selection capture dropped", "reason", "queue_full")
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
// already queued, or for ctx to expire, whichever comes first. Safe to call
// more than once. Callers that own a Runtime-style shutdown sequence should
// Close the Sink BEFORE closing the underlying *sql.DB, mirroring
// auth.UsageTelemetry's own ordering in internal/runtime/hosted -- a worker
// still draining when the pool closes underneath it would just add
// DeliveryFailures for events that were never going to land anyway, but
// closing in the right order avoids that entirely.
func (s *Sink) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Sink) run() {
	defer close(s.done)
	for {
		select {
		case event := <-s.queue:
			s.insert(event)
		case <-s.stop:
			s.drain()
			return
		}
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), s.insertTimeout)
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
	return nil
}

func (s *Sink) insertContext(ctx context.Context, event contextfabric.ClarificationSelectionEvent) error {
	if err := validateEvent(event); err != nil {
		return err
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
	provenance := event.SelectionProvenance
	if provenance == "" {
		provenance = "unknown"
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_clarification_selections
    (org_id, captured_at, question_hash, prior_result_id, selected_receipt_id, selected_subject_kind, selected_subject_canonical_id, selection_provenance, offered_candidates, pipeline_context)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		event.OrgID, event.CapturedAt, event.QuestionHash, event.PriorResultID,
		event.Selected.ReceiptID, event.Selected.SubjectKind, event.Selected.SubjectCanonicalID,
		provenance, offeredJSON, pipelineJSON)
	if err != nil {
		return fmt.Errorf("pgclarification: insert selection: %w", err)
	}
	return nil
}
