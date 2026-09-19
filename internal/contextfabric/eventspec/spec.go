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

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// tokenStrings converts one of this repository's many closed-vocabulary
// arrays (a fixed-size array of a ~string type, exactly what every
// contextfabric/contracts-v1 "XVocabulary() [N]X" accessor returns) into
// this package's own ClosedVocabulary shape, in the array's own declared
// order. It never retypes a vocabulary member -- every value still comes
// from the ONE array the producer package declares -- so a member added
// there reaches a field's ClosedVocabulary without a second, independently
// typed list here. Mirrors contextfabric's own unexported tokenStrings,
// which the producer side already uses for the same purpose.
func tokenStrings[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

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
	//
	// THE SCOPE IS THE EVENT'S OWN Attribution, NOT THE WORD "REQUEST".
	// certify.Certify gathers the lines of one ATTEMPT -- every declared
	// Attribution field -- and refuses more than one line in that attempt.
	// An event attributed by "request_id" alone therefore does mean one line
	// per call; an event whose Attribution is compound means one line per
	// compound scope, and a call that runs that scope twice legitimately
	// carries two lines. AnswerDisplay ("request_id", "surface") and
	// AnchorBindingTransition ("org_id", "result_id", "site") are the
	// compound cases: each of their extra Attribution fields is the
	// discriminator a reader joins on, and each is a required field with a
	// closed vocabulary where one is enumerable.
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
	{Key: "subject_member_qualifier", Type: FieldString, Presence: PresenceConditional, Applicability: "written when present=true"},
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
	{Key: "extension_members", Type: FieldStringSlice, Presence: PresenceConditional, Applicability: "written when present=true; the names of the snapshot's extension members, sorted, empty when none; never their values"},
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
// per term searched in a resolveSubjects call -- CHAOS-5517's first
// MultiplicityBoundedManyPerPass event: a resolution can search anywhere
// from zero to several terms, bounded by the number of terms THAT REQUEST
// searches.
//
// The bound is REQUEST-WIDE, not per-pass, and this event declares no "pass"
// field to make it otherwise: certify groups its lines by request_id alone.
// A single-subject resolution searches its own flat terms list once, so the
// bound is that list's length. A two-operand comparison searches each
// operand's terms in its own pass, so the bound is the sum over the operand
// slots and each pass numbers itself from where the previous one stopped --
// a pass restarting at index 1 would collide with the pass before it.
var Search = Event{
	ID:                 "graphrank.search",
	Msg:                "context fabric resolution trace: search",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by the number of terms this REQUEST searches -- its own flat terms list for a single-subject resolution, the sum over the operand slots for a comparison. Total on every line is that number, Index is this line's 1-based position among every search line the request emits.",
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
			// CHAOS-5825: WHY anchor_pool_kind_scope is "none" -- distinct
			// upstream defects (no receipt kind and nothing carried, vs. a
			// carried anchor the confirmed-kind gate withheld, vs. a carried
			// anchor ScopeAnchorRetrievalKind itself refused, vs. the
			// decision never running on this exit at all) render
			// identically on scope/source alone. "not_applicable" when a
			// scope WAS admitted; "not_evaluated" for an exit before the
			// decision ran, never a claim its inputs were proven empty.
			Key: "anchor_pool_kind_scope_none_reason", Type: FieldString, Presence: PresenceRequired,
			ClosedVocabulary: []string{"not_applicable", "no_receipt_kind_no_confirmed_anchor", "confirmed_anchor_no_confirmed_kind", "confirmed_anchor_kind_rejected", "not_evaluated"},
		},
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
		// The axis decision's authority: the parent's relation to the window
		// receipt, and whether the window was confirmed for this identical
		// question (read apart from the window-only shape).
		{Key: "parent_reference", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.ContinuationDecisionLineVocabulary("parent_reference")},
		{Key: "question_window_confirmed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// RememberedWindowAxis is the once-per-request Info line
// (telemetry.go, SlogEngineTelemetry.RecordRememberedWindowAxis) reporting the
// axis decision for a turn whose window the confirmed-need ledger remembered
// from its parent. Closed vocabularies are read from production
// (contextfabric.RememberedWindowAxisLineVocabulary).
var RememberedWindowAxis = Event{
	ID:                 "contextfabric.remembered_window_axis",
	Msg:                "context fabric remembered window axis",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per Investigate call, emitted only when the confirmed-need ledger applied a remembered window and interpretation produced an answerable-or-not fresh time.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the parent result the remembered window came from.
		{Key: "source_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "carrier_read", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RememberedWindowAxisLineVocabulary("carrier_read")},
		{Key: "interpreted_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RememberedWindowAxisLineVocabulary("interpreted_axis")},
		{Key: "carried_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RememberedWindowAxisLineVocabulary("carried_axis")},
		{Key: "decided_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RememberedWindowAxisLineVocabulary("decided_axis")},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RememberedWindowAxisLineVocabulary("outcome")},
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

// RequirementOutcomeTransition (CHAOS-5737) is the Info line for ONE published
// requirement whose assembled outcome differs from the derivation's prediction.
//
// EVERY SURFACE THAT SERVES A DOCUMENT EMITS IT, through one construction
// (contextfabric.RequirementOutcomeTransitionLogArgs): the engine's own serving
// exits via RecordRequirementOutcomeTransition, and the stored-read route,
// which never reaches the engine and whose response the MCP
// investigation_result tool forwards. What decides the line is the document
// served, never the path it took to a reader.
//
// A request emits one line per such requirement and none when every assembled
// account matches its prediction, so the multiplicity is bounded-many with no
// pass: the line describes the served document, which exists once per request.
var RequirementOutcomeTransition = Event{
	ID:                 "contextfabric.requirement_outcome_transition",
	Msg:                "context fabric requirement outcome transition",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "bounded by the number of requirements the served answer plan publishes; total on every line is the request's transition count and index is this line's 1-based position among them, in plan order.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the requirement coordinate and its three parts, each drawn from
		// a vocabulary the contracts package owns (obligation, subject role,
		// subject kind).
		{Key: "requirement", Type: FieldString, Presence: PresenceRequired},
		{Key: "obligation", Type: FieldString, Presence: PresenceRequired},
		{Key: "role", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "predicted", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RequirementOutcomeTransitionLineVocabulary("predicted")},
		{Key: "predicted_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RequirementOutcomeTransitionLineVocabulary("predicted_reason")},
		{Key: "assembled_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RequirementOutcomeTransitionLineVocabulary("assembled_outcome")},
		{Key: "cause", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.RequirementOutcomeTransitionLineVocabulary("cause")},
		// Open: the assembled row's own wire causes, each from the contracts
		// vocabulary that owns it and validated there, or `none`.
		{Key: "cause_coverage", Type: FieldString, Presence: PresenceRequired},
		{Key: "cause_overrun", Type: FieldString, Presence: PresenceRequired},
		{Key: "cause_narrowing", Type: FieldString, Presence: PresenceRequired},
		{Key: "served", Type: FieldInt, Presence: PresenceRequired},
		{Key: "declared", Type: FieldInt, Presence: PresenceRequired},
		{Key: "served_fact_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "member_set_resolved", Type: FieldBool, Presence: PresenceRequired},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// CompletenessAuthority (CHAOS-5743) is the Info line the outcome-derivation
// completeness authority emits for every investigation that reaches
// assembly with a plan.
//
// EVERY SURFACE THAT SERVES A DOCUMENT EMITS IT, through one construction
// (contextfabric.CompletenessAuthorityLogArgs): the engine's own serving
// exit (SlogEngineTelemetry.RecordCompletenessAuthority, called from
// finalizeServed, the one point every serving path -- fresh, reused, or a
// veto/refusal/clarification exit -- is downstream of) and the stored-read
// route, which never reaches the engine and whose response the MCP
// investigation_result tool forwards.
//
// `model_status`/`disposition` are on every line regardless of disposition;
// `server_state`/`derived` carry a real value only for the `answer`
// disposition, empty/false otherwise, so a reader can tell "not an answer"
// apart from "an answer with no semantic state". `disagreed`/`would_flip`
// and `direction` are populated unconditionally by the measurement,
// independent of either gated flip's setting -- see
// contextfabric.CompletenessAuthorityObservation's own doc comment.
//
// The `deciding_*` fields and `outcome_rows_total`/`outcome_rows_<token>`
// (CHAOS-5744) close the completeness-authority line's own observability
// gap: which requirement/stage/outcome/cause decided ServerState, and how
// many rows of each outcome the derivation read to decide it -- both empty/
// zero on a line with no outcome-derived basis or a decision the
// read-evaluation pass alone made (contextfabric.decidingRequirementOutcomeRow's
// own doc comment). `claimed_facts_<kind>` is the served half of
// reader-level completeness: how many claimed facts of each closed FactKind
// reached the document, joinable against the existing "context fabric fact
// read" line's per-kind read counts by request_id and kind.
var CompletenessAuthority = Event{
	ID:                 "contextfabric.completeness_authority",
	Msg:                "context fabric completeness authority",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per investigation that reaches finalizeServed or the stored-read route, from the shared measurement site; a request that reaches neither emits none.",
	Fields: append([]Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "model_status", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("model_status")},
		{Key: "disposition", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("disposition")},
		{Key: "basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("basis")},
		{Key: "server_state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("server_state")},
		{Key: "derived", Type: FieldBool, Presence: PresenceRequired},
		{Key: "disagreed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "would_flip", Type: FieldBool, Presence: PresenceRequired},
		{Key: "direction", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("direction")},
		// Open: the derivation series identifier, expected to gain new
		// values as the series is amended.
		{Key: "version", Type: FieldString, Presence: PresenceRequired},
		{Key: "deciding_requirement", Type: FieldString, Presence: PresenceRequired},
		{Key: "deciding_stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("deciding_stage")},
		{Key: "deciding_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("deciding_outcome")},
		{Key: "deciding_cause_overrun", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("deciding_cause_overrun")},
		{Key: "deciding_cause_coverage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("deciding_cause_coverage")},
		{Key: "deciding_cause_narrowing", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CompletenessAuthorityLineVocabulary("deciding_cause_narrowing")},
		{Key: "deciding_read_evaluation_gap", Type: FieldBool, Presence: PresenceRequired},
		{Key: "outcome_rows_total", Type: FieldInt, Presence: PresenceRequired},
	},
		append(completenessAuthorityOutcomeRowFields(),
			append(completenessAuthorityClaimedFactFields(),
				Field{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
			)...,
		)...,
	),
}

// completenessAuthorityOutcomeRowFields declares one int field per member of
// contextfabric.CompletenessAuthorityOutcomeTokens(), in that function's own
// order, matching contextfabric.CompletenessAuthorityLogArgs's own loop --
// the same derive-from-the-vocabulary discipline this package already
// applies elsewhere so a new outcome token cannot reach the line without a
// spec field to declare it.
func completenessAuthorityOutcomeRowFields() []Field {
	tokens := contextfabric.CompletenessAuthorityOutcomeTokens()
	fields := make([]Field, 0, len(tokens))
	for _, token := range tokens {
		fields = append(fields, Field{Key: "outcome_rows_" + token, Type: FieldInt, Presence: PresenceRequired})
	}
	return fields
}

// completenessAuthorityClaimedFactFields declares one int field per member
// of contextfabric.CompletenessAuthorityClaimedFactKinds(), matching
// CompletenessAuthorityLogArgs's own loop.
func completenessAuthorityClaimedFactFields() []Field {
	kinds := contextfabric.CompletenessAuthorityClaimedFactKinds()
	fields := make([]Field, 0, len(kinds))
	for _, kind := range kinds {
		fields = append(fields, Field{Key: "claimed_facts_" + kind, Type: FieldInt, Presence: PresenceRequired})
	}
	return fields
}

// SynthesisRetrySelection is the Info line emitted when the first synthesis
// measurement selects a bounded cohort for one retry. It is emitted before
// the retry executes, so the three retry outcome fields are explicit false
// values on this line; the later plan-narrowing line reports whether that
// retry ran, fitted, or failed. The rest of the fields intentionally reuse
// PlanNarrowingEvent's measurement vocabulary so the selected cohort can be
// compared with the measured result without a second, drifting field set.
var SynthesisRetrySelection = Event{
	ID:                 "contextfabric.synthesis_retry_selection",
	Msg:                "context fabric synthesis retry selected",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per request, emitted when the first synthesis measurement selects a bounded retry cohort and before the retry executes; requests that fit or decline a retry emit none.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "family", Type: FieldString, Presence: PresenceRequired},
		{Key: "family_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "stage", Type: FieldString, Presence: PresenceRequired},
		{Key: "basis", Type: FieldString, Presence: PresenceRequired},
		{Key: "basis_observed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "before", Type: FieldInt, Presence: PresenceRequired},
		{Key: "after", Type: FieldInt, Presence: PresenceRequired},
		{Key: "groups", Type: FieldBool, Presence: PresenceRequired},
		{Key: "overrun", Type: FieldString, Presence: PresenceRequired},
		{Key: "measured_items", Type: FieldInt, Presence: PresenceRequired},
		{Key: "predicted_items", Type: FieldInt, Presence: PresenceRequired},
		{Key: "attribution_global", Type: FieldInt, Presence: PresenceRequired},
		{Key: "attribution_member", Type: FieldInt, Presence: PresenceRequired},
		{Key: "attribution_group", Type: FieldInt, Presence: PresenceRequired},
		{Key: "attribution_multi_group", Type: FieldInt, Presence: PresenceRequired},
		{Key: "measured_bytes", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_items", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_serialized_bytes", Type: FieldInt, Presence: PresenceRequired},
		{Key: "retry_attempted", Type: FieldBool, Presence: PresenceRequired},
		{Key: "retry_fit", Type: FieldBool, Presence: PresenceRequired},
		{Key: "retry_failed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "refusal_planned", Type: FieldBool, Presence: PresenceRequired},
		{Key: "deadline_reserved", Type: FieldBool, Presence: PresenceRequired},
		{Key: "retry_declined", Type: FieldString, Presence: PresenceRequired},
		{Key: "narrower_continuation_axis", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome_reduction_applied", Type: FieldBool, Presence: PresenceRequired},
		{Key: "outcome_reduction_inner_fit", Type: FieldBool, Presence: PresenceRequired},
		{Key: "outcome_items_served", Type: FieldInt, Presence: PresenceRequired},
		{Key: "outcome_items_declared", Type: FieldInt, Presence: PresenceRequired},
		{Key: "outcome_completeness_state", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome_reduction_declined", Type: FieldString, Presence: PresenceRequired},
		{Key: "ledger_status", Type: FieldString, Presence: PresenceRequired},
		{Key: "quota_availability", Type: FieldString, Presence: PresenceRequired},
		{Key: "quota_group_allowance", Type: FieldInt, Presence: PresenceRequired},
		{Key: "quota_groups_granted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "quota_groups_measured", Type: FieldInt, Presence: PresenceRequired},
		{Key: "quota_groups_over_allowance", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// SynthesisZeroClaimRedraw* are the closed values of synthesis_input's
// zero_claim_redraw field: what the rule "a validated draft that claims no fact
// although the input's facts carry evidence references is drawn once more"
// decided for one synthesize call.
const (
	// SynthesisZeroClaimRedrawNotEvaluated: no draw validated, so the rule had
	// no draft to judge.
	SynthesisZeroClaimRedrawNotEvaluated = "not_evaluated"
	// SynthesisZeroClaimRedrawNotNeeded: the first validated draft claimed a
	// fact, or the input carried no fact evidence references to claim from.
	SynthesisZeroClaimRedrawNotNeeded = "not_needed"
	// SynthesisZeroClaimRedrawRecovered: the extra draw validated and claimed
	// a fact.
	SynthesisZeroClaimRedrawRecovered = "redrawn_recovered"
	// SynthesisZeroClaimRedrawStillZero: the extra draw validated and still
	// claimed no fact; that draft is the one served.
	SynthesisZeroClaimRedrawStillZero = "redrawn_still_zero"
	// SynthesisZeroClaimRedrawFailed: the extra draw was rejected or failed in
	// transport; the first valid zero-claim draft is the one served.
	SynthesisZeroClaimRedrawFailed = "redraw_failed"
	// SynthesisZeroClaimRedrawDeclinedDeadline: the caller's remaining
	// deadline could not cover the extra draw.
	SynthesisZeroClaimRedrawDeclinedDeadline = "declined_deadline"
	// SynthesisZeroClaimRedrawDeclinedCeiling: the call had already used every
	// draw the re-synthesis ceiling allows.
	SynthesisZeroClaimRedrawDeclinedCeiling = "declined_ceiling"
	// SynthesisZeroClaimRedrawDeclinedSingleDraw: the runtime is configured to
	// draw once per call (the fallback leg), so no extra draw is taken.
	SynthesisZeroClaimRedrawDeclinedSingleDraw = "declined_single_draw"
)

var synthesisZeroClaimRedrawVocabulary = []string{
	SynthesisZeroClaimRedrawNotEvaluated, SynthesisZeroClaimRedrawNotNeeded,
	SynthesisZeroClaimRedrawRecovered, SynthesisZeroClaimRedrawStillZero,
	SynthesisZeroClaimRedrawFailed, SynthesisZeroClaimRedrawDeclinedDeadline,
	SynthesisZeroClaimRedrawDeclinedCeiling, SynthesisZeroClaimRedrawDeclinedSingleDraw,
}

// SynthesisInput is the Info line one synthesize model call emits once its
// prompt input is encoded: the shape of what the synthesizer was handed
// (a digest of the encoded input plus counts of what it carries) beside the
// shape of what it returned per draw, so a zero-claim answer can be
// attributed to its input, or cleared of it, from the trace alone. Counts and
// digests only -- no question text, no fact values. The scope is one line per
// (request, encoded input, model): the fallback leg encodes the same input
// under its own model id, so the two never share a scope.
var SynthesisInput = Event{
	ID:                 "contextfabric.synthesis_input",
	Msg:                "context fabric synthesis input",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id", "input_digest", "model_id"},
	BoundedAggregation: "at most one line per synthesize call, emitted when the call has encoded its model input; a call rejected before encoding emits none. Per-draw lists are bounded by the re-synthesis ceiling.",
	Fields: []Field{
		{Key: "org_id_hash", Type: FieldString, Presence: PresenceRequired},
		{Key: "model_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "model_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "prompt_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "input_digest", Type: FieldString, Presence: PresenceRequired},
		{Key: "input_bytes", Type: FieldInt, Presence: PresenceRequired},
		{Key: "facts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "fact_evidence_refs", Type: FieldInt, Presence: PresenceRequired},
		{Key: "fact_evidence_refs_distinct", Type: FieldInt, Presence: PresenceRequired},
		{Key: "paths", Type: FieldInt, Presence: PresenceRequired},
		{Key: "driver_candidates", Type: FieldInt, Presence: PresenceRequired},
		{Key: "cohort_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired},
		{Key: "draws_total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "draw_outcomes", Type: FieldString, Presence: PresenceRequired},
		{Key: "draw_claims", Type: FieldString, Presence: PresenceRequired},
		{Key: "draw_output_digests", Type: FieldString, Presence: PresenceRequired},
		{Key: "claims", Type: FieldInt, Presence: PresenceRequired},
		{Key: "drivers", Type: FieldInt, Presence: PresenceRequired},
		{Key: "evidence_refs", Type: FieldInt, Presence: PresenceRequired},
		{Key: "zero_claim_redraw", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: synthesisZeroClaimRedrawVocabulary},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// WorkItemMembershipS1 is the Info line emitted after the dormant PR2 reader
// completes its one-statement S1 census. Counts are explicit even when zero;
// an unmeasured result is identified by state/reason and never represented by
// a fabricated zero population. The request id is conditional because the
// existing request-id middleware is the producer's source for that field.
var WorkItemMembershipS1 = Event{
	ID:                 "contextfabric.work_item_membership_s1",
	Msg:                "context fabric work item membership s1",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "exactly one Info line per BeginWorkItemMembership S1 call; the call emits one line after the atomic statement completes or fails, with fixed census and response caps carried on the line.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"exact", "floor", "unmeasured"}},
		{Key: "reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"", "s1_error", "read_limit_exceeded", "cancelled", "excluded_provider", "zero_authorized_overflow", "identity_omitted"}},
		{Key: "population_measured", Type: FieldBool, Presence: PresenceRequired},
		{Key: "population_complete", Type: FieldBool, Presence: PresenceRequired},
		{Key: "capped_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "authorized_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "denied_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "grant_organization_wide", Type: FieldBool, Presence: PresenceRequired},
		{Key: "grant_exact_selectors", Type: FieldInt, Presence: PresenceRequired},
		{Key: "grant_owner_selectors", Type: FieldInt, Presence: PresenceRequired},
		{Key: "grant_requested_selectors", Type: FieldBool, Presence: PresenceRequired},
		{Key: "organization_grant_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "direct_repo_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "project_ownership_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "pr_link_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "repo_less_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "repo_less_denied_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "denied_project_less_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "excluded_explicit_text_link_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "excluded_heuristic_link_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "served_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "census_limit", Type: FieldInt, Presence: PresenceRequired},
		{Key: "future_boundary_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "transition_assertion_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_execution_time_seconds", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_rows_to_read", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_memory_usage", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_result_rows", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// WorkItemMembershipGate is the Info line emitted when the bounded per-process
// gate refuses or cancels admission. A successful admission has no line; the
// absence is not a zero measurement because gate occupancy is present on every
// refusal/cancellation line.
var WorkItemMembershipGate = Event{
	ID:                 "contextfabric.work_item_membership_gate",
	Msg:                "context fabric work item membership gate",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one Info line per BeginWorkItemMembership admission attempt; the line exists only for refusal or cancellation and carries the bounded in-flight and queue occupancy.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"refused", "context_canceled", "deadline_too_short", "owner_closed", "owner_lease_conflict"}},
		{Key: "in_flight", Type: FieldInt, Presence: PresenceRequired},
		{Key: "queued", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_in_flight", Type: FieldInt, Presence: PresenceRequired},
		{Key: "queue_capacity", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// WorkItemReuse is the one tuple decision after stored clarification handling.
var WorkItemReuse = Event{
	ID:                 "contextfabric.work_item_reuse",
	Msg:                "context fabric work item reuse",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one tuple candidate per request; one decision records the first declined guard or the hit, and the current requested team scope",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"reading_unavailable", "payload_rejected", "census_unavailable", "digest_changed", "anchor_unavailable", "membership_unavailable", "membership_changed", "coverage_invalid", "hit"}},
		{Key: "semantic_read", Type: FieldString, Presence: PresenceRequired},
		{Key: "census_read", Type: FieldString, Presence: PresenceRequired},
		{Key: "requested_team_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// WorkItemStoredServing records tuple authorization and mandatory coverage
// capacity on both stored serving surfaces, before any success is emitted.
var WorkItemStoredServing = Event{
	ID: "contextfabric.work_item_stored_serving", Msg: "context fabric work item stored serving", Level: LevelInfo,
	Multiplicity: MultiplicityZeroOrOnePerRequest, Attribution: []string{"org_id"},
	BoundedAggregation: "one classified stored tuple per request; coverage counts contain no stored text",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "surface", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"reuse", "result_by_id"}},
		{Key: "basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"digest_matched", "digest_changed", "authorization_unverifiable", "universal_grant", "coverage_invalid"}},
		{Key: "semantic_read", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"available", "absent", "malformed", "unsupported_version", "oversized"}},
		{Key: "census_read", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"available", "absent", "malformed", "unsupported_version"}},
		{Key: "coverage_details_before", Type: FieldInt, Presence: PresenceRequired},
		{Key: "coverage_reasons_before", Type: FieldInt, Presence: PresenceRequired},
		{Key: "coverage_details_after", Type: FieldInt, Presence: PresenceRequired},
		{Key: "coverage_reasons_after", Type: FieldInt, Presence: PresenceRequired},
		{Key: "coverage_bound", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when request context carries request ID"},
	},
}

// AnswerDisplay records one executed bounded projection at its caller.
var AnswerDisplay = Event{
	ID: "contextfabric.answer_display", Msg: "context fabric answer display", Level: LevelInfo,
	Multiplicity: MultiplicityZeroOrOnePerRequest, Attribution: []string{"request_id", "surface"},
	BoundedAggregation: "one line after bounded projection validation succeeds and before response delivery; canonical-only retrieval and requests rejected before a validated projection emit none; markdown_rendered distinguishes API projection from MCP rendering",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "surface", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"result_by_id", "investigate_question"}},
		{Key: "canonical_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "projected_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "canonical_eligible_facts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "projected_facts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "facts_omitted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "members_omitted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "evidence_omitted", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_facts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "max_evidence", Type: FieldInt, Presence: PresenceRequired},
		{Key: "floor_counts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "recorded_counts", Type: FieldInt, Presence: PresenceRequired},
		{Key: "count_basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"absent", "floor", "recorded", "mixed"}},
		{Key: "projection_truncated", Type: FieldBool, Presence: PresenceRequired},
		{Key: "markdown_rendered", Type: FieldBool, Presence: PresenceRequired},
		{Key: "markdown_truncated", Type: FieldBool, Presence: PresenceRequired},
	},
}

// RetainedRankingAccounting reports a serving decision made from recorded
// member qualification. Counts describe the retained set, never the lost
// original population, and RowAdded never means a new ranking executed.
var RetainedRankingAccounting = Event{
	ID:                 "contextfabric.retained_ranking_accounting",
	Msg:                contextfabric.RetainedRankingAccountingLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityBoundedManyPerPass,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "one per served computed ranking requirement in the stored plan; index and total delimit the bounded request set",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "requirement", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "existing_row", Type: FieldBool, Presence: PresenceRequired},
		{Key: "qualification_recorded", Type: FieldBool, Presence: PresenceRequired},
		{Key: "row_added", Type: FieldBool, Presence: PresenceRequired},
		{Key: "assembled_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, contextfabric.RequirementOutcomeTransitionLineVocabulary("assembled_outcome")...)},
		{Key: "retained_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "qualified", Type: FieldInt, Presence: PresenceRequired},
		{Key: "provisional", Type: FieldInt, Presence: PresenceRequired},
		{Key: "insufficient_evidence", Type: FieldInt, Presence: PresenceRequired},
		{Key: "not_applicable", Type: FieldInt, Presence: PresenceRequired},
		{Key: "unrecorded_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "index", Type: FieldInt, Presence: PresenceRequired},
		{Key: "total", Type: FieldInt, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
	},
}

// WorkItemTupleAdmission is the SETTLED (enforced) work-item tuple
// admission decision, emitted at the one point the obligation strip
// itself runs -- engine.go, immediately after the last of several
// possible family-reading tighten calls decides the admission's final
// value. It is the enforced counterpart to the interpretation-time frame-
// validation line's predicted_stripped_obligations field, which is
// stamped before that later reading is known and can therefore disagree
// with this line on a turn whose family reading changes between the two.
//
// Fires only for a frame this arm is structurally concerned with
// (children_of_scope over work_item), whichever way admission settled: a
// refusal -- including one an earlier, heuristic reading of the SAME turn
// had provisionally promoted -- logs admitted=false and an empty
// stripped_obligations, never a missing line, so the mutation's absence
// is as observable as its presence.
var WorkItemTupleAdmission = Event{
	ID:                 "contextfabric.work_item_tuple_admission",
	Msg:                "context fabric work item tuple admission settled",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one line per request, emitted only when the frame is children_of_scope over work_item; every other frame emits none.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "admitted", Type: FieldBool, Presence: PresenceRequired},
		{Key: "stripped_obligations", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: contextfabric.WorkItemTupleAdmissionStrippedObligationsVocabulary()},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// WorkItemAuthorizationGap is the settled decision to disclose work-item
// members the principal may not read. It carries the census the decision was
// made from and the shape served, so a measured-but-denied project is never
// read as an empty one.
var WorkItemAuthorizationGap = Event{
	ID:                 "contextfabric.work_item_authorization_gap",
	Msg:                "context fabric work item authorization gap",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one line per fresh work-item tuple request, emitted only when the measured census has denied members; counts and closed states only.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"none_authorized", "partially_authorized"}},
		{Key: "census_state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"exact", "floor"}},
		{Key: "observed_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "authorized_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "denied_population", Type: FieldInt, Presence: PresenceRequired},
		{Key: "served_status", Type: FieldString, Presence: PresenceRequired},
		{Key: "served_members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "limitation_disclosed", Type: FieldBool, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// arrayTokens is tokenStrings' own counterpart for a "XVocabulary() [N]X"
// accessor: those return an array BY VALUE, which Go will not let a caller
// slice directly off the call result (unaddressable), so this copies it
// into an addressable local first. Still the ONE canonical array in every
// case -- copying is not retyping.
func arrayTokens[T ~string](values []T) []string { return tokenStrings(values) }

// The reusable vocabulary slices FrameValidation's own fields below share --
// each one derived from the ONE canonical array its producer package
// already declares, never retyped by hand here.
var (
	frameValidationOutcomeArr      = contextfabric.FrameValidationOutcomeVocabulary()
	frameValidationOutcomeTokens   = arrayTokens(frameValidationOutcomeArr[:])
	frameValidationPhaseArr        = contextfabric.FrameValidationPhaseVocabulary()
	frameValidationPhaseTokens     = arrayTokens(frameValidationPhaseArr[:])
	frameFailureDetailArr          = contextfabric.FrameFailureDetailVocabulary()
	frameFailureDetailTokens       = arrayTokens(frameFailureDetailArr[:])
	subjectExpressionKindArr       = contextfabric.SubjectExpressionKindVocabulary()
	subjectExpressionKindTokens    = arrayTokens(subjectExpressionKindArr[:])
	investigationGoalArr           = contextfabric.InvestigationGoalVocabulary()
	investigationGoalTokens        = arrayTokens(investigationGoalArr[:])
	cohortDiscoverabilityArr       = contextfabric.CohortDiscoverabilityVocabulary()
	cohortDiscoverabilityTokens    = arrayTokens(cohortDiscoverabilityArr[:])
	investigationShapeArr          = contractsv1.ContextFabricInvestigationShapeVocabulary()
	investigationShapeTokens       = arrayTokens(investigationShapeArr[:])
	groupAxisDecisionArr           = contextfabric.GroupAxisDecisionVocabulary()
	groupAxisDecisionTokens        = arrayTokens(groupAxisDecisionArr[:])
	frameRepairDecisionArr         = contextfabric.FrameRepairDecisionVocabulary()
	frameRepairDecisionTokens      = arrayTokens(frameRepairDecisionArr[:])
	frameRepairNameArr             = contextfabric.FrameRepairNameVocabulary()
	frameRepairNameTokens          = arrayTokens(frameRepairNameArr[:])
	frameRepairTermsMatchArr       = contextfabric.FrameRepairTermsMatchVocabulary()
	frameRepairTermsMatchTokens    = arrayTokens(frameRepairTermsMatchArr[:])
	contextFabricSubjectKindArr    = contractsv1.ContextFabricSubjectKindVocabulary()
	contextFabricSubjectKindTokens = arrayTokens(contextFabricSubjectKindArr[:])
	// The interpretation-boundary hint/slot kind fields (chaos5390_interpretation_boundary.go)
	// render a subject-kind token through closedKindToken PLUS the boundary's
	// own explicit absence tokens -- a hint slot (requested_*) can read
	// "absent" (no hint stated) or "unrecognized" (sanitizer dropped it); a
	// frame slot (proposed_*) can read "not_applicable" (the variant has no
	// such slot) or "unset" (the slot exists and is empty), beside the same
	// "unrecognized". Both add "unclassified" for a kind outside the
	// published registry (closedKindToken's own fallback).
	frameValidationRequestedHintKindTokens = append([]string{"absent", "unrecognized", "unclassified"}, contextFabricSubjectKindTokens...)
	frameValidationProposedSlotKindTokens  = append([]string{"not_applicable", "unset", "unrecognized", "unclassified"}, contextFabricSubjectKindTokens...)
)

// frameInvariantTokens is the closed vocabulary of invariant identifiers,
// in the SAME evaluation order contextfabric.FrameInvariantSpecs declares
// (the order RecordFrameValidation's own "first failure in table order"
// rule depends on) -- projected from that one registry rather than
// retyped, so a twentieth invariant reaches this declaration without an
// edit here.
func frameInvariantTokens() []string {
	specs := contextfabric.FrameInvariantSpecs()
	out := make([]string, len(specs))
	for i, spec := range specs {
		out[i] = string(spec.ID)
	}
	return out
}

// FrameValidation (design §13.6) is the operator's only trace of how a
// proposed frame was validated, repaired or refused: the outcome,
// the failed invariant (first failure in table order), the bounded
// repair's decision, the interpretation boundary (what the model's hints
// requested versus what the frame proposed), and the requirement
// derivation the validated frame produced. See
// contextfabric.SlogEngineTelemetry.RecordFrameValidation's own doc
// comment for why every field below -- including the ones a valid frame
// leaves at its zero value -- reaches this line unconditionally.
var FrameValidation = Event{
	ID:                 "contextfabric.frame_validation",
	Msg:                "context fabric frame validation",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one line per request: RecordFrameValidation fires once per frame that reaches validation (including a valid one), from validateProposedFrame, the one call site the model's interpretation attempt runs through; a request whose answer never reaches a fresh interpretation (a reuse hit, a request that never investigates) emits none.",
	Fields: append([]Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: frameValidationOutcomeTokens},
		// failed_invariant/failed_phase/failure_detail: empty on a valid or
		// repaired-and-passing frame -- FrameValidationResult's own zero
		// value, never omitted (clause 3's missing-vs-zero rule).
		{Key: "failed_invariant", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, frameInvariantTokens()...)},
		{Key: "failed_phase", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, frameValidationPhaseTokens...)},
		{Key: "failure_detail", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, frameFailureDetailTokens...)},
		// proposed_kind: the MODEL'S OWN proposal, before normalization --
		// empty when the model emitted no recognised variant (kind_unset,
		// or a kind outside the published registry).
		{Key: "proposed_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, subjectExpressionKindTokens...)},
		{Key: "proposed_goals", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: investigationGoalTokens},
		// accepted_goals: the goal set the turn actually acts on -- an empty
		// array on a refused frame, never a missing key.
		{Key: "accepted_goals", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: investigationGoalTokens},
		// accepted_judgment: OPEN. Composed from a small closed phrase table
		// keyed on the accepted Goals (requestedJudgmentForGoals), joined
		// with " and " when more than one goal contributes a phrase -- never
		// model text, but not a single closed token either, the same reason
		// `frame_gate` below is open rather than enumerated. "none" when no
		// repair populated it (noneWhenEmpty).
		{Key: "accepted_judgment", Type: FieldString, Presence: PresenceRequired},
		{Key: "ordering_present", Type: FieldBool, Presence: PresenceRequired},
		{Key: "predicted_stripped_obligations", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: contextfabric.WorkItemTupleAdmissionStrippedObligationsVocabulary()},
		{Key: "derived_obligation_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "widened_obligation_count", Type: FieldInt, Presence: PresenceRequired},
		{Key: "shape_diverged", Type: FieldBool, Presence: PresenceRequired},
		{Key: "emitted_shape", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, investigationShapeTokens...)},
		{Key: "derived_shape", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, investigationShapeTokens...)},
		// frame_version: Open, the same "expected to gain new values" reason
		// CompletenessAuthority's own `version` field documents.
		{Key: "frame_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "cohort_discoverability", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, cohortDiscoverabilityTokens...)},
		// frame_gate: OPEN. FrameGate.Observable() renders a composite token
		// -- "passed" / "not_proposed" / "not_evaluated" on their own, or
		// "rejected:<failed_invariant>" / "refused:<refuse_basis>" carrying
		// one of THOSE closed vocabularies embedded after the colon. The
		// prefix is the declared, non-open half; enumerating every
		// combination here would be the invariant/discoverability
		// vocabularies restated a second time under a different key.
		{Key: "frame_gate", Type: FieldString, Presence: PresenceRequired},
		{Key: "refuse_basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, cohortDiscoverabilityTokens...)},
		{Key: "requested_group_hint", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: frameValidationRequestedHintKindTokens},
		{Key: "group_hint_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"none", "model", "frame", "unclassified"}},
		{Key: "requested_member_hint", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: frameValidationRequestedHintKindTokens},
		{Key: "proposed_group_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: frameValidationProposedSlotKindTokens},
		{Key: "proposed_member_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: frameValidationProposedSlotKindTokens},
		{Key: "group_axis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"unset", "unclassified"}, groupAxisDecisionTokens...)},
		{Key: "repair_decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"not_evaluated"}, frameRepairDecisionTokens...)},
		{Key: "repair", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, frameRepairNameTokens...)},
		{Key: "repair_invariant", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, frameInvariantTokens()...)},
		{Key: "repair_kind_before", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, subjectExpressionKindTokens...)},
		{Key: "repair_kind_after", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, subjectExpressionKindTokens...)},
		{Key: "repair_member_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none", "unclassified"}, contextFabricSubjectKindTokens...)},
		{Key: "repair_terms_match", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"not_evaluated"}, frameRepairTermsMatchTokens...)},
		{Key: "repair_attempts", Type: FieldInt, Presence: PresenceRequired},
		// repair_carry_scope_anchor_kind: the anchor kind a repaired
		// proposal states for the downstream consumer a direct proposal of
		// the same shape would have stated it for -- "none" when the
		// repair did not run or carried nothing.
		{Key: "repair_carry_scope_anchor_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none", "unclassified"}, contextFabricSubjectKindTokens...)},
	},
		append(frameValidationRequirementDerivationFields(),
			Field{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
		)...,
	),
}

// frameValidationRequirementDerivationFields declares requirementDerivationLogAttrs'
// own tail, in its exact emission order: the static requirement-summary
// fields, then one int field per member of each closed vocabulary the
// derivation counts over -- matching that function's own per-vocabulary
// loops so a member added to any of the six underlying vocabularies
// reaches this declaration without an edit here.
func frameValidationRequirementDerivationFields() []Field {
	fields := []Field{
		// requirement_derivation_version: Open, the derivation series
		// identifier, matching CompletenessAuthority's own `version` field.
		{Key: "requirement_derivation_version", Type: FieldString, Presence: PresenceRequired},
		{Key: "requirement_cells_derived", Type: FieldInt, Presence: PresenceRequired},
		{Key: "requirement_cells_served", Type: FieldInt, Presence: PresenceRequired},
		{Key: "requirement_cells_unserved", Type: FieldInt, Presence: PresenceRequired},
		{Key: "requirement_accounting", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"ok", "violated"}},
	}
	for _, reason := range contextfabric.RequirementUnavailableReasonVocabulary() {
		fields = append(fields, Field{Key: "requirement_unavailable_" + string(reason), Type: FieldInt, Presence: PresenceRequired})
	}
	fields = append(fields,
		Field{Key: "requirement_computed_population_absent_not_a_population", Type: FieldInt, Presence: PresenceRequired},
		Field{Key: "requirement_computed_population_absent_unresolvable_member_set", Type: FieldInt, Presence: PresenceRequired},
		Field{Key: "requirement_computed_population_absent_non_computed_row", Type: FieldInt, Presence: PresenceRequired},
	)
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		fields = append(fields, Field{Key: "requirement_computed_input_kind_unplanned_" + string(kind), Type: FieldInt, Presence: PresenceRequired})
	}
	for _, quantifier := range contextfabric.CompletionQuantifierVocabulary() {
		fields = append(fields, Field{Key: "requirement_quantifier_" + string(quantifier), Type: FieldInt, Presence: PresenceRequired})
	}
	for _, role := range contextfabric.SubjectRoleVocabulary() {
		fields = append(fields, Field{Key: "requirement_role_" + string(role), Type: FieldInt, Presence: PresenceRequired})
	}
	fields = append(fields, Field{Key: "requirement_computed_rows_with_inputs", Type: FieldInt, Presence: PresenceRequired})
	for _, class := range contextfabric.ComputedStepInputClassVocabulary() {
		fields = append(fields, Field{Key: "requirement_computed_input_class_" + string(class), Type: FieldInt, Presence: PresenceRequired})
	}
	for _, kind := range contractsv1.ContextFabricFactKindVocabulary() {
		fields = append(fields, Field{Key: "requirement_computed_input_kind_" + string(kind), Type: FieldInt, Presence: PresenceRequired})
	}
	for _, execution := range contextfabric.ComputedStepExecutionVocabulary() {
		fields = append(fields, Field{Key: "requirement_computed_step_" + string(execution), Type: FieldInt, Presence: PresenceRequired})
	}
	return fields
}

// cohortKindFulltextDecision is the closed vocabulary for CohortKindFulltext's
// own "decision" field -- see falkorgraph.CohortKindFulltextDecisionVocabulary,
// the real producer's own array this list must never drift from (grep-checked
// by TestCohortKindFulltextDecisionClosedVocabularyMatchesEventspec, the same
// cross-package parity discipline CohortKindCensusDecision already proves for
// its own sibling arm).
var cohortKindFulltextDecision = []string{"ran", "read_failed"}

// CohortKindFulltext is the Info line for falkorgraph's kind-scoped lexical
// arm on one DiscoverContext call: whether a cohort's own declared member
// kind was given its own full-text budget, how many candidates it returned
// and how many of them the merge actually added versus had already seen,
// and whether THAT budget (not the shared, mixed-kind one "cohort kind
// basis" also reports) was itself exhausted -- or, on decision=read_failed,
// that the arm's own read failed and why, with no members/truncated/merge
// count implied (a failed read measured nothing).
//
// WHY THIS IS ITS OWN LINE, not more fields on the pre-existing "cohort kind
// basis"/"cohort kind census" lines (falkorgraph/config.go's
// RecordCohortKindBasis/RecordCohortKindCensus): those two predate this
// package's declaration authority and are grandfathered legacy lines; a
// newly-emitted value is declared here from its first emission, the same
// choice CHAOS-5654's own kind-scoped census made with its dedicated
// "cohort kind census" line rather than folding into "cohort kind basis".
//
// Emitted once per DiscoverContext call that declares a servable cohort
// member kind AND the exact-name/kind-scoped census is not already admitted
// for that kind (falkorgraph/reader.go, right after the kind-scoped
// fulltextSearchNodesForKind call) -- never for a call with no declared
// kind, and never when the census already covers it: whichever census runs
// there (chaos4348ExactNameCandidates for a kind in exactNameKinds, or
// cohortKindCensusCandidates otherwise) already fetches that kind
// exhaustively, so this arm would only duplicate it.
//
// AN AUXILIARY ARM'S OWN FAILURE MUST DEGRADE, NEVER ABORT. decision
// distinguishes a completed read (decision=ran, carrying members/truncated/
// added_by_kind_arm/duplicates_with_general) from a failed one
// (decision=read_failed, carrying only error) -- DiscoverContext never
// returns an error for THIS arm's own failure; it forces the pool-
// truncation input honest instead and keeps serving whatever the other
// arms can. members is the candidate count that query returned
// (post-truncation, matching CohortKindCensus's own convention); truncated
// is exactly the value cohortPoolTruncation's fulltext-arm input
// derives from (a true value here is what makes
// pool_truncation="truncated"/arms="fulltext" honest for this cohort's own
// kind, rather than inherited from the unrelated, mixed-kind general arm).
// added_by_kind_arm/duplicates_with_general are the merge's own delta: how
// many of this arm's own candidates were genuinely new to the cohort versus
// already seen (by the general arm, hop-walk, or an ownership census that
// ran earlier in the same call) -- without them, a regression that changes
// WHICH candidates this arm contributes, or their order, while leaving
// members/truncated unchanged, would be invisible at Info.
var CohortKindFulltext = Event{
	ID:                 "contextfabric.cohort_kind_fulltext",
	Msg:                "context_fabric: cohort kind fulltext",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one line per DiscoverContext call, emitted only when the frame declares a servable cohort member kind and the census is not already admitted for it",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: cohortKindFulltextDecision},
		// CLOSED against contractsv1.ContextFabricSubjectKindVocabulary()
		// (contextFabricSubjectKindTokens, reused rather than a second,
		// independently typed list -- see tokenStrings' own doc comment).
		// The real producer only ever calls this with
		// declaredCohortKindForRouting, itself restricted to the seam's
		// current servable-cohort-kind allow-list (team/project today) --
		// but the DECLARATION is closed against the full SubjectKind
		// vocabulary, not that narrower, still-growing allow-list, so a
		// value outside SubjectKind entirely is what this field's closed
		// vocabulary exists to catch; a value outside the narrower servable-
		// cohort-kind allow-list but still inside SubjectKind is
		// CohortMemberKindForFrame's own concern, not this field's.
		{Key: "member_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextFabricSubjectKindTokens},
		{Key: "members", Type: FieldInt, Presence: PresenceConditional, Applicability: "written when decision=ran"},
		{Key: "truncated", Type: FieldBool, Presence: PresenceConditional, Applicability: "written when decision=ran"},
		{Key: "added_by_kind_arm", Type: FieldInt, Presence: PresenceConditional, Applicability: "written when decision=ran"},
		{Key: "duplicates_with_general", Type: FieldInt, Presence: PresenceConditional, Applicability: "written when decision=ran"},
		{Key: "error", Type: FieldString, Presence: PresenceConditional, Applicability: "written when decision=read_failed"},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

var All = []Event{
	AnswerDisplay,
	RetainedRankingAccounting,
	WorkItemTupleAdmission,
	WorkItemAuthorizationGap,
	RankedCutSummary, AnchorSlotDisplaced, DecisionSummary, Search, KindOfferWithheld,
	Corroboration, CorroborationSummary, ReservedKindAdmitted, OfferPool, OfferPoolSummary,
	Decision, SearchQuestion, AliasLookup, AnchorPool, KindCoverageFloor, ConfirmedKindRescue,
	IdentityUniverse, KindHintSearch, ExactNameSearch, AnchorOffer,
	AnchorKindWithheld, AnchorKindWithheldSummary, WindowContinuationDecision, RememberedWindowAxis,
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
	RequirementOutcomeTransition,
	CompletenessAuthority,
	SynthesisRetrySelection,
	SynthesisInput,
	WorkItemMembershipS1,
	WorkItemMembershipGate,
	WorkItemReuse,
	WorkItemStoredServing,
	CountPopulationScope,
	FrameValidation,
	ConfirmedNeedLedger,
	CohortKindFulltext,
	AnchorBindingTransition,
}

// CountPopulationScope (CHAOS-5775) is the Info line for whether a served
// answer's counted member set is the population its frame asks about: the
// requested population (expression, member kind, requirement), what resolution
// and retrieval measured, the decision, and what the served document then
// states. Built by contextfabric.CountPopulationScopeLogArgs, emitted once per
// served document that owes a count, on the fresh decisive exit and on reuse.
var CountPopulationScope = Event{
	ID:                 "contextfabric.count_population_scope",
	Msg:                contextfabric.CountPopulationScopeLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "at most one per request: one served document, and a document owes at most one count requirement",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the frame's subject expression kind and member kind, empty when
		// no frame records the requested population.
		{Key: "expression_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "member_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "requirement", Type: FieldString, Presence: PresenceRequired},
		{Key: "committed", Type: FieldInt, Presence: PresenceRequired},
		{Key: "committed_anchors", Type: FieldInt, Presence: PresenceRequired},
		{Key: "committed_unbound", Type: FieldInt, Presence: PresenceRequired},
		// Open: the reading's anchor kind and the bound anchor's canonical id,
		// empty when there is none.
		{Key: "anchor_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "candidates", Type: FieldInt, Presence: PresenceRequired},
		{Key: "anchor_candidates", Type: FieldInt, Presence: PresenceRequired},
		// member_source (CHAOS-5783): which graph discovery arm served the
		// resolved member set -- ownership (the anchor's own declared
		// ownership signal) or hop_walk (bounded graph-proximity traversal),
		// not_applicable when no anchor-scoped arm ran (an organization-scope
		// count, an anchor that never resolved, or a reuse backfill).
		{Key: "member_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CohortMemberSourceVocabulary()},
		{Key: "member_set_resolved", Type: FieldBool, Presence: PresenceRequired},
		{Key: "members", Type: FieldInt, Presence: PresenceRequired},
		{Key: "decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: contextfabric.CountPopulationScopeDecisionVocabulary()},
		{Key: "assembled_outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{"none"}, contextfabric.RequirementOutcomeTransitionLineVocabulary("assembled_outcome")...)},
		{Key: "counted", Type: FieldBool, Presence: PresenceRequired},
		{Key: "served", Type: FieldInt, Presence: PresenceRequired},
		{Key: "reused", Type: FieldBool, Presence: PresenceRequired},
		// The minted cardinality claim's own subject, read off the served
		// document -- empty when it carries none. Open: a subject-kind token
		// drawn from contextfabric.SubjectKind, or a free canonical id.
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// The reusable vocabulary slices ConfirmedNeedLedger's own fields below
// share -- each one derived from the ONE canonical array its producer
// package already declares, never retyped by hand here.
var (
	confirmedNeedLedgerOutcomeTokens      = arrayTokens(contextfabric.ConfirmedNeedLedgerOutcomeVocabulary())
	confirmedNeedBasisArr                 = contextfabric.ConfirmedNeedBasisVocabulary()
	confirmedNeedBasisTokens              = arrayTokens(confirmedNeedBasisArr[:])
	confirmedAnchorAgreementArr           = contextfabric.ConfirmedAnchorAgreementVocabulary()
	confirmedAnchorAgreementTokens        = arrayTokens(confirmedAnchorAgreementArr[:])
	contextFabricStructureDispositionArr  = contractsv1.ContextFabricStructureDispositionVocabulary()
	contextFabricStructureDispositionToks = arrayTokens(contextFabricStructureDispositionArr[:])
	captureSkipReasonTokens               = arrayTokens(contextfabric.CaptureSkipReasonVocabulary())
	subjectSubstitutionOutcomeArr         = contextfabric.SubjectSubstitutionOutcomeVocabulary()
	subjectSubstitutionOutcomeTokens      = arrayTokens(subjectSubstitutionOutcomeArr[:])
	subjectSubstitutionOriginArr          = contextfabric.SubjectSubstitutionOriginVocabulary()
	subjectSubstitutionOriginTokens       = arrayTokens(subjectSubstitutionOriginArr[:])
	subjectSubstitutionRememberedArr      = contextfabric.SubjectSubstitutionRememberedCheckVocabulary()
	subjectSubstitutionRememberedTokens   = arrayTokens(subjectSubstitutionRememberedArr[:])
)

// ConfirmedNeedLedger is the per-need confirmation ledger's own trace: the
// admission outcome against the named parent, each member that applied
// with its closed kind and hashed value, each member dropped at reverify
// and why, the subject_anchor axis's own basis/agreement/disposition
// triple, and the capture-gate decision for a later turn to inherit, plus
// why that decision is empty on an exit that never reaches the check. See
// contextfabric.SlogEngineTelemetry.RecordConfirmedNeedLedger's own doc
// comment for what every field discloses and why it is emitted
// unconditionally, on every exit, including the ones that never reach
// resolution at all.
var ConfirmedNeedLedger = Event{
	ID:                 "contextfabric.confirmed_need_ledger",
	Msg:                "context fabric confirmed need ledger",
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"org_id"},
	BoundedAggregation: "exactly one line per Investigate call: recordConfirmedNeedLedger is deferred above the ledger's own resolution, so every request -- on every exit, including one that ends before subject resolution or the capture check ever runs -- produces exactly one line.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: confirmedNeedLedgerOutcomeTokens},
		// Open: the caller-named parent result id, or empty on miss_no_reference.
		{Key: "source_result_id", Type: FieldString, Presence: PresenceRequired},
		// Open at this layer: a comma-joined list of ContextFabricStructureNeedKind
		// members (or "none") the emitter checks one by one against that
		// closed vocabulary -- the joined string itself has no finite
		// vocabulary, the same reasoning WindowContinuationDecision's own
		// conflict_fields field documents.
		{Key: "applied_members", Type: FieldString, Presence: PresenceRequired},
		{Key: "applied_expected_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricSubjectKindTokens...)},
		{Key: "applied_anchor_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricSubjectKindTokens...)},
		// Open: confirmedNeedValueHash's own SHA-256/6-byte hex digest, empty
		// when the member did not apply.
		{Key: "applied_anchor_value_hash", Type: FieldString, Presence: PresenceRequired},
		{Key: "applied_anchor_basis", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: confirmedNeedBasisTokens},
		{Key: "applied_candidate_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricSubjectKindTokens...)},
		{Key: "applied_candidate_value_hash", Type: FieldString, Presence: PresenceRequired},
		{Key: "applied_handle_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricSubjectKindTokens...)},
		{Key: "applied_handle_value_hash", Type: FieldString, Presence: PresenceRequired},
		// Open at this layer: member:reason pairs (member a ContextFabricStructureNeedKind,
		// reason a ConfirmedNeedMemberDropReason), comma-joined, or "none" --
		// each side is closed and checked by observableConfirmedNeedDrops's
		// own caller, the joined string is not enumerated here, same
		// reasoning as applied_members above.
		{Key: "dropped_members", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_agreement", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: confirmedAnchorAgreementTokens},
		{Key: "anchor_disposition", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricStructureDispositionToks...)},
		{Key: "capture_decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextfabric.CountPopulationScopeDecisionVocabulary()...)},
		{Key: "capture_skip_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: captureSkipReasonTokens},
		// The subject-substitution guard's own four-part disclosure: what it
		// decided, what produced this turn's subject, and both identities as
		// kind plus canonical id -- the identity reference the anchor-binding
		// transition line uses, so the two lines join. The four together are
		// what let the decision be rebuilt from this line alone -- the
		// parent's identity, this turn's identity, the evidence class, and the
		// verdict. Identities only; a question's terms never reach this line.
		{Key: "substitution_guard", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: subjectSubstitutionOutcomeTokens},
		// The producer domain, exactly: which channel carried this turn's
		// committed subject into resolution -- a hint redeemed from a
		// prior-subject receipt, a hint the caller's own request carried, or
		// neither, which leaves resolution's own reach over the question.
		// "not_applicable" is the turn that committed no subject at all.
		{Key: "substitution_origin", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: subjectSubstitutionOriginTokens},
		{Key: "substitution_parent_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: append([]string{""}, contextFabricSubjectKindTokens...)},
		// Open: a free canonical id, empty when that side holds no identity.
		{Key: "substitution_parent_id", Type: FieldString, Presence: PresenceRequired},
		// Open: every subject this turn committed, "<kind>:<canonical id>" in
		// commit order, empty when it committed none.
		{Key: "substitution_committed_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		// Open: the result that issued, and the id of, the redeemed receipt
		// that carried the origin's subject; both empty unless
		// substitution_origin is prior_receipt.
		{Key: "substitution_origin_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "substitution_origin_receipt_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the result whose subject the parent identity is -- the named
		// parent, or the result a clarification the guard issued speaks for
		// -- and the result the origin receipt's issuer was issued for when
		// the guard issued it. Empty when that side has none.
		{Key: "substitution_parent_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "substitution_origin_issued_for", Type: FieldString, Presence: PresenceRequired},
		// The remembered subject's re-read: what it found (closed), the
		// verifier's own reason and the context error (open, empty when
		// there is none). Every way the offer is withheld is named here.
		{Key: "substitution_remembered_check", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: subjectSubstitutionRememberedTokens},
		{Key: "substitution_remembered_reason", Type: FieldString, Presence: PresenceRequired},
		{Key: "substitution_remembered_context_error", Type: FieldString, Presence: PresenceRequired},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// AnchorBindingTransition is the Info line for one shadow anchor binding
// decision: the binding the turn started from, what the binder read, the
// binding it decided with its reason, and how that compares with the anchor
// the served state carries. Built by contextfabric.AnchorBindingTransitionLogArgs,
// emitted once per Save from the engine's single save site and once per reuse
// serve.
var AnchorBindingTransition = Event{
	ID:                 "contextfabric.anchor_binding_transition",
	Msg:                contextfabric.AnchorBindingTransitionLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"org_id", "result_id", "site"},
	BoundedAggregation: "exactly one line per attempt, and this event's attempt is its whole Attribution -- (org_id, result_id, site) -- because each Save and each reuse serve decides its own binding and emits its own line while the shadow runs. site is the discriminator a reader joins on: decisive is the one the request's answer is served from, and a request whose decisive Save loses a structure claim saves a second result at the structure_veto site, so one request carries as many lines as it saved results, never two for one result at one site.",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "result_id", Type: FieldString, Presence: PresenceRequired},
		// Open: the parent the request names, empty when it names none.
		{Key: "parent_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "site", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("site")},
		{Key: "evaluation", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("evaluation")},
		{Key: "parent_binding", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("parent_binding")},
		// not_evaluated whenever a parent binding was used: the shadow carries
		// on the parent reference without same-question admission or live
		// re-authorization.
		{Key: "carry_checks", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("carry_checks")},
		{Key: "from_state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("from_state")},
		// Open: subject-kind tokens drawn from contextfabric.SubjectKind and
		// free canonical ids, empty when absent.
		{Key: "from_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "from_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "from_proof", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("from_proof")},
		{Key: "from_reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("from_reason")},
		{Key: "from_origin_result_id", Type: FieldString, Presence: PresenceRequired},
		// The parent binding's own graph epoch, 0 when unbound.
		{Key: "from_graph_epoch", Type: FieldInt, Presence: PresenceRequired},
		{Key: "from_contender_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "from_contender_id", Type: FieldString, Presence: PresenceRequired},
		// The graph epoch of the binding the parent row carries, -1 when no
		// stored binding was read; the stale-parent check compares it with
		// graph_epoch.
		{Key: "parent_graph_epoch", Type: FieldInt, Presence: PresenceRequired},
		// This turn's graph epoch: the epoch an identity it proves stands on.
		{Key: "graph_epoch", Type: FieldInt, Presence: PresenceRequired},
		// Open: a SubjectExpressionKind token, empty with no frame. Only a
		// children_of_scope frame can bind an anchor.
		{Key: "frame_expression_kind", Type: FieldString, Presence: PresenceRequired},
		// Open: the SubjectKind the reading counts, empty when the frame names
		// none. A committed subject of that kind is the population being
		// counted and is never admitted as the anchor, so it decides
		// admission and two turns that differ only here differ here.
		{Key: "frame_member_kind", Type: FieldString, Presence: PresenceRequired},
		// How many anchor terms the frame names; the terms are corpus text and
		// are never published.
		{Key: "anchor_term_count", Type: FieldInt, Presence: PresenceRequired},
		// Open: "<kind>:<canonical id>" for every committed subject one of
		// whose own candidates matched a stated anchor term, empty when none.
		// The match, not the term, is what admits an identity-proven commit.
		{Key: "anchor_term_matched_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		// Open: "<kind>:<canonical id>=<commit basis>" for every committed
		// subject the binder weighed, empty when none.
		{Key: "committed_subjects", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "model_anchor_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "named_expected_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "receipt_anchor_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "receipt_anchor_id", Type: FieldString, Presence: PresenceRequired},
		// Open: "<kind>:<canonical id>" tokens, empty lists when none.
		{Key: "caller_hint_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "proven_anchor_ids", Type: FieldStringSlice, Presence: PresenceRequired},
		{Key: "effective_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "to_state", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("to_state")},
		{Key: "to_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "to_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "proof", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("proof")},
		{Key: "reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("reason")},
		{Key: "origin_result_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "to_graph_epoch", Type: FieldInt, Presence: PresenceRequired},
		{Key: "contender_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "contender_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "persisted", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("persisted")},
		{Key: "shadow_agreement", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("shadow_agreement")},
		{Key: "disagreement_field", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("disagreement_field")},
		{Key: "served_anchor_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "served_anchor_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "served_count_decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("served_count_decision")},
		{Key: "served_count_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "served_count_id", Type: FieldString, Presence: PresenceRequired},
		// The subject-substitution guard's decision for the turn: a contender
		// on a line whose guard fired is one the guard WITHHELD, never one the
		// turn failed to prove. not_evaluated on an exit above the guard.
		{Key: "substitution_guard", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: anchorBindingVocabulary("substitution_guard")},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}

// anchorBindingVocabulary is the producer's closed vocabulary for key plus
// the emitter's fail-closed token for a value outside it.
func anchorBindingVocabulary(key string) []string {
	return append(contextfabric.AnchorBindingTransitionLineVocabulary(key), contextfabric.AnchorBindingUndeclaredToken)
}
