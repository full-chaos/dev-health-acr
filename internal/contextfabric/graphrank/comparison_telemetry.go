package graphrank

// THE OBSERVABLE FOR TURN-1 COMPARISON RESOLUTION.
//
// A BEHAVIOUR CHANGE LANDS WITH ITS OBSERVABLE. This file is not "the
// telemetry step at the end"; it is the operator-visible half of every
// behaviour the comparison work added, and each event below exists because a
// specific regression in a specific behaviour would otherwise be INVISIBLE on
// a running rig.
//
// WHY A NEW SINK RATHER THAN THE EXISTING TRACER. Two independent reasons,
// both structural:
//
//  1. SlogResolutionTracer.Trace builds its OWN context.Background(), so it
//     cannot carry a real request context. Correlating a comparison's lines
//     with the request that produced them is the entire point of these events,
//     and a background context makes that impossible rather than merely
//     inconvenient.
//  2. It emits at DEBUG. Twenty-six of its cases are DebugContext, so on a
//     production rig -- where debug is off -- none of it exists. An observable
//     nobody can read is not an observable.
//
// This sink takes a context and emits at INFO, which is the level a bisect on
// the rig actually reads.
//
// CONTENT SAFETY BY CONSTRUCTION, the same discipline the tracer documents:
// every field below is a closed token, a count, a bool, or a subject kind.
// No term text, no question text, no label, no model output. A reader who
// needs the answer itself correlates by request_id; nothing here leaks the
// corpus into a log stream.
//
// AND A CLOSED VOCABULARY NEVER NAMES A COMBINATION. An ambiguous operand
// beside a resolved one emits TWO slot lines and ONE decision line -- never a
// single "partially_resolved" token. A combination token cannot be counted,
// and the moment one exists every new pairing needs another.

