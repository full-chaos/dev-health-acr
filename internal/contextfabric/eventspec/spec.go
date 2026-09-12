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

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

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
	// MultiplicityZeroOrOnePerRequest (CHAOS-5517): the request-scoped
	// sibling of MultiplicityZeroOrOnePerPass -- a line produced at most
	// once per resolveSubjects CALL, gated behind its own trigger
	// condition, with NO "pass" field (the mechanism it discloses runs at
	// most once per call, never once per internal re-decision pass, so a
	// pass identity would be meaningless on it -- the same "no pass
	// concept applies" reasoning MultiplicityExactlyOnePerRequest already
	// carries, just for the zero-or-one shape). CertifyAbsent accepts this
	// multiplicity the same way it accepts ZeroOrOnePerPass, scoped by
	// Attribution alone (never "pass", which this multiplicity forbids).
	MultiplicityZeroOrOnePerRequest Multiplicity = "zero_or_one_per_request"
	// MultiplicityBoundedManyPerPass (CHAOS-5517): a scope (request, or
	// request+pass for an event that also declares "pass") may carry ANY
	// number of lines, 0..N, where N is a cardinality some other part of
	// the system bounds (a term list's length, a retrieval pool's size, a
	// fixed enumerated list) -- clause 1's own "bounded aggregation" is
	// what makes this a specification concern rather than an unbounded
	// free-for-all. The bound is carried ON THE LINES, never only in
	// BoundedAggregation's prose (chris's engineering ruling, 2026-09-11):
	// every event of this multiplicity declares two required int fields,
	// "index" (1-based position within its own scope) and "total" (the
	// scope's own declared cardinality, the SAME value on every line in
	// that scope) -- certify.Certify asserts every line in scope agrees on
	// "total", that "index" covers exactly 1..total with no gap or
	// duplicate, and that the observed line count equals "total". A scope
	// with zero lines needs no such check (there is nothing to disagree)
	// and CERTIFIES trivially -- this multiplicity's whole point is that
	// 0..N are all legitimate shapes, so Certify never refuses an empty
	// scope the way it refuses ExactlyOnePerPass/ExactlyOnePerRequest's
	// own emptiness. Certify.Assertion.Want must additionally carry
	// "index" (which of the scope's own lines is being value-asserted),
	// the same role "pass" plays for a pass-keyed at-most-one event.
	// Whether a BoundedManyPerPass event ALSO declares "pass" is decided
	// PER EVENT (unlike every other Multiplicity value, whose pass-field
	// requirement is fixed): an event emitted once per internal
	// re-decision pass (corroboration/offer_pool/decision/
	// reserved_kind_admitted's own per-candidate lines) declares "pass"
	// and is grouped by (request_id, pass); an event emitted once per
	// resolveSubjects CALL regardless of internal passes (search's own
	// per-term lines, and the rest) declares no "pass" and is grouped by
	// request_id alone.
	MultiplicityBoundedManyPerPass Multiplicity = "bounded_many_per_pass"
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
	// FieldFloat (CHAOS-5517): every event before Corroboration carried
	// only integer/enum/bool/string measurements -- a candidate's
	// confidence score is this specification's first genuinely fractional
	// production value, so FieldInt's own "reject any non-whole number"
	// rule (round r3's own fractional-value fix) cannot apply to it. A
	// FieldFloat value is still refused for null/wrong-scalar-type/absent
	// exactly like FieldInt; the ONLY difference is that a fractional JSON
	// number is the EXPECTED shape here, not a defect.
	FieldFloat FieldType = "float"
	// FieldObject (CHAOS-5465 M2): a slog.Group renders as ONE nested JSON
	// object under its own key, and until this type existed the
	// specification could not describe one -- a group key certified as
	// "undeclared" however carefully its contents were built. It is
	// deliberately NOT an opaque blob: an object field declares its MEMBER
	// fields in Field.Fields, exactly one level deep and never recursive, and
	// the certifier walks them with the same validateFields pass a top-level
	// field set gets. Every member therefore keeps its own declared type,
	// presence and closed vocabulary, and an undeclared member key inside the
	// group fails certification like an undeclared top-level key.
	FieldObject FieldType = "object"
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
	// Fields describes an object_slice field's own per-element shape, or an
	// object field's own MEMBER shape (one level, never recursive). Empty for
	// every other Type.
	Fields []Field
}

