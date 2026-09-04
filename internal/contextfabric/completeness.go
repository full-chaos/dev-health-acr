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
	outcomes := accountForPublishedPlanRequirements(result)
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

// accountForPublishedPlanRequirements returns the carried outcome set with a
// planning-stage row added for every requirement the plan PUBLISHES that no
// carried row names.
//
// THE DEFECT IT CLOSES. Publishing the requirement rows and seeding their
// outcomes were two steps in two places. The rows are stamped where the plan
// is created, so every terminal downstream of planning carries them. The seed
// ran inside finalization -- which the window- and structure-veto terminals
// never reach. Those terminals served, and SAVED, a plan describing
// requirements that nothing accounted for; the document-level join, which
// holds in both directions, then refuses the saved document. The failure is
// invisible from any single exit's tests because each exit is individually
// well-formed. It only appears when the two published arrays are read
// together, which is the one thing a reader of the artifact does.
//
// WHY HERE, AND NOT AT EACH EXIT. This function is the choke point every
// independent exit already calls immediately before its own Validate. Fixing
// it per-exit would mean the next exit added is wrong by default and stays
// wrong until someone remembers; fixing it here means an exit cannot publish
// requirements it does not account for, because the accounting happens on the
// way out rather than on the way in. The rule is a property of the surface,
// so it lives at the surface.
//
// WHY GAP-FILL RATHER THAN SEED-IF-EMPTY. Filling only when the set is empty
// would leave the join broken for the harder, quieter case: an exit that
// appended rows for some requirements and not others. Gap-filling is monotone
// -- it only ever adds -- so it cannot violate the append invariant this file
// states above, and it is idempotent, so running it twice is running it once.
//
// It is NOT a second authority for what the requirements are: the identities
// come from the plan's own published array, so a row it adds cannot describe
// a requirement the plan does not publish.
func accountForPublishedPlanRequirements(result InvestigationResult) []RequirementOutcomeRow {
	outcomes := result.Completeness.Outcomes
	if result.AnswerPlan == nil || len(result.AnswerPlan.Requirements) == 0 {
		return outcomes
	}
	accounted := make(map[string]bool, len(outcomes))
	for _, row := range outcomes {
		if row.Requirement != "" {
			accounted[row.Requirement] = true
		}
	}
	gaps := make([]RequirementOutcomeRow, 0, len(result.AnswerPlan.Requirements))
	for _, seeded := range SeedOutcomesFromPublishedPlanRequirements(result.AnswerPlan.Requirements) {
		if accounted[seeded.Requirement] {
			continue
		}
		// Mark before appending: two published rows sharing an identity
		// would otherwise each add a row, and the join reads "exactly one".
		accounted[seeded.Requirement] = true
		gaps = append(gaps, seeded)
	}
	if len(gaps) == 0 {
		return outcomes
	}
	// COPY, then append. `result` arrives by value but its slice header
	// still points at the CALLER's backing array, so appending onto
	// `outcomes` directly would write the gap rows into that array whenever
	// it has spare capacity -- mutating a caller this function documents
	// itself as not mutating, and doing so only for some capacities, which
	// is the kind of defect that reproduces on Tuesdays.
	accountedSet := make([]RequirementOutcomeRow, 0, len(outcomes)+len(gaps))
	accountedSet = append(accountedSet, outcomes...)
	accountedSet = append(accountedSet, gaps...)
	return accountedSet
}
