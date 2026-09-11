// Package eventspec is the ONE canonical declaration authority for every
// production decision-event line this repository emits. CHAOS-5515 (thread
// C of the CHAOS-5513 observability contract) establishes it with the first
// two piloted events; later tickets in the same chain (CHAOS-5516..5520)
// register the rest of the emission population here as each seam migrates.
// Nothing outside this file may declare a second, competing list of a line's
// keys, types, or vocabulary -- a producer's own doc comment MAY explain WHY
// a field exists, but the shape it emits is owned here.
//
// This package declares; internal/contextfabric/eventspec/certify asserts
// against production output; the zz_generated.go / schema.json artefacts in
// this directory are DERIVED from the declarations below by Generate() and
// are never hand-edited (regen_test.go pins that byte-for-byte).
package eventspec

//go:generate go run ./gen

// Level is the production slog level a variant is required to emit at.
type Level string

const (
	LevelInfo  Level = "info"
	LevelDebug Level = "debug"
)

// Multiplicity states how many lines of a variant a single scoped pass may
// produce. It is part of the declaration, not a convention left to the
// producer, because "how many lines" is exactly what a collection-proof leg
// (A5) has to check an unexplained deficit against.
type Multiplicity string

const (
	// MultiplicityExactlyOnePerPass: every pass that reaches this variant's
	// trigger produces exactly one line. A pass that reaches the trigger and
	// produces zero, or more than one, is a defect in the producer, not a
	// legitimate zero.
	MultiplicityExactlyOnePerPass Multiplicity = "exactly_one_per_pass"
	// MultiplicityZeroOrOnePerPass: a pass produces at most one line, and its
	// absence is a measured "did not apply" only when the sibling summary
	// line the variant is bounded against explicitly says so (see each
	// event's BoundedAggregation).
	MultiplicityZeroOrOnePerPass Multiplicity = "zero_or_one_per_pass"
	// MultiplicityExactlyOnePerRequest (r3 fix): exactly one line per
	// resolveSubjects CALL, never per internal pass -- the event has NO
	// "pass" field at all, because the concept does not apply to it (it
	// folds every pass the call ran into one line, emitted once after the
	// call returns). Round r2's own finding: DecisionSummary was declared
	// MultiplicityExactlyOnePerPass while actually being per-REQUEST with
	// no pass field, and an executable consistency check (certify's own
	// TestEveryEventsMultiplicityAgreesWithWhetherItDeclaresAPassField)
	// failed against that mismatch. This is the pass-less member the two
	// pass-bearing ones were missing: MultiplicityExactlyOnePerPass and
	// MultiplicityZeroOrOnePerPass now REQUIRE a declared "pass" field
	// (enforced by the same consistency check), and this one requires the
	// opposite.
	MultiplicityExactlyOnePerRequest Multiplicity = "exactly_one_per_request"
)

// FieldPresence states whether a field is written on every line of its
// variant (explicit zero, never omitted) or only under a declared, testable
// applicability condition. There is no third state: absence must never
// substitute for a measured zero (clause 3).
type FieldPresence string

const (
	// PresenceRequired: present on every line, every pass, with an explicit
	// zero/empty value when nothing applies. A field marked required that a
	// producer omits is a defect, not a legitimate absence.
	PresenceRequired FieldPresence = "required"
	// PresenceConditional: present only when Field.Applicability holds; the
	// condition is stated in prose on the field and is checked by the
	// producer's own tests, not inferred from absence.
	PresenceConditional FieldPresence = "conditional"
)

// FieldType is the JSON shape a field's value takes on the emitted line.
type FieldType string

const (
	FieldString      FieldType = "string"
	FieldInt         FieldType = "int"
	FieldStringSlice FieldType = "string_slice"
	FieldObjectSlice FieldType = "object_slice"
	// FieldBool (CHAOS-5516): PR1's two piloted events had no native
	// boolean field, so this type did not exist until decision_summary's
	// own migration needed one (offered_under_window_gate,
	// offer_pool_emptied_by_exclusion).
	FieldBool FieldType = "bool"
)

// Field is one key on one event variant's emitted line.
type Field struct {
	// Key is the JSON key exactly as slog writes it (the log line's own
	// vocabulary -- this package does not rename what production emits).
	Key      string
	Type     FieldType
	Presence FieldPresence
	// ClosedVocabulary lists every value this field may take, when the field
	// is a closed vocabulary. Nil means the field's value space is open
	// (e.g. a subject-kind token drawn from contextfabric.SubjectKind, or a
	// free identifier) and is NOT asserted exhaustively by this package.
	ClosedVocabulary []string
	// Applicability documents, in one sentence, the testable condition under
	// which a PresenceConditional field is written. Empty for a required
	// field.
	Applicability string
	// Fields describes an object_slice field's own per-element shape. Empty
	// for every other Type.
	Fields []Field
}