// semanticStateGroupFields is the MEMBER shape of a persisted-reading group.
// carried_state and fresh_state render through the same producer
// (contextfabric.semanticStateLogGroup), so they declare the SAME members --
// one list, referenced twice, never two lists that can drift.
//
// Only `present` is required: an absent snapshot renders present=false and
// nothing else, which is a different fact from a snapshot whose every member
// is zero.
var semanticStateGroupFields = []Field{
	{Key: "present", Type: FieldBool, Presence: PresenceRequired},
	{Key: "format_version", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "family", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "family_source", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "family_table_version", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "group_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "narrowing_basis", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "scope_anchor_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "scope_anchor_term_present", Type: FieldBool, Presence: PresenceConditional, Applicability: "written when present=true; the term itself is corpus text and is never published"},
	{Key: "frame_present", Type: FieldBool, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "frame_version", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "subject_expression_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "subject_member_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "subject_group_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "subject_expected_kind", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "operands", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "retrieval_term_count", Type: FieldInt, Presence: PresenceConditional, Applicability: "written when present=true; the terms themselves are corpus text and are never published"},
	{Key: "goals", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "temporal", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "emphasis", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "dimensions", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "obligations", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "widened_obligations", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "emitted_shape", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "gate", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "roles", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "requirements_declared", Type: FieldBool, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "requirement_count", Type: FieldInt, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "requirements", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "requirement_derivation_version", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
	{Key: "request_identity_version", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true; the recipe that produced the digest, empty on a snapshot written before one existed"},
	{Key: "request_identity_digest", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true; a hash, never the inputs it was taken over"},
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

// ConfirmedKindScope* are the FIVE values chaos4154_confirmed_kind_scope.go's
// "state" field (also reused, unchanged, by low_population_kind_scope's own
// "state" field -- see LowPopulationKindScope's own doc comment) ships.
// CHAOS-5636: centralized here, the same DeclaredKindRescue* migration this
// package already made -- a second, independently typed copy of a closed
// vocabulary is exactly how two copies drift apart, and this one is
// ALREADY shared by two producers (confirmed_kind_scope,
// low_population_kind_scope) rather than one, making the drift risk this
// migration removes a live one, not a hypothetical. graphrank's own
// confirmedKindScope* constants (chaos4154_confirmed_kind_scope.go) now
// alias these rather than retyping the five literals a second time.
const (
	ConfirmedKindScopeNotAttempted   = "not_attempted"
	ConfirmedKindScopeComplete       = "complete"
	ConfirmedKindScopeTruncated      = "truncated"
	ConfirmedKindScopeFailed         = "failed"
	ConfirmedKindScopePlanIncomplete = "plan_incomplete"
)

var confirmedKindScopeState = []string{
	ConfirmedKindScopeNotAttempted, ConfirmedKindScopeComplete, ConfirmedKindScopeTruncated,
	ConfirmedKindScopeFailed, ConfirmedKindScopePlanIncomplete,
}

// ConfirmedKindVectorScope* are the SIX values
// chaos4155_confirmed_kind_vector_scope.go's own vector-census outcome
// carries, ridden by confirmed_kind_scope's own "vector_census_state"
// field. Centralized here for the same reason as ConfirmedKindScope*
// immediately above -- graphrank's own ConfirmedKindVectorScope* constants
// (chaos4155_confirmed_kind_vector_scope.go) now alias these.
const (
	ConfirmedKindVectorScopeNotAttempted = "not_attempted"
	ConfirmedKindVectorScopeComplete     = "complete"
	ConfirmedKindVectorScopeOverBudget   = "over_budget"
	ConfirmedKindVectorScopeMalformed    = "malformed"
	ConfirmedKindVectorScopeDrift        = "incomplete_snapshot_drift"
	ConfirmedKindVectorScopeFailed       = "failed"
)

var confirmedKindVectorScopeState = []string{
	ConfirmedKindVectorScopeNotAttempted, ConfirmedKindVectorScopeComplete, ConfirmedKindVectorScopeOverBudget,
	ConfirmedKindVectorScopeMalformed, ConfirmedKindVectorScopeDrift, ConfirmedKindVectorScopeFailed,
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

// Search is the Info line (graphrank/tracer.go, case "search") emitted once
// per term in a resolveSubjects call's own terms list -- CHAOS-5517's first
// MultiplicityBoundedManyPerPass event: a resolution can search anywhere
// from zero to several terms, bounded by that call's own terms list length,
// with no per-pass concept (the per-term search loop runs once per
// resolveSubjects call, before any internal re-decision pass exists).
var Search = Event{
	ID:                 "graphrank.search",
	Msg:                "context fabric resolution trace: search",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by resolveSubjects' own terms list length for this call -- Total on every line is that length, Index is this line's 1-based position in the loop that produced it.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"search"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "term_hash", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a SHA-256 hex digest of the search term,
			// never the term itself.
		},
		{Key: "result_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "truncated", Type: FieldBool, Presence: PresenceRequired},
	},
}

// KindOfferWithheld is the Info line (graphrank/tracer.go, case
// "kind_offer_withheld", CHAOS-5218) emitted ONLY when the unconditional
// kind_offer event's own offer withheld at least one frame-declared kind
// because the full merged pool held no candidate of that kind -- a genuine
// zero-multiplicity event (most resolutions never reach this trigger at
// all), single-shot per resolveSubjects call (never re-derived per internal
// pass), hence CHAOS-5517's first MultiplicityZeroOrOnePerRequest event.
var KindOfferWithheld = Event{
	ID:                 "graphrank.kind_offer_withheld",
	Msg:                "context fabric resolution trace: kind offer withheld",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- emitted only when the offer withheld at least one frame-declared kind; CertifyAbsent asserts the (far more common) case where it never fires.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"kind_offer_withheld"}},
		{Key: "withheld_count", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "withheld_kinds", Type: FieldStringSlice, Presence: PresenceRequired,
			// Closed-vocabulary subject-kind VALUES only (never a canonical
			// id, never candidate identity) -- the same ruled exception
			// boundary_kinds/missing_kinds_list already carry. Left as an
			// open string_slice here (the closed vocabulary itself lives on
			// contextfabric.SubjectKind, outside this package's own import
			// graph) -- same convention kind_offer's own boundary_kinds
			// field will use once declared.
		},
		{Key: "declared_hint_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "distinct_kind_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "suppressed_by_cardinality", Type: FieldBool, Presence: PresenceRequired},
		{Key: "suppressed_by_unservable_declared_kind", Type: FieldBool, Presence: PresenceRequired},
	},
}

// Corroboration is the Debug per-candidate line (graphrank/tracer.go, case
// "corroboration", event.CorroborationSummary==false) emitted once per
// candidate every corroboration pass processes, unconditionally (retrieval-
// pool-sized -- 96 events measured on a 90-candidate crowd -- past the
// per-pass Info ceiling, so it stays Debug; CorroborationSummary below is
// the once-per-pass Info line an operator actually gets). CHAOS-5517's
// second MultiplicityBoundedManyPerPass event, and the first one that also
// declares "pass" (emitted from inside resolveFromMergedCandidatesWithAnchorSlot,
// so a multi-pass resolution legitimately produces this shape more than
// once per request, once per pass).
var Corroboration = Event{
	ID:                 "graphrank.corroboration",
	Msg:                "context fabric resolution trace: corroboration",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by this pass's own candidate-set size -- Total on every line is that size, matching CorroborationSummary's own candidate_count for the SAME pass (same underlying slice, no intervening append/removal).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"corroboration"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "base_confidence", Type: FieldFloat, Presence: PresenceRequired},
		{Key: "final_confidence", Type: FieldFloat, Presence: PresenceRequired},
		{Key: "distinct_mechanisms", Type: FieldInt, Presence: PresenceRequired},
	},
}

// CorroborationSummary is the once-per-pass Info line (graphrank/tracer.go,
// case "corroboration", event.CorroborationSummary==true) folding every
// candidate Corroboration's own pass into one bounded aggregate. Now
// pass-keyed (CHAOS-5517) the same way ranked_cut/anchor_slot_displaced/
// decision_summary already are.
var CorroborationSummary = Event{
	ID:                 "graphrank.corroboration_summary",
	Msg:                "context fabric resolution trace: corroboration summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per corroboration pass -- the per-candidate detail this summary aggregates stays at Debug (Corroboration above).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"corroboration"}},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "top_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "min_confidence", Type: FieldFloat, Presence: PresenceRequired},
		{Key: "max_confidence", Type: FieldFloat, Presence: PresenceRequired},
	},
}

// ReservedKindAdmitted is the Info line (graphrank/tracer.go, case
// "reserved_kind_admitted") emitted once per candidate the CHAOS-4038 kind
// reserve keeps past the flat cut -- CHAOS-5517's third
// MultiplicityBoundedManyPerPass, pass-keyed event: bounded by however many
// admissions this pass's own reserve produced (0 in the common case -- the
// reserve is inert on most resolutions), never a fixed count, and no
// sibling summary line exists for it (its own presence, or absence, IS the
// operator-visible signal), so its bound is self-carried index/total only.
var ReservedKindAdmitted = Event{
	ID:                 "graphrank.reserved_kind_admitted",
	Msg:                "context fabric resolution trace: reserved kind admitted",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by this pass's own reserve-admission count (self-carried index/total) -- 0 on the common path where the reserve never fires; no sibling summary event exists for this stage.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"reserved_kind_admitted"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "rank", Type: FieldInt, Presence: PresenceRequired},
		{Key: "survived", Type: FieldBool, Presence: PresenceRequired},
	},
}

// OfferPool is the Debug per-candidate line (graphrank/tracer.go, case
// "offer_pool", event.OfferPoolSummary==false) emitted for every vector-only
// candidate the phase-4 offer-pool seam acts on -- EITHER demoted (still
// competes for the commit decision, never offered) or excluded (withheld
// from the offer entirely) -- distinguished by the "disposition" field, the
// SAME wire Msg either way. CHAOS-5517's fourth MultiplicityBoundedManyPerPass
// event: bounded by the combined demoted+excluded count for this pass, the
// SAME two counts OfferPoolSummary reports below (its own resolution.go
// producer computes the combined Total BEFORE emitting the first line, so
// it is never a second, independently-derived number).
var OfferPool = Event{
	ID:                 "graphrank.offer_pool",
	Msg:                "context fabric resolution trace: offer pool",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by this pass's own combined vector_only_demoted + vector_only_excluded count -- Total on every line is that sum, matching OfferPoolSummary's own two fields for the SAME pass.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"offer_pool"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "disposition", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"vector_only_demoted", "vector_only_excluded"}},
	},
}

