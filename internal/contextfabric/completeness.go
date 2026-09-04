package contextfabric

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4413: promotes CHAOS-4386's harness-only terminal-answer
// measurement (formerly duplicated as chaos4386TerminalReason/
// chaos4386TerminalFields in internal/runtime/hosted's trial-harness test
// helpers) into production. ComputeAnswerCompleteness is now the ONE place
// this logic lives; the harness reads the promoted contract field
// (result.Completeness, stamped by the engine below) instead of
// recomputing it from a side channel.
//
// Placement in the engine mirrors CHAOS-4415's SelectRenderShapes: run on
// the FINAL result, after every composer including the commit-affirmation
// gate, immediately before Validate -- see engine.go's call site.

// ComputeAnswerCompleteness derives result's ContextFabricAnswerCompleteness
// block. Pure: reads result, mutates nothing.
func ComputeAnswerCompleteness(result InvestigationResult) contractsv1.ContextFabricAnswerCompleteness {
	rows := 0
	for _, fact := range result.ClaimedFacts {
		rows += len(fact.Rows)
	}
	// The outcome set is CARRIED, never rebuilt here, and the state is
	// DERIVED from it on every call.
	//
	// That is the whole ordering discipline of the outcome layer in two
	// lines. Stages append rows to the block; this function runs last, at
	// the surface that serves the answer, and computes the state from
	// whatever the set holds by then. Because it re-derives rather than
	// copies, a stage that narrows the document after an earlier
	// completeness was stamped cannot leave a stale state behind -- the
	// narrowing is itself a row, and the state moves with it.
	//
	// It also means this function must never DROP rows: rebuilding the
	// block without them would silently delete another stage's disclosure,
	// which is the one thing the append invariant forbids.
	outcomes := result.Completeness.Outcomes
	return contractsv1.ContextFabricAnswerCompleteness{
		TerminalStatus:    result.Status,
		TerminalReason:    answerTerminalReason(result),
		ClaimedFactsCount: len(result.ClaimedFacts),
		RowsCount:         rows,
		State:             contractsv1.DeriveContextFabricAnswerCompletenessState(outcomes),
		Outcomes:          outcomes,
	}
}

// answerTerminalReason classifies WHY a non-complete result stopped where
// it did, into the closed ContextFabricTerminalReason vocabulary -- never
// the engine's or model's own raw prose (Coverage.DegradedReasons/
// Limitations/Warnings can all be arbitrary, dynamically-formatted or
// model-authored text; a corpus-safe reason class points AT the channel
// that fired without ever copying its content).
//
// There is no single unified "reason" field on
// ContextFabricInvestigationResult. For clarification_required, the
// engine's OWN disclosure channel is SubjectResolution.ClarificationPrompt
// (unresolved.go's composeSubjectlessTerminal populates it on the ordinary
// ambiguous-candidate path) -- Interpretation.ClarificationReason is a
// secondary, independent channel, checked too, so an alternate path that
// populates only that field still classifies correctly. Every other
// non-complete status carries its explanation, when the engine gave one, in
// Coverage.DegradedReasons, Limitations, or, failing those, Warnings --
// Limitations specifically is where a normal production no_match (an empty
// subject pool, a window/structure veto) puts its explanation, while
// DegradedReasons/Warnings both stay empty on that path.
func answerTerminalReason(result InvestigationResult) contractsv1.ContextFabricTerminalReason {
	switch result.Status {
	case InvestigationComplete:
		return ""
	case InvestigationClarificationRequired:
		if result.SubjectResolution.ClarificationPrompt != "" || result.Interpretation.ClarificationReason != "" {
			return contractsv1.ContextFabricTerminalReasonClarificationDisclosed
		}
		return contractsv1.ContextFabricTerminalReasonUndisclosed
	default:
		if len(result.Coverage.DegradedReasons) > 0 {
			return contractsv1.ContextFabricTerminalReasonDegradedDisclosed
		}
		if len(result.Limitations) > 0 {
			return contractsv1.ContextFabricTerminalReasonLimitationDisclosed
		}
		if len(result.Warnings) > 0 {
			return contractsv1.ContextFabricTerminalReasonWarningDisclosed
		}
		return contractsv1.ContextFabricTerminalReasonUndisclosed
	}
}