// Event is one canonical, named production log line: its identity, every
// variant's field set, its production level, how many lines one scoped pass
// may produce, which fields jointly attribute a line to its owning scope and
// attempt, and how its own volume stays bounded.
type Event struct {
	// ID is this event's stable identity in the specification, independent
	// of the msg string a future PR may need to change.
	ID string
	// Msg is the exact slog msg this variant is emitted under today -- the
	// line's identity on the wire, and what the certification runner (A2)
	// locates a line by.
	Msg          string
	Level        Level
	Multiplicity Multiplicity
	// Attribution lists the field keys that jointly make one emitted line
	// uniquely attributable to the scope and attempt that produced it.
	Attribution []string
	Fields      []Field
	// BoundedAggregation documents, in one sentence, what keeps this
	// variant's own line volume bounded per pass (a fixed cap, "at most one
	// per pass", etc.) -- the aggregation clause 1 requires this
	// specification to own.
	BoundedAggregation string
}

// DeclaredKindRescue* are the FIVE values chaos5388_declared_kind_rescue.go's
// "state" field ships (unchanged by this ticket -- "no schema field names or
// additional outcome tokens are minted by this amendment"). These are the
// ONE declaration of that vocabulary's string values; graphrank's own
// declaredKindRescue* constants reference these rather than retyping the
// literals a second time (round r2's P1: "ran_matched_survived" was
// omitted from a hand-typed second copy of this list -- the real resolver
// path emits it, so it certified successfully against an incomplete
// vocabulary once nested validation was added; a second, independently
// typed list is exactly how the two drifted. Centralizing removes the
// class of bug, not just this one instance of it).
const (
	DeclaredKindRescueNotRun             = "not_run"
	DeclaredKindRescueMatchedZero        = "ran_matched_zero"
	DeclaredKindRescueMatchedThenDropped = "ran_matched_then_dropped"
	DeclaredKindRescueMatchedThenCut     = "ran_matched_then_cut"
	DeclaredKindRescueMatchedSurvived    = "ran_matched_survived"
)

// declaredKindRescueState is the CLOSED vocabulary for the five constants
// above, in the same order chaos5388_declared_kind_rescue.go declares them.
var declaredKindRescueState = []string{
	DeclaredKindRescueNotRun,
	DeclaredKindRescueMatchedZero,
	DeclaredKindRescueMatchedThenDropped,
	DeclaredKindRescueMatchedThenCut,
	DeclaredKindRescueMatchedSurvived,
}

// RankedCutSummary is the once-per-pass Info line
// (graphrank/tracer.go, case "ranked_cut" with event.RankedCutSummary==true)
// that reports phase 4's ranked-cut decision for a resolution pass, including
// the CHAOS-5434 anchor-slot decision and the CHAOS-5388 declared-kind-rescue
// disclosure. Multiplicity is per PASS, not per request: a multi-pass
// resolution's LAST summary reaching the tracer is the one describing the
// pass whose resolution was actually returned (tracer.go's own doc comment).
var RankedCutSummary = Event{
	ID:                 "graphrank.ranked_cut_summary",
	Msg:                "context fabric resolution trace: ranked cut summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per ranked-cut pass -- the per-candidate detail this summary aggregates stays at Debug (case \"ranked_cut\", RankedCutSummary==false).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		// pass (CHAOS-5516): which finalization of the owning resolution
		// this line's own pass is, 1-based, in the order they ran -- the
		// same field, same meaning, thread D's cover events (#496) carry.
		// certify's exactly_one_per_pass guard keys duplicate detection on
		// (request_id, pass) rather than byte-identical-except-time (PR1
		// RISK-NOTES' documented limit): a second line for the SAME pass is
		// always a defect; a second line for a DIFFERENT pass is always a
		// legitimate re-decision, regardless of whether every other field
		// happens to coincide.
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"ranked_cut"}},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "survived_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "survived_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "max", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "anchor_slot_reserved", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or the
			// explicit "none" token (anchorSlotNone) when nothing was
			// reserved on this pass. Every pass writes one of the two --
			// never omitted.
		},
		{Key: "anchor_slot_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"receipt", "confirmed_anchor", "none"}},
		{Key: "anchor_slot_displaced", Type: FieldInt, Presence: PresenceRequired},
		{Key: "pool_truncated_n", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "declared_kind_rescue", Type: FieldObjectSlice, Presence: PresenceRequired,
			// Always present, empty list when this pass's frame/receipt
			// declared no kind requiring the rescue arm.
			Fields: []Field{
				{Key: "kind", Type: FieldString, Presence: PresenceRequired},
				{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: declaredKindRescueState},
				{Key: "terms_queried", Type: FieldInt, Presence: PresenceRequired},
				{Key: "matched", Type: FieldInt, Presence: PresenceRequired},
				{Key: "survived", Type: FieldInt, Presence: PresenceRequired},
				{Key: "reached", Type: FieldInt, Presence: PresenceRequired},
			},
		},
	},
}

