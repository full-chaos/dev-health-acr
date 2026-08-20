// Package pgstructureselection is the production Postgres implementation
// of contextfabric.StructureSelectionSink (CHAOS-3927 P4, capture-only
// phase): it persists structure-offer selection events to
// acr.context_fabric_structure_selections (migration 0024) and nothing
// else -- no read path, no feedback into ranking, offers, or priors. That
// table is org-scoped exactly like acr.context_fabric_investigation_results;
// a future read path MUST filter by org_id the same way every other
// Context Fabric store call does (internal/storage/AGENTS.md's
// convention).
//
// This package deliberately mirrors internal/contextfabric/pgclarification
// almost verbatim (same bounded-queue/single-worker/fail-open shape) rather
// than extending that package's Sink to a second event type: this
// codebase's own established convention (pgclarification.generateUUID's
// doc comment, pginvestigation vs memoryinvestigation's parallel-not-shared
// precedent) is that two capture sinks with independent tables, independent
// CHECK-constraint vocabularies, and independent failure/backpressure
// profiles stay two packages, so a change to one's queue/worker tuning can
// never silently alter the other's.
package pgstructureselection

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
	// defaultQueueCapacity mirrors pgclarification's own default exactly --
	// same low expected rate (one per confirmed structure member, not one
	// per request), same reasoning.
	defaultQueueCapacity = 256
	maximumQueueCapacity = 4096
	defaultInsertTimeout = 5 * time.Second
	dropSummaryInterval  = 30 * time.Second
)

// SinkOptions configures Sink. Every field has a sane default; the zero
// value is a fully usable configuration.
type SinkOptions struct {
	QueueCapacity int
	InsertTimeout time.Duration
	Logger        *slog.Logger
}

// SinkMetrics exposes low-cardinality counters for queue saturation and
// delivery health, mirroring pgclarification.SinkMetrics exactly.
type SinkMetrics struct {
	Enqueued         int64
	Dropped          int64
	Delivered        int64
	DeliveryFailures int64
}

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