// OfferPoolSummary is the once-per-pass Info line (graphrank/tracer.go, case
// "offer_pool", event.OfferPoolSummary==true) folding the phase-4 offer-pool
// seam's own pass into one bounded aggregate -- emitted unconditionally,
// including on the early-return "the graph held nothing" path (explicit
// zeros, distinguishable from a build where this seam never ran). Now
// pass-keyed (CHAOS-5517) the same way CorroborationSummary already is.
var OfferPoolSummary = Event{
	ID:                 "graphrank.offer_pool_summary",
	Msg:                "context fabric resolution trace: offer pool summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per pass, emitted unconditionally (including explicit zeros on the early-return empty-graph path) -- the per-candidate detail this summary aggregates stays at Debug (OfferPool above).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"offer_pool"}},
		{Key: "vector_only_excluded", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_only_demoted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "emptied_by_exclusion", Type: FieldBool, Presence: PresenceRequired},
	},
}

// AnchorKindWithheld is the Debug per-candidate line (graphrank/tracer.go,
// case "anchor_kind_withheld") emitted for every subject resolve.go's
// contest-admission boundary (CHAOS-5422, chaos5422_contest_set.go) refused
// on the grouping/scope axis -- resolve.go's own admission.withheldSubjects().
//
// r1 class finding (CHAOS-5517): this used to be emitted under Stage
// "offer_pool", the SAME wire Msg as the pass-scoped vector-only detail
// lines (OfferPool above) -- an executed AST enumeration of every
// ResolutionTraceEvent{...} construction site (not the original hand
// sweep, which missed it) found it carrying disposition
// "anchor_kind_withheld", outside OfferPool's own declared closed
// vocabulary, with no pass/index/total at all. Genuinely REQUEST-scoped,
// not pass-scoped: it fires once per resolveSubjects call, after every
// internal pass has finished, describing the union of everything refused
// across however many passes ran -- tagging it with a fabricated pass
// number would misrepresent it as belonging to one pass it does not
// describe. Given its own Stage/Msg so it can never again collide with
// OfferPool's; self-carries index/total (no "pass" field declared) the
// same way Search/KindHintSearch/ExactNameSearch already do -- a per-event
// choice MultiplicityBoundedManyPerPass's own doc comment allows.
var AnchorKindWithheld = Event{
	ID:                 "graphrank.anchor_kind_withheld",
	Msg:                "context fabric resolution trace: anchor kind withheld",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per request_id call by len(admission.withheldSubjects()) -- self-carried index/total, cross-checked against AnchorKindWithheldSummary's own anchor_kind_withheld count.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_kind_withheld"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "disposition", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_kind_withheld"}},
	},
}

// AnchorKindWithheldSummary is the Info line (graphrank/tracer.go, case
// "anchor_kind_withheld_summary") folding resolve.go's contest-admission
// disclosure into one unconditional per-call line -- explicit zeros
// included, so a question that refused nothing and a build where the
// refusal stopped happening can never read alike.
//
// r1 class finding (CHAOS-5517): pre-fix this reused Stage "offer_pool"
// with the SAME OfferPoolSummary==true flag OfferPoolSummary's own
// pass-scoped vector-only summary uses, but populated an entirely
// DIFFERENT field set (the anchor-kind-withheld aggregate) that
// tracer.go's "offer_pool" case never read -- the line reached production
// as a decoy, all vector-only fields at their zero default and pass=0,
// with its real content silently absent from that wire shape (visible
// only via DecisionSummary's own fold of the same producer call). Given
// its own Stage/Msg, ExactlyOnePerRequest like AnchorPool/
// KindCoverageFloor/AnchorOffer (no "pass" field: this fires once per
// call, not once per internal pass).
var AnchorKindWithheldSummary = Event{
	ID:           "graphrank.anchor_kind_withheld_summary",
	Msg:          "context fabric resolution trace: anchor kind withheld summary",
	Level:        LevelInfo,
	Multiplicity: MultiplicityExactlyOnePerRequest,
	Attribution:  []string{"request_id"},
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_kind_withheld_summary"}},
		{Key: "anchor_kind_withheld", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "anchor_kind_withheld_scope", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a SubjectKind token, or "none".
		},
		{
			Key: "anchor_kind_withheld_reason", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a reason token, or "none" -- matches
			// DecisionSummary's own offer_pool_anchor_kind_withheld_reason
			// field for the same underlying producer value.
		},
		{Key: "anchor_kind_withheld_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "anchor_kind_exempted", Type: FieldInt, Presence: PresenceRequired},
	},
}

// Decision is the Debug line (graphrank/tracer.go, case "decision") reporting
// phase-3's own commit decision for a pass -- CHAOS-5517's fifth
// MultiplicityBoundedManyPerPass event, and the only one whose own bound is
// NEVER zero: every pass reaches exactly one of three mutually exclusive
// branches (resolution.go's own switch), each unconditional -- "committed"
// (one line per committed subject, Total=len(resolution.Committed)) or
// "ambiguous"/"no_commit" (exactly one line, Total=1). DecisionSummary is
// the folded per-REQUEST Info line an operator actually reads; this is its
// own per-pass, per-outcome detail.
var Decision = Event{
	ID:                 "graphrank.decision",
	Msg:                "context fabric resolution trace: decision",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by the pass's own outcome: one line per committed subject for a \"committed\" pass (self-carried index/total), otherwise exactly one line (index=1/total=1) -- never zero.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "pass", Type: FieldInt, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"decision"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"committed", "ambiguous", "no_commit"}},
		{
			Key: "winning_mechanism", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.MatchMechanism token, or "".
		},
		{
			Key: "commit_gate", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a commit-gate reason token, or "".
		},
		{Key: "alias_identity_complete", Type: FieldBool, Presence: PresenceRequired},
		{Key: "identity_trust_gate_blocked", Type: FieldBool, Presence: PresenceRequired},
		{Key: "search_truncated", Type: FieldBool, Presence: PresenceRequired},
		{
			Key: "commit_basis", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.CommitBasis token, or "".
		},
		{Key: "tied_statistical_top", Type: FieldBool, Presence: PresenceRequired},
		{Key: "search_candidate_limit", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "population_basis", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a population-basis token, or "none".
		},
	},
}

// SearchQuestion is the Info line (graphrank/tracer.go, case
// "search_question", CHAOS-4120) emitted for the question-level
// SearchQuestion pass -- gated on `deps.SearchQuestion != nil` (a backend
// that does not implement it never runs this pass at all, the pre-CHAOS-4120
// shape), so this is CHAOS-5517's second MultiplicityZeroOrOnePerRequest
// event, not an unconditional one. Unlike Search (one line per TERM), this
// pass has no per-term identity even when it does run.
var SearchQuestion = Event{
	ID:                 "graphrank.search_question",
	Msg:                "context fabric resolution trace: search question",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on deps.SearchQuestion != nil and a non-empty question; CertifyAbsent asserts the (far more common) case where the backend does not implement it.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"search_question"}},
		{Key: "result_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "truncated", Type: FieldBool, Presence: PresenceRequired},
	},
}

