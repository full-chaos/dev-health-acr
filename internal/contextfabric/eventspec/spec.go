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

// declaredKindRescueState is the CLOSED vocabulary chaos5388_declared_kind_rescue.go
// already ships (unchanged by this ticket -- "no schema field names or
// additional outcome tokens are minted by this amendment"). All FIVE
// states (round r2's P1: "ran_matched_survived" was omitted from this
// list -- the real resolver path emits it, so it certified successfully
// against an incomplete vocabulary once nested validation was added; fixed
// by completing the list against chaos5388_declared_kind_rescue.go's own
// five constants, not by inventing anything new).
var declaredKindRescueState = []string{
	"not_run",
	"ran_matched_zero",
	"ran_matched_then_dropped",
	"ran_matched_then_cut",
	"ran_matched_survived",
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
		{Key: "stage", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"anchor_slot_displaced"}},
		{Key: "subject_kind", Type: FieldString, Presence: PresenceRequired},
		{Key: "subject_canonical_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_slot_reserved", Type: FieldString, Presence: PresenceRequired},
		{Key: "anchor_slot_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"receipt", "confirmed_anchor", "none"}},
		{Key: "anchor_slot_displaced", Type: FieldInt, Presence: PresenceRequired},
		{Key: "pool_truncated_n", Type: FieldInt, Presence: PresenceRequired},
	},
}

// All is every event this specification declares. Generate() and the
// certification runner both range over exactly this slice -- neither
// maintains a second list.
var All = []Event{RankedCutSummary, AnchorSlotDisplaced}
