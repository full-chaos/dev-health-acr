package v1

// CHAOS-4413: promotes the CHAOS-4386 answer-rate measurement out of
// harness-only telemetry (cmd/acr-trial-merge-two-turn's twoTurnCaseResult,
// PR #315) into a literal field of the public v1 contract. Before this,
// "what happened" (terminal_status/terminal_reason) and "how much of an
// answer is here" (claimed_facts_count/rows_count) existed only in a corpus
// report a consumer never sees -- ask-dev and any other bounded consumer had
// no way to render "missing/stale/partial/unavailable" (North Star check 11:
// "the answer contract is richer than the prose"; check 12: "unknown/stale/
// sparse/not-applicable/zero are distinct").
//
// ContextFabricTerminalReason is the closed vocabulary explaining WHY a
// non-complete terminal status carries the disclosure it does -- never the
// engine's or model's own raw prose (that is exactly the CHAOS-3888/CHAOS-4085
// corpus-leak class this file's own callers already guard against elsewhere:
// Coverage.DegradedReasons/Limitations/Warnings can be arbitrary,
// dynamically-formatted or model-authored text, so a reason class points AT
// one of those channels without ever copying its content).
type ContextFabricTerminalReason string

const (
	// ContextFabricTerminalReasonClarificationDisclosed -- a
	// clarification_required result gave a reason, via either
	// SubjectResolution.ClarificationPrompt (the engine's own ordinary
	// disclosure channel for an ambiguous candidate) or
	// Interpretation.ClarificationReason (the secondary, model-interpretation
	// channel).
	ContextFabricTerminalReasonClarificationDisclosed ContextFabricTerminalReason = "clarification_reason_disclosed"
	// ContextFabricTerminalReasonDegradedDisclosed -- a non-complete result
	// carries at least one Coverage.DegradedReasons entry.
	ContextFabricTerminalReasonDegradedDisclosed ContextFabricTerminalReason = "degraded_reason_disclosed"
	// ContextFabricTerminalReasonLimitationDisclosed -- a non-complete
	// result carries no degraded reason but at least one Limitations entry
	// (the common no_match path: an empty subject pool or a window/structure
	// veto explains itself here, not in DegradedReasons/Warnings).
	ContextFabricTerminalReasonLimitationDisclosed ContextFabricTerminalReason = "limitation_disclosed"
	// ContextFabricTerminalReasonWarningDisclosed -- a non-complete result
	// carries no degraded reason and no limitation, but at least one
	// Warnings entry.
	ContextFabricTerminalReasonWarningDisclosed ContextFabricTerminalReason = "warning_disclosed"
	// ContextFabricTerminalReasonUndisclosed -- a non-complete result gave
	// no reason through any of the above channels. "Undisclosed" is itself
	// a disclosed fact (check 12): a consumer can tell "the engine did not
	// say why" from "the engine had nothing to say" (complete, empty
	// terminal_reason) instead of the two collapsing into the same blank.
	ContextFabricTerminalReasonUndisclosed ContextFabricTerminalReason = "undisclosed"
)

// ValidContextFabricTerminalReason reports whether value is one of the
// closed vocabulary's non-empty members. The empty string is legal too --
// see ContextFabricAnswerCompleteness.TerminalReason's own doc comment --
// but is checked separately by the caller against Status, not here, so this
// helper stays a single-purpose membership check.
func ValidContextFabricTerminalReason(value ContextFabricTerminalReason) bool {
	switch value {
	case ContextFabricTerminalReasonClarificationDisclosed,
		ContextFabricTerminalReasonDegradedDisclosed,
		ContextFabricTerminalReasonLimitationDisclosed,
		ContextFabricTerminalReasonWarningDisclosed,
		ContextFabricTerminalReasonUndisclosed:
		return true
	default:
		return false
	}
}