// AnchorSlotDisplaced is the Info line (graphrank/tracer.go, case
// "anchor_slot_displaced") naming the specific candidate the CHAOS-5434
// reserved slot evicted. It is a SECOND event on the same pass as
// RankedCutSummary, not a replacement for it: RankedCutSummary's own
// anchor_slot_displaced count is the bounded-aggregation field this line's
// presence is checked against (its absence on a pass whose summary reports
// anchor_slot_displaced==0 is the pass's measured zero, not a missing
// measurement).
var AnchorSlotDisplaced = Event{
	ID:                 "graphrank.anchor_slot_displaced",
	Msg:                "context fabric resolution trace: anchor slot displaced",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "present iff this pass's RankedCutSummary line reports anchor_slot_displaced > 0 -- the two lines' counts must agree; today at most one candidate can be displaced per pass (kindReserveSlotsPerKind), so at most one line.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		// pass (CHAOS-5516): the same pass this line's own RankedCutSummary
		// carries -- see that event's own doc comment on this field.
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_slot_displaced"}},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_slot_reserved", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_slot_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"receipt", "confirmed_anchor", "none"}},
		{Key: "anchor_slot_displaced", Type: FieldInt, Presence: PresenceRequired},
		{Key: "pool_truncated_n", Type: FieldInt, Presence: PresenceRequired},
	},
}

// DecisionSummary is the once-per-REQUEST Info line (graphrank/tracer.go,
// case "decision_summary") that decisionSummaryBuffer.flush() emits after
// resolveSubjects returns -- the ONE line that says what the resolver
// DECIDED, folding every decision-stage event and the offer_pool/anchor_pool
// summaries of the whole call, always (even when it counted nothing:
// dictation 378's explicit-zero rule). Unlike RankedCutSummary/
// AnchorSlotDisplaced, its own multiplicity is per REQUEST, not per internal
// pass -- decisionSummaryBuffer accumulates across every pass
// resolveSubjects runs and flushes exactly once, so it carries no `pass`
// field of its own.
//
// CHAOS-5516 (clauses 2+4): this is the pilot's typed-construction scope.
// eventspec/gen generates a DecisionSummaryFields struct + SlogArgs() method
// from these Fields (zz_generated.go) -- decisionSummaryBuffer.flush()
// constructs one instead of a hand-typed ResolutionTraceEvent composite
// literal for this event's own fields, and tracer.go's "decision_summary"
// case emits via the generated SlogArgs() instead of a second, independently
// hand-typed key list. Three lists (spec.go's Fields, the buffer's struct
// literal, tracer.go's key strings) become one generated source with two
// consumers.
//
// r3 FIX: declared MultiplicityExactlyOnePerRequest, not
// MultiplicityExactlyOnePerPass -- round r2 found the label lying about the
// shape (an executable consistency check failed: this event has no `pass`
// field, and MultiplicityExactlyOnePerPass now means "per PASS", which
// requires one). ExactlyOnePerRequest is the pass-less member: certify's
// own scoping (Attribution: request_id) already IS the whole scope for
// this event, so it needs no per-pass grouping at all -- more than one line
// in the request's own scope is unconditionally a defect, exactly the
// behavior this event always had; only the DECLARED label changes.
var DecisionSummary = Event{
	ID:                 "graphrank.decision_summary",
	Msg:                "context fabric resolution trace: decision summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call, emitted from decisionSummaryBuffer.flush() unconditionally (including a zero count) -- the per-candidate decision events this line folds stay at Debug (case \"decision\").",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"decision_summary"}},
		{Key: "decision_event_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "committed_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "ambiguous_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "no_commit_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "committed_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "commit_gates", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "commit_bases", Type: FieldStringSlice, Presence: PresenceRequired},
		{
			Key: "offered_under_window_gate", Type: FieldBool, Presence: PresenceRequired,
			// OR across the call: true if AT LEAST ONE decision-stage event
			// this line folds was offers-only (offersOnlyDecisionTracer).
		},
		{
			Key: "frame_gate", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: stamped at construction from the ordering
			// verdict the call carried in (CommitGatePolicy), decided
			// BEFORE this call, never re-derived here.
		},
		{
			Key: "refuse_basis", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: the frame's own refusal-reason token, an
			// input to the call like frame_gate above.
		},
		{Key: "offer_pool_vector_only_excluded", Type: FieldInt, Presence: PresenceRequired},
		{Key: "offer_pool_vector_only_demoted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "offer_pool_emptied_by_exclusion", Type: FieldBool, Presence: PresenceRequired},
		{Key: "offer_pool_anchor_kind_withheld", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "offer_pool_anchor_kind_withheld_scope", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "none".
		},
		{
			Key: "offer_pool_anchor_kind_withheld_reason", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a reason token, or "none".
		},
		{Key: "offer_pool_anchor_kind_withheld_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "offer_pool_anchor_kind_exempted", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "anchor_pool_kind_scope", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "none".
		},
		{
			Key: "anchor_pool_kind_scope_source", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: how the scope was decided (receipt,
			// confirmed_anchor, none, or another producer-defined token).
		},
		{
			Key: "member_kind_confirmed", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "none".
		},
		{Key: "reserved_kinds", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "filter_kinds", Type: FieldStringSlice, Presence: PresenceRequired},
	},
}

// All is every event this specification declares. Generate() and the
// certification runner both range over exactly this slice -- neither
// maintains a second list.
var All = []Event{RankedCutSummary, AnchorSlotDisplaced, DecisionSummary}