// Sink is a single-worker, bounded-queue contextfabric.StructureSelectionSink.
// See pgclarification.Sink's own doc comment for the full rationale this
// mirrors field-for-field and method-for-method.
type Sink struct {
	db            *sql.DB
	queue         chan contextfabric.StructureSelectionEvent
	stop          chan struct{}
	done          chan struct{}
	stopOnce      sync.Once
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
		return nil, errors.New("pgstructureselection: sink requires a database")
	}
	if options.QueueCapacity == 0 {
		options.QueueCapacity = defaultQueueCapacity
	}
	if options.QueueCapacity < 1 || options.QueueCapacity > maximumQueueCapacity {
		return nil, errors.New("pgstructureselection: queue capacity is invalid")
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
		queue:         make(chan contextfabric.StructureSelectionEvent, options.QueueCapacity),
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

// RecordSelection implements contextfabric.StructureSelectionSink. It
// NEVER blocks and NEVER returns an error to the caller -- a full queue
// drops the newest event and increments a low-cardinality counter ONLY,
// mirroring pgclarification.Sink.RecordSelection's own fail-open contract
// (that method's own doc comment covers the caller-goroutine-never-logs
// reasoning this reuses verbatim).
func (s *Sink) RecordSelection(_ context.Context, event contextfabric.StructureSelectionEvent) {
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
// already queued, or for ctx to expire, whichever comes first -- mirrors
// pgclarification.Sink.Close exactly, including the same
// cancel-worker-on-timeout second line of defense.
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

func (s *Sink) logDroppedSummary(lastLogged int64) int64 {
	current := s.metrics.droppedCount()
	if current == lastLogged {
		return lastLogged
	}
	s.logger.Warn("structure selection capture dropped (periodic summary)",
		"dropped_since_last_summary", current-lastLogged, "dropped_total", current)
	return current
}

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

func (s *Sink) insert(event contextfabric.StructureSelectionEvent) {
	ctx, cancel := context.WithTimeout(s.workerCtx, s.insertTimeout)
	defer cancel()
	if err := s.insertContext(ctx, event); err != nil {
		s.metrics.addFailure()
		s.logger.Warn("structure selection capture failed", "error", err)
		return
	}
	s.metrics.addDelivered()
}

// pipelineContextPayload mirrors pgclarification's own flat JSONB shape
// exactly (that type's own doc comment covers why flat, not nested).
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

// knownMemberValues mirrors migration 0024's
// ck_acr_cf_structure_selections_member_vocabulary CHECK exactly -- kept
// here, not imported from contextfabric, for the same "own validation
// never silently drifts from what the DATABASE enforces" reasoning
// pgclarification.knownSelectionProvenanceValues documents.
var knownMemberValues = map[string]struct{}{
	"expected_kind":  {},
	"subject_anchor": {},
	"subject_handle": {},
}

// knownSelectionModeValues mirrors migration 0024's
// ck_acr_cf_structure_selections_mode_vocabulary CHECK exactly.
var knownSelectionModeValues = map[string]struct{}{
	"human_panel":         {},
	"agent_receipt":       {},
	"agent_explicit":      {},
	"agent_explicit_echo": {},
}

// knownSelectionProvenanceValues mirrors pgclarification's own vocabulary
// map exactly -- the SAME closed set, the SAME CHECK constraint values,
// duplicated per this package's own no-shared-validation convention (this
// file's own package doc comment).
var knownSelectionProvenanceValues = map[string]struct{}{
	"web_assertion":        {},
	"credential_mcp":       {},
	"credential_workbench": {},
	"credential_other":     {},
}

// validateEvent fails closed on a malformed event -- constructing a
// well-formed contextfabric.StructureSelectionEvent is Engine's own
// responsibility (captureStructureSelection always supplies every field),
// but this sink must never let a construction bug reach Postgres as a
// confusing CHECK-constraint error when a clear, named validation error
// says the same thing better -- mirrors pgclarification.validateEvent's
// own reasoning.
func validateEvent(event contextfabric.StructureSelectionEvent) error {
	for name, value := range map[string]string{
		"org_id": event.OrgID, "question_hash": event.QuestionHash,
		"prior_result_id": event.PriorResultID, "member": event.Member,
		"selected_receipt_id": event.Selected.ReceiptID, "selected_applied_value": event.Selected.AppliedValue,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("pgstructureselection: %s is required", name)
		}
	}
	if len(event.QuestionHash) != 64 {
		return errors.New("pgstructureselection: question_hash must be a 64-character hex digest")
	}
	if event.CapturedAt.IsZero() {
		return errors.New("pgstructureselection: captured_at is required")
	}
	if _, known := knownMemberValues[event.Member]; !known {
		return fmt.Errorf("pgstructureselection: member %q is not in the closed vocabulary", event.Member)
	}
	if _, known := knownSelectionModeValues[event.SelectionMode]; !known {
		return fmt.Errorf("pgstructureselection: selection_mode %q is not in the closed vocabulary", event.SelectionMode)
	}
	if _, known := knownSelectionProvenanceValues[event.SelectionProvenance]; !known {
		return fmt.Errorf("pgstructureselection: selection_provenance %q is not in the closed vocabulary", event.SelectionProvenance)
	}
	return nil
}

func (s *Sink) insertContext(ctx context.Context, event contextfabric.StructureSelectionEvent) error {
	if err := validateEvent(event); err != nil {
		return err
	}
	selectionID, err := s.generateID()
	if err != nil {
		return fmt.Errorf("pgstructureselection: generate selection id: %w", err)
	}
	offeredJSON, err := json.Marshal(event.Offered)
	if err != nil {
		return fmt.Errorf("pgstructureselection: marshal offered options: %w", err)
	}
	pipelineJSON, err := json.Marshal(pipelineContextPayload{
		ProjectionVersion: event.ProjectionVersion, ModelIdentities: event.ModelIdentities,
		EmbedRetrievalIdentity: event.RetrievalIdentity.EmbedRetrievalIdentity, RetrievalPolicyVersion: event.RetrievalIdentity.RetrievalPolicyVersion,
		InterpretationPromptVersion: event.PromptVersions.InterpretationPromptVersion, SynthesisPromptVersion: event.PromptVersions.SynthesisPromptVersion,
		QueryVersion: event.VersionAuthorities.QueryVersion, CanonicalServiceVersion: event.VersionAuthorities.CanonicalServiceVersion,
		ModelOutputSchemaVersion: event.VersionAuthorities.ModelOutputSchemaVersion,
	})
	if err != nil {
		return fmt.Errorf("pgstructureselection: marshal pipeline context: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_structure_selections
    (selection_id, org_id, captured_at, question_hash, prior_result_id, member, selected_receipt_id, selected_applied_value, accepted, selection_mode, selection_provenance, offered, pipeline_context)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		selectionID, event.OrgID, event.CapturedAt, event.QuestionHash, event.PriorResultID, event.Member,
		event.Selected.ReceiptID, event.Selected.AppliedValue, event.Accepted,
		event.SelectionMode, event.SelectionProvenance, offeredJSON, pipelineJSON)
	if err != nil {
		return fmt.Errorf("pgstructureselection: insert selection: %w", err)
	}
	return nil
}

// generateUUID mirrors pgclarification.generateUUID exactly (that
// function's own doc comment covers why this is a per-package copy, not a
// shared helper).
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
