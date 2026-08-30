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
}