// AliasLookup is the Info line (graphrank/tracer.go, case "alias_lookup")
// emitted from the single alias-lookup emission site in resolve.go -- gated
// on `deps.AliasLookup != nil`. CORRECTED after tracing the real production
// wiring (initial declaration wrongly claimed no composition root sets it,
// from a grep that only matched a struct-literal `AliasLookup:` and missed
// the real site's plain assignment): falkorgraph/reader.go wires
// deps.AliasLookup whenever `a.config.IdentityUniverse != nil`, and
// hosted/open.go's real buildContextFabricInvestigator ALWAYS passes
// wireIdentityUniverse=true -- so AliasLookup IS wired in real production,
// gated instead on `!temporal.active` (a historical-axis question skips it
// entirely, HIGH-6's own "temporal authority stays with the graph" rule)
// and on the identity-universe read itself finding a match. Still
// MultiplicityZeroOrOnePerRequest: genuinely conditional, just not on
// today's absent wiring -- CertifyAbsent covers the historical-axis /
// no-match path, Certify covers the firing one.
var AliasLookup = Event{
	ID:                 "graphrank.alias_lookup",
	Msg:                "context fabric resolution trace: alias lookup",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on deps.AliasLookup != nil, which no production composition root sets today.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"alias_lookup"}},
		{Key: "complete", Type: FieldBool, Presence: PresenceRequired},
		{Key: "matched_claimants", Type: FieldInt, Presence: PresenceRequired},
	},
}

// AnchorPool is the Info line (graphrank/tracer.go, case "anchor_pool")
// naming the scope anchor decision phase 4 hands to retrieval and the
// filter -- once per resolution, no per-candidate counterpart.
var AnchorPool = Event{
	ID:                 "graphrank.anchor_pool",
	Msg:                "context fabric resolution trace: anchor pool kind scope",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call -- emitted from the same statement that hands the scope to the confirmed-kind filter.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_pool"}},
		{
			Key: "anchor_pool_kind_scope", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "none".
		},
		{Key: "anchor_pool_kind_scope_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"receipt", "confirmed_anchor", "none"}},
		{
			Key: "member_kind_confirmed", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "none".
		},
		{Key: "reserved_kinds", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "filter_kinds", Type: FieldStringSlice, Presence: PresenceRequired},
	},
}

// KindCoverageFloor is the Info line (graphrank/tracer.go, case
// "kind_coverage_floor", CHAOS-4086/CHAOS-4038) reporting the coverage
// floor's own operator-visible half -- once per resolution.
var KindCoverageFloor = Event{
	ID:                 "graphrank.kind_coverage_floor",
	Msg:                "context fabric resolution trace: kind coverage floor",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"kind_coverage_floor"}},
		{Key: "fired", Type: FieldBool, Presence: PresenceRequired},
		{Key: "missing_kinds", Type: FieldInt, Presence: PresenceRequired},
		{Key: "truncated", Type: FieldBool, Presence: PresenceRequired},
		{
			Key: "missing_kinds_list", Type: FieldStringSlice, Presence: PresenceRequired,
			// Closed-vocabulary subject-kind VALUES only (never a canonical
			// id, never candidate identity) -- open string_slice here, same
			// convention as KindOfferWithheld's own withheld_kinds.
		},
	},
}

// ConfirmedKindRescue is the Info line (graphrank/tracer.go, case
// "confirmed_kind_rescue", CHAOS-4132) reporting the confirmed-kind
// rescue's own operator-visible half -- once per resolution; its own
// presence already means the rescue was attempted.
var ConfirmedKindRescue = Event{
	ID:                 "graphrank.confirmed_kind_rescue",
	Msg:                "context fabric resolution trace: confirmed kind rescue",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on confirmedKindRescueAttempted (this event's own PRESENCE already means the rescue was attempted); CertifyAbsent asserts the case where it never ran.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"confirmed_kind_rescue"}},
		{Key: "attempted", Type: FieldBool, Presence: PresenceRequired},
		{Key: "fired", Type: FieldBool, Presence: PresenceRequired},
		{Key: "result_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "truncated", Type: FieldBool, Presence: PresenceRequired},
	},
}

// IdentityUniverse is the Info line (falkorgraph/reader.go, case
// "identity_universe", chris ruling 2026-08-17) -- CHAOS-5517's named
// cross-package producer: constructed in falkorgraph, not graphrank,
// inside the SAME deps.AliasLookup closure AliasLookup's own event fires
// from (falkorgraph wires AliasLookup only when a.config.IdentityUniverse
// is configured, which hosted/open.go's real production composition root
// always does), gated further on `!temporal.active` (a historical-axis
// question skips this mechanism entirely). Genuinely conditional in
// production, hence MultiplicityZeroOrOnePerRequest, not unconditional.
var IdentityUniverse = Event{
	ID:                 "graphrank.identity_universe",
	Msg:                "context fabric resolution trace: identity universe read",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on the SAME AliasLookup wiring/temporal-axis condition AliasLookup's own event fires under, plus the identity-universe read itself running.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"identity_universe"}},
		{Key: "complete", Type: FieldBool, Presence: PresenceRequired},
	},
}

// KindHintSearch is the Debug per-node line (graphrank/tracer.go, case
// "kind_hint_search", CHAOS-4348) emitted once per matched node from
// traceKindHintSearch (chaos4348_reachability.go) -- CALLED MORE THAN ONCE
// PER REQUEST (once per kind x term the coverage-floor hint loop tries),
// each call with its own independent node set, so unlike every other
// BoundedManyPerPass event declared so far, its Attribution includes
// "term_hash": the bound is scoped per (request_id, term_hash) CALL, never
// accumulated across calls that share no buffer.
//
// Attribution ALSO includes "queried_kind" (r1 finding, CHAOS-5517): the
// hint loop calls traceKindHintSearch once per (kind, term) pair, so two
// different hinted kinds queried for the SAME term each independently
// produce their own index=1..N/total=N sequence. Without queried_kind in
// scope, two such calls collapse into one (request_id, term_hash) group
// and their otherwise-identical indices read as duplicates. queried_kind
// is the loop's OWN kind for this call, distinct from subject_kind (the
// matched node's own kind, read off the result).
var KindHintSearch = Event{
	ID:                 "graphrank.kind_hint_search",
	Msg:                "context fabric resolution trace: kind hint search",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id", "term_hash", "queried_kind"},
	BoundedAggregation: "bounded per (request_id, term_hash, queried_kind) call -- self-carried index/total, never accumulated across the multiple calls one resolution can make.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"kind_hint_search"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "term_hash", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a SHA-256 hex digest of the search term.
		},
		{Key: "queried_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// ExactNameSearch is the Debug per-node line (graphrank/tracer.go, case
// "exact_name_search", CHAOS-4348) -- the same per-(request_id, term_hash)
// call-scoped shape as KindHintSearch above, from traceExactNameSearch.
var ExactNameSearch = Event{
	ID:                 "graphrank.exact_name_search",
	Msg:                "context fabric resolution trace: exact name search",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id", "term_hash"},
	BoundedAggregation: "bounded per (request_id, term_hash) call -- self-carried index/total, never accumulated across the multiple calls one resolution can make.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"exact_name_search"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "term_hash", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a SHA-256 hex digest of the search term.
		},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// AnchorOffer is the Info line (graphrank/tracer.go, case "anchor_offer",
// CHAOS-4210) reporting the anchor-axis label-normalization count -- fires
// unconditionally, once per resolveSubjects call (anchorOfferMaterial is
// called and traced independently of kind/candidate/handle's shared site).
var AnchorOffer = Event{
	ID:                 "graphrank.anchor_offer",
	Msg:                "context fabric resolution trace: anchor offer",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call -- anchorOfferMaterial's own single call site.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_offer"}},
		{Key: "labels_normalized_count", Type: FieldInt, Presence: PresenceRequired},
	},
}