import (
	"context"
	"log/slog"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// ComparisonPolicyEvent is emitted ONCE per admitted comparison, at dispatch.
//
// THE REGRESSION IT MAKES VISIBLE: a question silently ceasing to be admitted
// as a comparison. Interpreter drift or a classifier change would send it back
// down the flat pooled path -- which is a LEGAL outcome that serves a
// perfectly well-formed answer -- so nothing else on the document would move.
// Without this line the fix would quietly stop applying and the only symptom
// would be the return of the original defect.
//
// It also carries the two SUPPRESSIONS that implement the no-read hold, for
// the same reason: if the exclusions lapsed, a comparison would begin reading
// during resolution and still serve a correct-looking answer.
type ComparisonPolicyEvent struct {
	RequestID                string
	OrgID                    string
	Admission                contextfabric.ComparisonAdmission
	SlotCount                int
	QuestionSearchSuppressed bool
	EvidenceCensusSuppressed bool
	CandidateBudget          int
}

// OperandSlotEvent is emitted ONCE PER OPERAND.
//
// THE REGRESSION IT MAKES VISIBLE: slot term isolation breaking. The published
// candidate list is the MERGED one, so a slot that started seeing the other
// operand's candidates would look identical on the served document -- the same
// subjects, the same count. Per-slot candidate counts are the only place that
// shows, and they are the direct observable for the defect this whole change
// exists to fix.
type OperandSlotEvent struct {
	RequestID         string
	OrgID             string
	SlotPosition      int
	SlotKind          contextfabric.SubjectKind
	TermCount         int
	CandidateCount    int
	CommittedCount    int
	Outcome           operandSlotState
	ReceiptBound      bool
	RetrievalDegraded bool
}

// ComparisonReceiptBindingEvent is emitted ONCE when a comparison carried
// selections.
//
// THE REGRESSION IT MAKES VISIBLE: a selection binding the WRONG operand, or
// binding at all where it should have stayed unbound. The served document is
// identical in both cases -- a committed pair is a committed pair -- so the
// binding decision is unobservable from the outside. Bound slot position is
// what distinguishes "answered operand one" from "answered operand two", and
// the unbound count is what distinguishes a refusal from a guess.
type ComparisonReceiptBindingEvent struct {
	RequestID          string
	OrgID              string
	ReceiptsConsidered int
	BoundCount         int
	UnboundCount       int
	BoundSlotPositions []int
}

// ComparisonDecisionEvent is emitted ONCE, at publication.
//
// THE REGRESSION IT MAKES VISIBLE: a hold that stops being a hold, or a hold
// whose terminal status silently reverts to no-match. The user-visible
// difference between those is a MISSING COMPLETION ACTION -- the shared
// projection drops the clarification unless the status is the
// clarification-required one -- and no served field distinguishes that from an
// ordinary no-match. This line is where the difference is legible.
//
// It also correlates with the engine's existing commit-affirmation retraction
// warning: `published_committed=2` here beside one retraction line for the
// same request_id IS a half-published comparison, which neither line shows
// alone. That correlation is deliberate rather than a duplicated field --
// re-emitting the retraction here would create a second authority for a fact
// the affirmation gate already owns.
type ComparisonDecisionEvent struct {
	RequestID          string
	OrgID              string
	Decision           string
	PublishedCommitted int
	UnboundReceipts    int
	RetrievalDegraded  bool
}

// Closed decision vocabulary. Two members, and no third naming a combination.
const (
	comparisonDecisionPublished = "published"
	comparisonDecisionHeld      = "held"
)

// OperandResolutionSink records comparison resolution's operator-visible
// decisions. EVERY METHOD TAKES A CONTEXT -- that is the point of the type.
type OperandResolutionSink interface {
	RecordComparisonPolicy(ctx context.Context, event ComparisonPolicyEvent)
	RecordOperandSlot(ctx context.Context, event OperandSlotEvent)
	RecordComparisonReceiptBinding(ctx context.Context, event ComparisonReceiptBindingEvent)
	RecordComparisonDecision(ctx context.Context, event ComparisonDecisionEvent)
}

// THE FIELD-TO-LOG-KEY MAPPING, DECLARED ONCE.
//
// These four maps are the single authority for which struct field becomes
// which log key. The reflection test enumerates every field of every event
// struct against them and rejects a missing mapping, a duplicate key, or a key
// with no field -- so a field added to an event without a key is a test
// failure rather than a value that silently never reaches the log.
var (
	comparisonPolicyLogKeys = map[string]string{
		"RequestID": "request_id", "OrgID": "org_id",
		"Admission": "admission", "SlotCount": "slot_count",
		"QuestionSearchSuppressed": "question_search_suppressed",
		"EvidenceCensusSuppressed": "evidence_census_suppressed",
		"CandidateBudget":          "candidate_budget",
	}
	operandSlotLogKeys = map[string]string{
		"RequestID": "request_id", "OrgID": "org_id",
		"SlotPosition": "slot_position", "SlotKind": "slot_kind",
		"TermCount": "term_count", "CandidateCount": "candidate_count",
		"CommittedCount": "committed_count", "Outcome": "outcome",
		"ReceiptBound": "receipt_bound", "RetrievalDegraded": "retrieval_degraded",
	}
	comparisonReceiptBindingLogKeys = map[string]string{
		"RequestID": "request_id", "OrgID": "org_id",
		"ReceiptsConsidered": "receipts_considered", "BoundCount": "bound_count",
		"UnboundCount": "unbound_count", "BoundSlotPositions": "bound_slot_positions",
	}
	comparisonDecisionLogKeys = map[string]string{
		"RequestID": "request_id", "OrgID": "org_id",
		"Decision": "decision", "PublishedCommitted": "published_committed",
		"UnboundReceipts": "unbound_receipts", "RetrievalDegraded": "retrieval_degraded",
	}
)

// SlogOperandResolutionSink is the production sink: one Info line per event,
// on the CALLER'S context, with a nil logger falling back to slog.Default() --
// the same convention SlogEngineTelemetry and SlogResolutionTracer both use.
type SlogOperandResolutionSink struct {
	logger *slog.Logger
}

// NewSlogOperandResolutionSink builds the production sink.
func NewSlogOperandResolutionSink(logger *slog.Logger) SlogOperandResolutionSink {
	if logger == nil {
		logger = slog.Default()
	}
	return SlogOperandResolutionSink{logger: logger}
}

func (s SlogOperandResolutionSink) RecordComparisonPolicy(ctx context.Context, event ComparisonPolicyEvent) {
	s.logger.InfoContext(ctx, "context fabric comparison resolution policy",
		comparisonPolicyLogKeys["RequestID"], contextfabric.SanitizeLogAttr(event.RequestID),
		comparisonPolicyLogKeys["OrgID"], contextfabric.SanitizeLogAttr(event.OrgID),
		comparisonPolicyLogKeys["Admission"], contextfabric.SanitizeLogAttr(string(event.Admission)),
		comparisonPolicyLogKeys["SlotCount"], event.SlotCount,
		comparisonPolicyLogKeys["QuestionSearchSuppressed"], event.QuestionSearchSuppressed,
		comparisonPolicyLogKeys["EvidenceCensusSuppressed"], event.EvidenceCensusSuppressed,
		comparisonPolicyLogKeys["CandidateBudget"], event.CandidateBudget)
}

func (s SlogOperandResolutionSink) RecordOperandSlot(ctx context.Context, event OperandSlotEvent) {
	s.logger.InfoContext(ctx, "context fabric operand slot resolution",
		operandSlotLogKeys["RequestID"], contextfabric.SanitizeLogAttr(event.RequestID),
		operandSlotLogKeys["OrgID"], contextfabric.SanitizeLogAttr(event.OrgID),
		operandSlotLogKeys["SlotPosition"], event.SlotPosition,
		operandSlotLogKeys["SlotKind"], contextfabric.SanitizeLogAttr(string(event.SlotKind)),
		operandSlotLogKeys["TermCount"], event.TermCount,
		operandSlotLogKeys["CandidateCount"], event.CandidateCount,
		operandSlotLogKeys["CommittedCount"], event.CommittedCount,
		operandSlotLogKeys["Outcome"], contextfabric.SanitizeLogAttr(string(event.Outcome)),
		operandSlotLogKeys["ReceiptBound"], event.ReceiptBound,
		operandSlotLogKeys["RetrievalDegraded"], event.RetrievalDegraded)
}

func (s SlogOperandResolutionSink) RecordComparisonReceiptBinding(ctx context.Context, event ComparisonReceiptBindingEvent) {
	s.logger.InfoContext(ctx, "context fabric comparison receipt binding",
		comparisonReceiptBindingLogKeys["RequestID"], contextfabric.SanitizeLogAttr(event.RequestID),
		comparisonReceiptBindingLogKeys["OrgID"], contextfabric.SanitizeLogAttr(event.OrgID),
		comparisonReceiptBindingLogKeys["ReceiptsConsidered"], event.ReceiptsConsidered,
		comparisonReceiptBindingLogKeys["BoundCount"], event.BoundCount,
		comparisonReceiptBindingLogKeys["UnboundCount"], event.UnboundCount,
		comparisonReceiptBindingLogKeys["BoundSlotPositions"], event.BoundSlotPositions)
}

func (s SlogOperandResolutionSink) RecordComparisonDecision(ctx context.Context, event ComparisonDecisionEvent) {
	s.logger.InfoContext(ctx, "context fabric comparison decision",
		comparisonDecisionLogKeys["RequestID"], contextfabric.SanitizeLogAttr(event.RequestID),
		comparisonDecisionLogKeys["OrgID"], contextfabric.SanitizeLogAttr(event.OrgID),
		comparisonDecisionLogKeys["Decision"], contextfabric.SanitizeLogAttr(string(event.Decision)),
		comparisonDecisionLogKeys["PublishedCommitted"], event.PublishedCommitted,
		comparisonDecisionLogKeys["UnboundReceipts"], event.UnboundReceipts,
		comparisonDecisionLogKeys["RetrievalDegraded"], event.RetrievalDegraded)
}