// ContextFabricAnswerCompleteness groups the answer-completeness/terminal
// disclosure CHAOS-4413 promotes, so a bounded consumer reading only this
// block -- without cross-referencing the top-level Status field or counting
// a budget-clamped array itself -- still gets the full picture of what this
// answer delivered.
//
// ClaimedFactsCount/RowsCount are deliberately NOT derivable by a consumer
// counting ContextFabricAnswerProjection.KeyFacts: that array is
// budget-clamped (see ContextFabricAnswerProjectionBudget), so its length
// answers "how much of the answer did THIS bounded read get", never "how
// much of an answer did the investigation actually produce". These two
// counts are the un-clamped totals from the canonical result and are copied
// onto the projection verbatim (answerprojection.Project), not
// recomputed from whatever KeyFacts the budget kept.
type ContextFabricAnswerCompleteness struct {
	// TerminalStatus mirrors Status verbatim. It is a deliberate duplication
	// (not a $ref-style pointer back to the sibling field): the promotion's
	// whole point is a single, self-contained disclosure group a consumer
	// can read without also holding the rest of the document in scope.
	TerminalStatus ContextFabricInvestigationStatus `json:"terminal_status"`
	// TerminalReason is empty exactly when Status is
	// ContextFabricInvestigationComplete (nothing to disclose), and one of
	// the ContextFabricTerminalReason closed values otherwise --
	// "undisclosed" included: every non-complete status carries a reason
	// class, never a blank one silently standing in for "no data".
	TerminalReason ContextFabricTerminalReason `json:"terminal_reason,omitempty"`
	// ClaimedFactsCount is the literal len() of the result's ClaimedFacts --
	// deliberately not the count of distinct canonical_fact:* Coverage
	// sources (a different concept: sources available to cite vs. facts the
	// synthesis actually claimed).
	ClaimedFactsCount int `json:"claimed_facts_count"`
	// RowsCount sums every claimed fact's own Rows table length across the
	// whole result.
	RowsCount int `json:"rows_count"`
	// State is what the outcome set below adds up to, DERIVED from it by
	// DeriveContextFabricAnswerCompletenessState and never authored
	// independently. The validator requires exact agreement, the same
	// discipline TerminalReason already gets: a state that disagreed with
	// its own rows would let a consumer reading the summary draw a
	// different picture than the rows it summarizes.
	//
	// `not_derived` when Outcomes is empty. That is a real state, not a
	// gap: an answer whose outcomes were never derived must not be able to
	// claim the strongest completeness there is.
	State ContextFabricAnswerCompletenessState `json:"state,omitempty"`
	// Outcomes is the ONE authority for what this answer was supposed to
	// contain and what became of it.
	//
	// Every narrowing stage between planning and the served document
	// APPENDS to it; no stage rewrites or removes another stage's row.
	// State is then derived from the whole set at the surface that serves
	// the answer. That ordering is the whole mechanism: it makes it
	// impossible to measure completeness and then shrink the document
	// somewhere the measurement cannot see, because the shrink is itself
	// one of the rows the measurement reads.
	//
	// This SUPERSEDES, for the outcome layer only, the earlier rule that a
	// projected view copies the completeness block verbatim and never
	// re-derives it. That rule was coherent under its own assumption --
	// that narrowing is disclosed by COUNTERS, which travel with the
	// document they describe. A copied completeness cannot carry a NAME it
	// never had, and naming the reduced requirement is what this layer is
	// for. A projected surface appends its own cuts as rows and re-derives
	// State; it never edits a canonical row, so the two surfaces cannot
	// disagree about what the investigation established.
	Outcomes []ContextFabricPlanRequirementOutcomeRow `json:"outcomes,omitempty"`
}

// IsZero reports whether the block is entirely unstamped.
//
// It exists because Outcomes makes ContextFabricAnswerCompleteness
// non-comparable, and the legacy read path needs exactly the test the `==`
// against a zero value used to perform: every row persisted before this
// field existed carries the zero block, results are immutable, and that one
// shape is excused on read.
func (c ContextFabricAnswerCompleteness) IsZero() bool {
	return c.TerminalStatus == "" && c.TerminalReason == "" &&
		c.ClaimedFactsCount == 0 && c.RowsCount == 0 &&
		c.State == "" && len(c.Outcomes) == 0
}