// WindowContinuationDecision is the once-per-REQUEST Info line
// (telemetry.go, SlogEngineTelemetry.RecordWindowContinuationDecision) that
// reports the CHAOS-5465 window-continuation decision for every request
// carrying a window receipt, emitted from Investigate's single deferred site.
//
// CHAOS-5582 registers it here with the axis decision it now carries: the
// receipt count and explicit-window presence the receipt conflicts veto on,
// the fresh, carried and executed axes, and interpreted_axis_outcome. Every
// closed vocabulary below is read from PRODUCTION
// (contextfabric.ContinuationDecisionLineVocabulary), never retyped here: the
// emitter's own membership guards and this declaration read one list.
var WindowContinuationDecision = Event{
	ID:                 "contextfabric.window_continuation_decision",
	Msg:                "context fabric window continuation decision",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per Investigate call whose request carries a window receipt, from the one deferred emit site; a request with no window receipt emits none (the line's denominator is receipt-bearing requests).",
	Fields: []Field{
		// Open: the authenticated org id and the caller-named prior result id.
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "source_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "seed_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("seed_source")},
		{Key: "family_carried", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("family_carried")},
		{Key: "family_fresh", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("family_fresh")},
		{Key: "family_accepted", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("family_accepted")},
		{Key: "family_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("family_source")},
		{Key: "continuation_disposition", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("continuation_disposition")},
		{Key: "decision_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("decision_reason")},
		{Key: "comparison_evaluated", Type: FieldBool, Presence: PresenceRequired},
		{Key: "agreement", Type: FieldBool, Presence: PresenceRequired},
		{Key: "conflict_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("conflict_reason")},
		{Key: "conflict_count", Type: FieldInt, Presence: PresenceRequired},
		// Open at this layer: a comma-joined list whose MEMBERS the emitter
		// checks one by one; the joined string itself has no finite
		// vocabulary.
		{Key: "conflict_fields", Type: FieldString, Presence: PresenceRequired},
		// Open: relative id | frozen start | frozen end | provenance, or empty.
		{Key: "applied_window", Type: FieldString, Presence: PresenceRequired},
		{Key: "carried_context_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "fresh_context_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "accepted_context_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "composition_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("composition_outcome")},
		{Key: "composition_failed_invariant", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("composition_failed_invariant")},
		{Key: "refusal_basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("refusal_basis")},
		// Open: the prior result id the window-only request names, or empty.
		{Key: "referenced_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "carrier_read", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("carrier_read")},
		// The carrier's SNAPSHOT read, beside the carrier read itself: a
		// carrier can read back perfectly and still carry no usable reading.
		{Key: "carried_state_read", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("carried_state_read")},
		{Key: "request_identity_match", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("request_identity_match")},
		// THE TWO READINGS, IN FULL. The carried one is what admission read;
		// the fresh one is this turn's diagnostic proposal. Same member shape
		// on both sides, so they are read against each other key by key.
		{Key: "carried_state", Type: FieldObject, Presence: PresenceRequired, Fields: semanticStateGroupFields},
		{Key: "fresh_state", Type: FieldObject, Presence: PresenceRequired, Fields: semanticStateGroupFields},
		{Key: "window_receipt_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "explicit_window_present", Type: FieldBool, Presence: PresenceRequired},
		{Key: "interpreted_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("interpreted_axis")},
		{Key: "carried_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("carried_axis")},
		{Key: "executed_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("executed_axis")},
		{Key: "interpreted_axis_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("interpreted_axis_outcome")},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// KindOffer is the Info line (graphrank/tracer.go, case "kind_offer",
// CHAOS-4012) reporting kindOfferMaterial/candidateOfferMaterial/
// handleOfferMaterial's own combined per-resolution offer bookkeeping --
// UNCONDITIONAL: these three run on every resolution, never gated behind a
// "still missing" precondition, so this stage fires every time a tracer is
// wired. CHAOS-5636: folded (kindOfferFold, resolve.go) the same way
// anchor_offer/kind_coverage_floor already are -- the mechanism's own
// business trigger is unconditional, but the call site sits deep inside
// resolveSubjects, downstream of several early returns that would
// otherwise skip it silently.
var KindOffer = Event{
	ID:                 "graphrank.kind_offer",
	Msg:                "context fabric resolution trace: kind offer",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call -- kindOfferMaterial/candidateOfferMaterial/handleOfferMaterial's own unconditional call site, folded (CHAOS-5636) so an early return upstream cannot skip it.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"kind_offer"}},
		{Key: "explicit_hint_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "declared_hint_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "declared_withheld_not_in_pool_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "distinct_kind_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "suppressed_by_cardinality", Type: FieldBool, Presence: PresenceRequired},
		{Key: "suppressed_by_unservable_declared_kind", Type: FieldBool, Presence: PresenceRequired},
		{Key: "candidate_offer_count", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "offer_kind", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary in THIS package's own sense (the field is
			// actually a small fixed set -- "kind"/"candidate"/"both"/"" --
			// but graphrank's own resolve.go computes it inline rather than
			// through a named, exported constant list this package could
			// read without retyping it, so it is left undeclared here
			// rather than risking a third, drifted copy).
		},
		{Key: "candidate_offer_labels_normalized_count", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "boundary_kinds", Type: FieldStringSlice, Presence: PresenceRequired,
			// Closed-vocabulary subject-kind VALUES only (never a canonical
			// id, never candidate identity) -- open string_slice here, the
			// same convention KindOfferWithheld's own withheld_kinds and
			// KindCoverageFloor's own missing_kinds_list already use.
		},
		{Key: "boundary_kinds_before_repair", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "distinct_kind_count_before_repair", Type: FieldInt, Presence: PresenceRequired},
		{Key: "suppressed_by_cardinality_before_repair", Type: FieldBool, Presence: PresenceRequired},
		{Key: "handle_offer_count_before_graph_source", Type: FieldInt, Presence: PresenceRequired},
		{Key: "handle_offer_graph_derived_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "handle_offer_graph_derived_rejected_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "offered_under_window_gate", Type: FieldBool, Presence: PresenceRequired},
	},
}

// ConfirmedKindScope is the Info line (graphrank/tracer.go, case
// "confirmed_kind_scope", CHAOS-4154/CHAOS-4155) reporting the
// confirmed-kind truncation-scoping mechanism's own operator-visible half,
// including the CHAOS-4155 Phase 1 shadow vector census riding the same
// line. Genuinely MultiplicityZeroOrOnePerRequest, NOT ExactlyOnePerRequest
// despite looking like the same "fold it unconditional" class as
// KindOffer/AnchorOffer/KindCoverageFloor at a glance:
// TestResolveSubjects_ConfirmedKindScope_NilConfirmedKindNeverTriggers
// (chaos4154_confirmed_kind_scope_test.go) pins a CHAOS-4039
// non-interference requirement that this mechanism stay STRUCTURALLY
// UNREACHABLE for a confirmedKind==nil resolution -- a synthetic "never
// attempted" fallback line on every such resolution would violate that
// guarantee by making the stage fire where the ticket that owns it
// requires silence. CertifyAbsent asserts the (far more common) case where
// it never fires.
var ConfirmedKindScope = Event{
	ID:                 "graphrank.confirmed_kind_scope",
	Msg:                "context fabric resolution trace: confirmed kind scope",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on confirmedKind != nil && resolution-wide searchTruncated && nothing committed yet (CHAOS-4039 requires it stay structurally unreachable otherwise); CertifyAbsent asserts the case where it never fires.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"confirmed_kind_scope"}},
		{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: confirmedKindScopeState},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "vector_census_state", Type: FieldString, Presence: PresenceRequired,
			// Not always populated: zero-value "" on every state other than
			// confirmedKindScopePlanIncomplete, the only case that invokes
			// the CHAOS-4155 shadow arm at all. Declared PresenceRequired
			// (always WRITTEN, explicit zero/empty when not applicable) --
			// see clause 3's own "absence must never substitute for a
			// measured zero" -- so "" is a legitimate, closed-vocabulary
			// member here, not an omission.
			ClosedVocabulary: append(append([]string{}, confirmedKindVectorScopeState...), ""),
		},
		{Key: "vector_census_population_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_enumerated_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_malformed_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_query_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_queries_scored", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_comparison_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_rival_count_above_tau", Type: FieldInt, Presence: PresenceRequired},
		{Key: "vector_census_snapshot_stable", Type: FieldBool, Presence: PresenceRequired},
		{Key: "vector_census_duration_ms", Type: FieldInt, Presence: PresenceRequired},
	},
}

// lowPopulationKindScopeOutcome is the closed vocabulary
// LowPopulationKindScope's own summary line reports on
// LowPopulationKindScopeOutcome -- the four values
// chaos4417_low_population_kind_scope.go's own lowPopulationKindScopeOutcome*
// constants carry.
var lowPopulationKindScopeOutcome = []string{"vector_configured", "offer_only", "no_low_pop_candidates", "error"}

// LowPopulationKindScope is the Debug per-kind line (graphrank/tracer.go,
// case "low_population_kind_scope", CHAOS-4417) emitted once per
// chaos4417LowPopulationScopedKinds member attempted this resolution --
// this specification's sixth MultiplicityBoundedManyPerPass event, bounded
// by that fixed-length constant, self-carrying Index/Total (CHAOS-5636)
// the same way Search/KindHintSearch/ExactNameSearch already do.
var LowPopulationKindScope = Event{
	ID:                 "graphrank.low_population_kind_scope",
	Msg:                "context fabric resolution trace: low population kind scope",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per resolveSubjects call by len(chaos4417LowPopulationScopedKinds), a fixed constant known before the loop starts -- self-carried index/total, cross-checked against no sibling summary count (the summary shares no field with the detail line; see LowPopulationKindScopeSummary's own doc comment for why the two were split onto distinct Msg strings).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"low_population_kind_scope"}},
		{
			Key: "kind", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, always
			// one of chaos4417LowPopulationScopedKinds' own three members
			// on this variant (never empty -- empty is the summary's own
			// discriminator, which is why the two now carry distinct Msg
			// strings rather than sharing this one).
		},
		{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: confirmedKindScopeState},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
	},
}

// LowPopulationKindScopeSummary is the Debug once-per-request line
// (graphrank/tracer.go, case "low_population_kind_scope", empty
// LowPopulationKindScopeKind) folding applyLowPopulationKindOffers' own
// call into one outcome token -- fired exactly once per call via a plain
// deferred statement (chaos4417_low_population_kind_scope.go), regardless
// of outcome, INCLUDING the vector-configured early return, so this is
// MultiplicityExactlyOnePerRequest, not conditional the way the sibling
// per-kind detail line is.
//
// CHAOS-5636 class fix: this used to share ITS OWN Msg with the per-kind
// detail line above (discriminated only by an empty "kind" field value,
// not by Stage/Msg the way every other detail/summary pair in this
// package already is) -- certify's own line lookup matches by Msg alone,
// so declaring these as two Events sharing one Msg would have
// certifyBoundedMany silently mix this line into the detail scope's own
// Index/Total agreement check. Given its own Msg here, the same fix
// AnchorKindWithheld already needed for a different Msg collision (its
// own doc comment).
var LowPopulationKindScopeSummary = Event{
	ID:                 "graphrank.low_population_kind_scope_summary",
	Msg:                "context fabric resolution trace: low population kind scope summary",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per resolveSubjects call, emitted via a plain deferred statement inside applyLowPopulationKindOffers -- including the vector-configured early return.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"low_population_kind_scope"}},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: lowPopulationKindScopeOutcome},
	},
}

// IdentityGate is the Debug per-candidate line (graphrank/tracer.go, case
// "identity_gate", CHAOS-3884) emitted from NodeCandidate (candidate.go)
// for every isAliasLookupScopedKind candidate -- this specification's
// seventh MultiplicityBoundedManyPerPass event, and the one whose own
// Index/Total (CHAOS-5636) cannot be stamped at the Trace call itself:
// NodeCandidate fires from TWO separate call sites in resolve.go, each its
// own loop with no shared upfront bound. identityGateSummaryBuffer
// (resolve.go) now buffers every detail line (not only counts) and stamps
// Index/Total at flush, once the call's own total gate-checked population
// is finally known -- see that buffer's own doc comment for the full
// mechanism and why it is the one deliberate exception to this package's
// "forward first, bookkeep second" fold shape.
var IdentityGate = Event{
	ID:                 "graphrank.identity_gate",
	Msg:                "context fabric resolution trace: identity gate",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per resolveSubjects call by however many isAliasLookupScopedKind candidates NodeCandidate builds across BOTH its own call sites -- self-carried index/total, stamped at identityGateSummaryBuffer.flush() once that total is known, matching IdentityGateSummary's own candidate_count for the same call.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"identity_gate"}},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "from_keyed_identity_lookup", Type: FieldBool, Presence: PresenceRequired},
		{Key: "eligible_kind", Type: FieldBool, Presence: PresenceRequired},
		{Key: "alias_matched", Type: FieldBool, Presence: PresenceRequired},
		{Key: "provider_matched", Type: FieldBool, Presence: PresenceRequired},
		{Key: "gate_fired", Type: FieldBool, Presence: PresenceRequired},
		{Key: "final_confidence", Type: FieldFloat, Presence: PresenceRequired},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
	},
}

// IdentityGateSummary is the Info once-per-call line (graphrank/tracer.go,
// case "identity_gate", IdentityGateSummary==true) folding IdentityGate's
// own call into one aggregate -- emitted by identityGateSummaryBuffer.flush()
// ONLY when at least one candidate reached the gate (candidateCount==0
// withholds the summary entirely: "silence means never reached", the same
// convention SurvivorVerdictSummary's own doc comment uses), hence
// MultiplicityZeroOrOnePerRequest rather than Exactly.
var IdentityGateSummary = Event{
	ID:                 "graphrank.identity_gate_summary",
	Msg:                "context fabric resolution trace: identity gate summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- withheld entirely when no alias-lookup-scoped candidate reached the gate; CertifyAbsent asserts that case.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"identity_gate"}},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "fired_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "fired_ids", Type: FieldStringSlice, Presence: PresenceRequired},
	},
}

// shadowOutcome is the closed vocabulary ShadowOutcome (chaos3899_evidence_round.go's
// own ShadowOutcome type) carries.
var shadowOutcome = []string{"would_commit", "would_no_match", "would_clarify"}

// kindInsensitivityOutcome is the closed vocabulary
// chaos3900_structure_offers.go's own kindInsensitivityOutcome type carries
// (four fixed values), reused by BOTH ShadowKindInsensitivityOutcome and
// ShadowHandleInsensitivityOutcome -- plus the empty string, the zero value
// written whenever the respective *InsensitivityEvaluated bool is false
// (the probe never ran).
var kindInsensitivityOutcome = []string{"", "commit_sound", "no_match_sound", "kind_sensitive_outcome", "probe_error"}

// explicitKindNarrowingMode is the closed vocabulary
// chaos3900_structure_offers.go's own explicitKindNarrowingMode type
// carries (including its own zero value ""), ridden by
// ShadowKindInsensitivityMode.
var explicitKindNarrowingMode = []string{"", "narrowed", "observed_no_overlap", "observed_subsumed"}

// EvidenceRound is the Info line (graphrank/tracer.go, case
// "evidence_round", CHAOS-3899 design brief v5 Slice A) reporting the
// shadow evidence round's own per-resolution outcome, SUPPRESSED from any
// commit-path decision -- measurement only. Fires on every call that
// reaches past the axis/scope gates INCLUDING a refused one (design brief
// §6/§7's own non-vacuity bar: "the round ran but found nothing" and "the
// round never ran" must be structurally distinguishable), gated on
// deps.CensusFunc != nil and the stalled-resolution precondition
// (resolve.go's own call site), hence MultiplicityZeroOrOnePerRequest.
var EvidenceRound = Event{
	ID:                 "graphrank.evidence_round",
	Msg:                "context fabric resolution trace: evidence round (shadow)",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- gated on deps.CensusFunc != nil and the stalled-resolution precondition (nothing committed, resolution-wide searchTruncated); CertifyAbsent asserts the (far more common) case where it never runs.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"evidence_round"}},
		{Key: "shadow_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: shadowOutcome},
		{
			Key: "shadow_reason", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a DegradationReason token (chaos3899_handle_grammar.go),
			// a large cross-cutting enum this package leaves open rather
			// than retype, the same convention DecisionSummary's own
			// commit_gate/refuse_basis fields already use for their own
			// cross-package reason tokens.
		},
		{
			Key: "shadow_d_identity_hash", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a SHA-256 hex digest, or "".
		},
		{Key: "shadow_precondition_unproven", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_unscoped_visibility", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_non_censused_survivor", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_handle_grammar_bound", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_anchor_unique_claimant", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_anchor_receipt_confirmed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_kinds_censused", Type: FieldInt, Presence: PresenceRequired},
		{Key: "shadow_kind_insensitivity_evaluated", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_kind_insensitivity_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: kindInsensitivityOutcome},
		{Key: "shadow_kind_insensitivity_mode", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: explicitKindNarrowingMode},
		{Key: "shadow_handle_insensitivity_evaluated", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_handle_insensitivity_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: kindInsensitivityOutcome},
		{Key: "shadow_caller_hint_short_circuit", Type: FieldBool, Presence: PresenceRequired},
	},
}

