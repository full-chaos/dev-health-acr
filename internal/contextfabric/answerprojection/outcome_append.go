package answerprojection

import (
	"fmt"

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
	room := contractsv1.ContextFabricPlanRequirementOutcomeMaxCount - len(rows)
	if room < 0 {
		room = 0
	}
	omissions, omissionRows := pendingOmissionRows(projection)
	countRows := countRowsBesideCutMembers(projection)
	cut := false
	if len(omissionRows)+len(countRows) <= room {
		rows = append(rows, omissionRows...)
		rows = append(rows, countRows...)
	} else {
		cut = true
		// The disclosure displaces a limitation when that list is full, and
		// the displacement is itself an omission, so it is applied BEFORE the
		// omission rows are built from the budget.
		projection = reserveDisclosureLimitation(projection)
		omissions, omissionRows = pendingOmissionRows(projection)
		var merged, dropped int
		rows, merged, dropped = appendWithinCap(rows, omissions, omissionRows, countRows, room)
		projection.ProjectionBudget.Truncated = true
		projection.Limitations = withCutDisclosure(projection.Limitations, merged, dropped)
	}
	projection.Completeness.Outcomes = rows
	// DERIVED LAST, over the whole set. This is the line that makes the
	// served answer's completeness true of the served document.
	projection.Completeness.State = contractsv1.DeriveContextFabricAnswerCompletenessState(rows)
	// A cut can leave no room for any row to say so, and the rows then derive
	// the state of a document that lost rows; the cut itself bars `complete`.
	if cut && projection.Completeness.State == contractsv1.ContextFabricAnswerCompletenessComplete {
		projection.Completeness.State = contractsv1.ContextFabricAnswerCompletenessPartial
	}
	return projection
}

// pendingOmissionRows builds one row per non-zero omission counter.
func pendingOmissionRows(projection contractsv1.ContextFabricAnswerProjection) ([]projectionOmission, []contractsv1.ContextFabricPlanRequirementOutcomeRow) {
	var omissions []projectionOmission
	var omissionRows []contractsv1.ContextFabricPlanRequirementOutcomeRow
	for _, omission := range projectionOmissions(projection.ProjectionBudget) {
		if omission.Count <= 0 {
			continue
		}
		// Declared is how many this budget dropped, served is how many
		// of THOSE the caller still receives -- none, by definition of a
		// drop. The pair is carried rather than the count alone because
		// the row's own validator requires a narrowing to be a real
		// reduction, and a bare count cannot show that it was.
		omissions = append(omissions, omission)
		omissionRows = append(omissionRows, projectionOutcomeRow(omission.Impact, omission.Count))
	}
	return omissions, omissionRows
}

// reserveDisclosureLimitation makes room in a full limitations list for the
// cut disclosure and counts the limitation it displaces.
func reserveDisclosureLimitation(projection contractsv1.ContextFabricAnswerProjection) contractsv1.ContextFabricAnswerProjection {
	if len(projection.Limitations) >= contractsv1.ContextFabricProjectedNarrativeMaxCount {
		projection.Limitations = append([]string(nil), projection.Limitations[:contractsv1.ContextFabricProjectedNarrativeMaxCount-1]...)
		projection.ProjectionBudget.LimitationsOmitted++
	}
	return projection
}

// withCutDisclosure appends the cut disclosure, unless the list already holds
// that exact text (the validator rejects duplicates).
func withCutDisclosure(limitations []string, merged, dropped int) []string {
	disclosure := fmt.Sprintf("Projection outcome rows were cut to the %d-row cap: %d omission rows merged by impact, %d rows not itemized.", contractsv1.ContextFabricPlanRequirementOutcomeMaxCount, merged, dropped)
	for _, existing := range limitations {
		if existing == disclosure {
			return limitations
		}
	}
	return append(append([]string(nil), limitations...), disclosure)
}

// appendWithinCap keeps the projection-stage rows inside the outcome row cap.
// Canonical rows are never rewritten. Omission rows go first; when they do not
// fit they are merged into one row per impact kind, and count rows take
// whatever room remains. It reports how many omission rows were merged and how
// many rows could not be itemized, which the limitations then name outside the
// rows, because at a full canonical set there is no room for a row to say so.
func appendWithinCap(rows []contractsv1.ContextFabricPlanRequirementOutcomeRow, omissions []projectionOmission, omissionRows, countRows []contractsv1.ContextFabricPlanRequirementOutcomeRow, room int) ([]contractsv1.ContextFabricPlanRequirementOutcomeRow, int, int) {
	remaining := room
	merged := 0
	dropped := 0
	if len(omissionRows) <= remaining {
		rows = append(rows, omissionRows...)
		remaining -= len(omissionRows)
	} else {
		totals := map[contractsv1.ContextFabricAnswerImpactKind]int{}
		for _, omission := range omissions {
			totals[omission.Impact] += omission.Count
		}
		for _, impact := range []contractsv1.ContextFabricAnswerImpactKind{contractsv1.ContextFabricAnswerImpactScope, contractsv1.ContextFabricAnswerImpactDepth} {
			if totals[impact] == 0 {
				continue
			}
			if remaining == 0 {
				dropped++
				continue
			}
			rows = append(rows, projectionOutcomeRow(impact, totals[impact]))
			remaining--
		}
		merged = len(omissionRows)
	}
	for _, row := range countRows {
		if remaining == 0 {
			dropped++
			continue
		}
		rows = append(rows, row)
		remaining--
	}
	return rows, merged, dropped
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

// countRowsBesideCutMembers appends, for each assembled-result `count` row
// that states more members than the projection serves, a projection-stage row
// carrying that row's own requirement identity and the served member count.
//
// The identity is copied from the count row, not inferred: a count is
// membership-bound, so a cut member set is a reduction of exactly that
// requirement. The unattributed omission rows above stay unattributed.
// Nothing canonical is rewritten; the later row is the requirement's
// effective account.
func countRowsBesideCutMembers(projection contractsv1.ContextFabricAnswerProjection) []contractsv1.ContextFabricPlanRequirementOutcomeRow {
	if projection.ProjectionBudget.CohortMembersOmitted <= 0 || projection.Cohort == nil {
		return nil
	}
	served := len(projection.Cohort.Members)
	var appended []contractsv1.ContextFabricPlanRequirementOutcomeRow
	for _, row := range projection.Completeness.Outcomes {
		if row.Stage != contractsv1.ContextFabricOutcomeStageAssembledResult || row.Obligation != string(contractsv1.ContextFabricAnswerObligationCount) || row.Requirement == "" {
			continue
		}
		if row.Outcome != contractsv1.ContextFabricRequirementSatisfied && row.Outcome != contractsv1.ContextFabricRequirementNarrowed {
			continue
		}
		if row.Served <= served {
			continue
		}
		appended = append(appended, contractsv1.ContextFabricWithReductionRefinement(contractsv1.ContextFabricPlanRequirementOutcomeRow{
			Stage:         contractsv1.ContextFabricOutcomeStageProjection,
			Requirement:   row.Requirement,
			Obligation:    row.Obligation,
			Outcome:       contractsv1.ContextFabricRequirementNarrowed,
			Impact:        contractsv1.ContextFabricAnswerImpactScope,
			CauseOverrun:  contractsv1.ContextFabricBudgetOverrunBytes,
			CauseObserved: true,
			Served:        served,
			Declared:      row.Served,
		}))
	}
	return appended
}
