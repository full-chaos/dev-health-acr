package answerprojection

import (
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The projection's half of the outcome layer.
//
// WHY THIS EXISTS. The projection used to copy the canonical completeness
// block verbatim and then narrow the document underneath it. Under a census
// that was coherent: the block carried counts, and the projection's own
// counters sat beside them, so a reader could see both. Under an outcome set
// it is not, because a copied completeness cannot carry a NAME it never had
// -- every row could read `satisfied` while the served document had lost
// members and whole groups. That is the measure-then-shrink defect relocated
// one boundary down.
//
// So the projection APPENDS its own cuts as rows and the state is RE-DERIVED
// from the extended set. Nothing canonical is rewritten: the rows the
// investigation established are carried through untouched, which is what the
// per-surface rule actually requires. What changes is that the served answer
// can no longer claim a completeness its own document contradicts.

// projectionOmission is one omission counter and what losing it costs the
// reader.
type projectionOmission struct {
	// Field is the ProjectionBudget field name. It is the identity the
	// coverage guard reflects over, so a counter added to that struct
	// without an entry here fails a test rather than silently escaping
	// disclosure.
	Field string
	Count int
	// Impact is what the reader loses. SCOPE where fewer subjects reach
	// them, DEPTH where the same subjects arrive with less behind them.
	Impact contractsv1.ContextFabricAnswerImpactKind
}

// projectionOmissions enumerates every drop the projection budget declares.
//
// It is deliberately the SAME population declaresDrop reads. A projection
// that declares itself truncated on a counter with no entry here would
// announce a truncation it could not name, which is the generic bit this
// layer exists to replace.
func projectionOmissions(budget contractsv1.ContextFabricProjectionBudget) []projectionOmission {
	scope := contractsv1.ContextFabricAnswerImpactScope
	depth := contractsv1.ContextFabricAnswerImpactDepth
	return []projectionOmission{
		// Fewer subjects than the investigation found.
		{"CohortMembersOmitted", budget.CohortMembersOmitted, scope},
		{"CohortGroupsOmitted", budget.CohortGroupsOmitted, scope},
		{"CandidatesOmitted", budget.CandidatesOmitted, scope},
		// The same subjects, with less behind them.
		{"DriversOmitted", budget.DriversOmitted, depth},
		{"WithheldDriversOmitted", budget.WithheldDriversOmitted, depth},
		{"FactsOmitted", budget.FactsOmitted, depth},
		{"EvidenceRefsOmitted", budget.EvidenceRefsOmitted, depth},
		{"ReasonsOmitted", budget.ReasonsOmitted, depth},
		{"ValuesClamped", budget.ValuesClamped, depth},
		{"LimitationsOmitted", budget.LimitationsOmitted, depth},
		{"WarningsOmitted", budget.WarningsOmitted, depth},
		{"CoverageOmitted", budget.CoverageOmitted, depth},
		{"RenderShapesOmitted", budget.RenderShapesOmitted, depth},
	}
}

// appendProjectionOutcomes appends one row per non-zero omission and
// re-derives the state from the whole set.
//
// The rows carry no requirement identity. That is honest rather than lazy:
// the projection cuts by its own budget over the finished document and does
// not know which requirement a dropped driver was serving. Attaching the
// nearest plausible requirement would be a wrong attribution, and a reader
// acts on those; an absent one they can see is absent.
//
// The cause is the caller's own BYTE ceiling, from the shipped overrun
// vocabulary -- which is what a projection budget is.
func appendProjectionOutcomes(projection contractsv1.ContextFabricAnswerProjection) contractsv1.ContextFabricAnswerProjection {
	rows := projection.Completeness.Outcomes
	for _, omission := range projectionOmissions(projection.ProjectionBudget) {
		if omission.Count <= 0 {
			continue
		}
		// Declared is how many this budget dropped, served is how many
		// of THOSE the caller still receives -- none, by definition of a
		// drop. The pair is carried rather than the count alone because
		// the row's own validator requires a narrowing to be a real
		// reduction, and a bare count cannot show that it was.
		rows = append(rows, projectionOutcomeRow(omission.Impact, omission.Count))
	}
	projection.Completeness.Outcomes = rows
	// DERIVED LAST, over the whole set. This is the line that makes the
	// served answer's completeness true of the served document.
	projection.Completeness.State = contractsv1.DeriveContextFabricAnswerCompletenessState(rows)
	return projection
}

// projectionOutcomeRow is the shape every projection-stage row takes: no
// requirement identity, the caller's byte ceiling as the cause, observed.
func projectionOutcomeRow(impact contractsv1.ContextFabricAnswerImpactKind, declared int) contractsv1.ContextFabricPlanRequirementOutcomeRow {
	row := contractsv1.ContextFabricPlanRequirementOutcomeRow{
		Stage:         contractsv1.ContextFabricOutcomeStageProjection,
		Outcome:       contractsv1.ContextFabricRequirementNarrowed,
		Impact:        impact,
		CauseOverrun:  contractsv1.ContextFabricBudgetOverrunBytes,
		CauseObserved: true,
		Served:        0,
		Declared:      declared,
	}
	// The reduction step, derived from the row. Yields nothing when declared
	// is zero -- a projection that dropped nothing has no step to record, and
	// a zero-length step is not a refinement.
	return contractsv1.ContextFabricWithReductionRefinement(row)
}