// EvidenceProbe is the Info per-kind line (graphrank/tracer.go, case
// "evidence_probe", CHAOS-3899 design brief §1.3(3)) -- ONE per-kind census
// receipt, never aggregated across kinds. This specification's eighth
// MultiplicityBoundedManyPerPass event: bounded per call by len(a.Kinds),
// known before its own loop starts (the SAME slice EvidenceRound's own
// shadow_kinds_censused already counts), self-carrying Index/Total
// (CHAOS-5636).
var EvidenceProbe = Event{
	ID:                 "graphrank.evidence_probe",
	Msg:                "context fabric resolution trace: evidence probe (shadow census)",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per resolveSubjects call by len(a.Kinds), the SAME count EvidenceRound's own shadow_kinds_censused reports for the same call -- self-carried index/total.",
	Fields: []Field{
		{
			Key: "census_kind", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token.
		},
		{Key: "census_complete", Type: FieldBool, Presence: PresenceRequired},
		{Key: "census_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "census_read_at_unix", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "census_protocol", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: caller-supplied via deps.CensusFunc
			// (CensusOutcome.Protocol), not a closed set this package
			// itself owns.
		},
		{Key: "census_closure_mismatch", Type: FieldBool, Presence: PresenceRequired},
		{Key: "census_statement_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "census_rows_read", Type: FieldInt, Presence: PresenceRequired},
		{Key: "census_handle_applied", Type: FieldBool, Presence: PresenceRequired},
		{Key: "census_anchor_applied", Type: FieldBool, Presence: PresenceRequired},
		{Key: "shadow_caller_hint_short_circuit", Type: FieldBool, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"evidence_probe"}},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
	},
}

