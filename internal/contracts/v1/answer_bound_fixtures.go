package v1

import "time"

// The measured answer fixtures, built BY CONSTRUCTION from one table of the
// validator's own limits.
//
// WHY THIS EXISTS. A hand-built fixture measured the minimum answer size four
// times and was wrong four times: it padded a rune-counted field with ASCII; it
// omitted the interpretation's terms entirely; it carried a live clock, so the
// same document measured differently between runs; and it inherited two
// non-minimal fields from a fixture written for another purpose. Every one was
// an INHERITANCE or an OMISSION. Not one was a wrong calculation.
//
// That is the signature of a hand-built primitive: each fix corrects the
// instance and leaves the class intact, because nothing forces the fixture to
// account for a field nobody thought about. So the fixtures are no longer
// written; they are DERIVED from a table, and a field missing from that table
// is a test failure rather than a silent default.
//
// The table is the single source for both documents:
//
//	irreducible — every field at the SMALLEST value its validator accepts
//	maximal     — every field at the LARGEST value its validator accepts
//
// and the proof is per FIELD, not per byte: for every entry, stepping past the
// bound must make the validator reject. A byte total that nobody can attribute
// to a field is not evidence; a field whose limit is proven by the validator is.

// answerBound is one field of ContextFabricInvestigationResult and the limits
// its own validator places on it.
type answerBound struct {
	// Field is the Go field name. The reflection guard requires every field
	// of the result struct to appear here exactly once -- that is what makes
	// an omission impossible rather than merely unlikely.
	Field string
	// Why names the validator clause or constant that fixes the limits, so a
	// reader can check the entry against the code rather than trusting it.
	Why string
	// Min sets the field to the SMALLEST legal value, measured in serialized
	// bytes rather than in count -- for a bool that is `true`, which encodes
	// one byte shorter than `false`.
	Min func(r *ContextFabricInvestigationResult)
	// Max sets the field to the LARGEST legal value.
	Max func(r *ContextFabricInvestigationResult)
	// PastMax steps one increment beyond the upper bound. Validate MUST
	// reject the result afterwards. Nil means the field has no upper bound a
	// test can breach (a fixed enum, a bool, a required identifier), and the
	// guard requires a reason in Why rather than accepting a bare nil.
	PastMax func(r *ContextFabricInvestigationResult)
}

// fixedAnswerInstant pins the one input that is otherwise a live clock. A zero
// fractional second is deliberate: Go marshals time as RFC3339Nano, which drops
// trailing zeros, so a zero fraction is both the SHORTEST encoding and a stable
// one. A fixture with time.Now() in it measured 22 to 30 bytes differently
// between runs, passed locally four times, and failed in CI.
var fixedAnswerInstant = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

// minimalSubject is the smallest legal subject reference: a valid kind and
// one-rune identifiers.
func minimalSubject() ContextFabricSubjectRef {
	return ContextFabricSubjectRef{Kind: ContextFabricSubjectProject, CanonicalID: "p", Label: "l"}
}