// evidenceCensusCommitOutcome is the closed vocabulary
// emitEvidenceCensusCommit's own "outcome" argument (resolve.go, CHAOS-5636
// fold) carries: "refused" (graph-missing-satisfier or unauthorized/invalid
// node), the contest-admission disposition token graphrank's own
// contestSetDisposition constant carries ("anchor_kind_withheld" --
// AnchorKindWithheld's own doc comment), or "merged".
var evidenceCensusCommitOutcome = []string{"refused", "anchor_kind_withheld", "merged"}

// evidenceCensusCommitReason is the closed vocabulary
// EvidenceCensusCommit's own "census_commit_reason" field carries: empty
// (every outcome but the graph-missing-satisfier refusal never sets it),
// the ReasonGraphMissingSatisfier token (chaos3899_handle_grammar.go), or
// resolve.go's own censusCommitErrorReason constant.
var evidenceCensusCommitReason = []string{"", "graph_missing_satisfier", "census_commit_error"}

// EvidenceCensusCommit is the Info line (graphrank/tracer.go, case
// "evidence_census_commit", CHAOS-3896 Slice C) reporting
// mergeCensusAttestedSatisfier's own commit-gate outcome for the shadow
// evidence round's own attested satisfier. CHAOS-5636: this function's
// four internal Trace call sites now route through ONE shared emission
// point (emitEvidenceCensusCommit, resolve.go) rather than four
// independent ResolutionTraceEvent{...} literals -- see that helper's own
// doc comment for why. MultiplicityZeroOrOnePerRequest: mergeCensusAttestedSatisfier
// has exactly one, unlooped call site, itself gated on the evidence round
// having named an attested satisfier -- folding the site count from four
// to one does not change that a genuine backend fault/absence legitimately
// produces zero lines.
var EvidenceCensusCommit = Event{
	ID:                 "graphrank.evidence_census_commit",
	Msg:                "context fabric resolution trace: evidence census commit",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- mergeCensusAttestedSatisfier's own single, unlooped call site, gated on the evidence round naming an attested satisfier; CertifyAbsent asserts the (far more common) case where it never runs.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"evidence_census_commit"}},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: evidenceCensusCommitOutcome},
		{Key: "graph_existence_ok", Type: FieldBool, Presence: PresenceRequired},
		{Key: "census_commit_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: evidenceCensusCommitReason},
	},
}

// EvidenceSourceNative is the Info line (graphrank/tracer.go, case
// "evidence_source_native", CHAOS-3918/CHAOS-3899 widening measurement)
// reporting traceSourceNativeBinds' own aggregate bind count -- fires
// unconditionally once the shadow round reaches past its own axis/scope
// gates (mirrors EvidenceRound's own non-vacuity proof), so it is
// MultiplicityZeroOrOnePerRequest with the SAME gating as EvidenceRound,
// never a second independent condition.
var EvidenceSourceNative = Event{
	ID:                 "graphrank.evidence_source_native",
	Msg:                "context fabric resolution trace: evidence source native (shadow widening)",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- the SAME gating EvidenceRound's own call site carries (traceSourceNativeBinds is called from inside RunShadowEvidenceRound, past the same axis/scope gates); CertifyAbsent asserts the case where the round never reaches that point.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"evidence_source_native"}},
		{Key: "source_native_match_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "source_native_any_resolved", Type: FieldBool, Presence: PresenceRequired},
	},
}

// sourceNativeGrammar is the closed vocabulary ShadowSourceNativeGrammar
// carries -- the five FIXED grammar names
// chaos3899_source_native_grammar.go's own sourceNativeGrammarRegistry
// declares (Grammar is always `entry.name`, never a matched literal).
var sourceNativeGrammar = []string{
	"provider_qualified_name", "repo_slug", "branch_name_keyword", "branch_name_prefix", "commit_sha",
}

// EvidenceSourceNativeProbe is the Debug per-match line (graphrank/tracer.go,
// case "evidence_source_native_probe", CHAOS-3918) -- ONE per-match
// receipt, mirroring EvidenceProbe's own "per-kind, never aggregated"
// cardinality one level down to "per grammar match". This specification's
// ninth MultiplicityBoundedManyPerPass event: bounded per call by
// len(binds), the SAME slice EvidenceSourceNative's own
// source_native_match_count already counts, self-carrying Index/Total
// (CHAOS-5636).
var EvidenceSourceNativeProbe = Event{
	ID:                 "graphrank.evidence_source_native_probe",
	Msg:                "context fabric resolution trace: evidence source native probe (shadow widening)",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per resolveSubjects call by len(binds), the SAME count EvidenceSourceNative's own source_native_match_count reports for the same call -- self-carried index/total.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"evidence_source_native_probe"}},
		{Key: "source_native_grammar", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: sourceNativeGrammar},
		{Key: "source_native_resolved", Type: FieldBool, Presence: PresenceRequired},
		{
			Key: "source_native_kind", Type: FieldString, Presence: PresenceRequired,
			// Open vocabulary: a contextfabric.SubjectKind token, or "" when
			// source_native_resolved is false.
		},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
	},
}

// survivorVerdict is the closed vocabulary SurvivorVerdict carries --
// chaos3896_slice_b_presentation.go's own two verdict names.
var survivorVerdict = []string{"neutral", "eliminated"}

// SliceBSurvivorVerdict is the Debug per-candidate line
// (graphrank/tracer.go, case "slice_b_survivor_verdict", CHAOS-4088)
// reporting SurvivorsFirstOrder's own candidateSurvivorVerdict for each
// candidate in the FINAL list -- this specification's tenth
// MultiplicityBoundedManyPerPass event, bounded by len(ordered) (known
// before its own loop starts, the SAME count the sibling summary's own
// candidate_count reports), self-carrying Index/Total (CHAOS-5636).
var SliceBSurvivorVerdict = Event{
	ID:                 "graphrank.slice_b_survivor_verdict",
	Msg:                "context fabric resolution trace: slice b survivor verdict",
	Level:              LevelDebug,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded per resolveSubjects call by len(ordered), the SAME count SliceBSurvivorVerdictSummary's own candidate_count reports for the same call -- self-carried index/total; zero lines whenever attestation.Reason == ReasonBudgetExhausted (the function's own early return before either loop runs).",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"slice_b_survivor_verdict"}},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "survivor_verdict", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: survivorVerdict},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
	},
}

// SliceBSurvivorVerdictSummary is the Info once-per-call line
// (graphrank/tracer.go, case "slice_b_survivor_verdict",
// SurvivorVerdictSummary==true) folding SurvivorsFirstOrder's own call
// into one aggregate -- emitted only when len(ordered) > 0 ("silence means
// never reached", the same convention IdentityGateSummary's own doc
// comment uses), hence MultiplicityZeroOrOnePerRequest.
var SliceBSurvivorVerdictSummary = Event{
	ID:                 "graphrank.slice_b_survivor_verdict_summary",
	Msg:                "context fabric resolution trace: slice b survivor verdict summary",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per resolveSubjects call -- withheld entirely when SurvivorsFirstOrder's own candidate list is empty; CertifyAbsent asserts that case.",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"slice_b_survivor_verdict"}},
		{Key: "candidate_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "neutral_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "eliminated_count", Type: FieldInt, Presence: PresenceRequired},
		{
			Key: "eliminated_ids", Type: FieldStringSlice, Presence: PresenceRequired,
			// Open vocabulary: canonical ids, capped at traceSummaryIDCap.
		},
	},
}

// All is every event this specification declares. Generate() and the
// certification runner both range over exactly this slice -- neither
// maintains a second list.
// SemanticStatePersistence is the Save-site decision about one result's
// persisted reading: whether a snapshot was written, and when it was not, the
// closed reason and the bound it breached.
//
// Declared HERE and not only emitted, because the certifier is what makes a
// line's shape a promise rather than a habit: the 30-member `state` group
// carries the whole reading, and it is declared against the SAME member list
// the continuation decision's two readings use, so the three groups cannot
// drift apart.
var SemanticStatePersistence = Event{
	ID:                 "contextfabric.semantic_state_persistence",
	Msg:                "context fabric semantic state persistence",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per Save, from the engine's single save site; a request that never reaches Save emits none.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "result_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the ancestry parent, or empty on a first turn.
		{Key: "parent_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "site", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.SemanticStatePersistenceLineVocabulary("site")},
		{Key: "decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.SemanticStatePersistenceLineVocabulary("decision")},
		{Key: "absence", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.SemanticStatePersistenceLineVocabulary("absence")},
		{Key: "oversized_bound", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.SemanticStatePersistenceLineVocabulary("oversized_bound")},
		{Key: "encoded_bytes", Type: FieldInt, Presence: PresenceRequired},
		{Key: "encoded_cap", Type: FieldInt, Presence: PresenceRequired},
		{Key: "state", Type: FieldObject, Presence: PresenceRequired, Fields: semanticStateGroupFields},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

var All = []Event{
	RankedCutSummary, AnchorSlotDisplaced, DecisionSummary, Search, KindOfferWithheld,
	Corroboration, CorroborationSummary, ReservedKindAdmitted, OfferPool, OfferPoolSummary,
	Decision, SearchQuestion, AliasLookup, AnchorPool, KindCoverageFloor, ConfirmedKindRescue,
	IdentityUniverse, KindHintSearch, ExactNameSearch, AnchorOffer,
	AnchorKindWithheld, AnchorKindWithheldSummary, WindowContinuationDecision,
	// CHAOS-5636: KindOffer/ConfirmedKindScope
	// close out the two remaining "(only)" events; LowPopulationKindScope/
	// IdentityGate/SliceBSurvivorVerdict each contribute a detail+summary
	// pair; the evidence_census family (EvidenceRound/EvidenceProbe/
	// EvidenceCensusCommit/EvidenceSourceNative/EvidenceSourceNativeProbe)
	// closes CHAOS-3899/CHAOS-3896's own remaining trace surface.
	KindOffer, ConfirmedKindScope,
	LowPopulationKindScope, LowPopulationKindScopeSummary,
	IdentityGate, IdentityGateSummary,
	EvidenceRound, EvidenceProbe, EvidenceCensusCommit, EvidenceSourceNative, EvidenceSourceNativeProbe,
	SliceBSurvivorVerdict, SliceBSurvivorVerdictSummary,
	SemanticStatePersistence,
}
