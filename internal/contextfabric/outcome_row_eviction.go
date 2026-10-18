package contextfabric

import contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"

// outcomeRowsEvicted is the cumulative count the document's eviction
// disclosure states; zero when it carries none.
func outcomeRowsEvicted(limitations []string) int {
	for _, limitation := range limitations {
		if total, ok := contractsv1.ParseContextFabricOutcomeEvictionLimitation(limitation); ok {
			return total
		}
	}
	return 0
}

func outcomeImpactRank(impact contractsv1.ContextFabricAnswerImpactKind) int {
	switch impact {
	case contractsv1.ContextFabricAnswerImpactNone:
		return 0
	case contractsv1.ContextFabricAnswerImpactScope:
		return 1
	case contractsv1.ContextFabricAnswerImpactDepth:
		return 2
	default:
		return 3
	}
}

// isLeverOutcomeRow reports a row a stage-3 lever wrote to disclose its own
// cut. Evicting one would delete a disclosure to make room for another.
func isLeverOutcomeRow(row RequirementOutcomeRow) bool {
	return row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult && row.Outcome == contractsv1.ContextFabricRequirementNarrowed
}

// evictionIndex picks the lowest-ranked row: least reader impact, ties to the
// last row. Lever rows are skipped unless every row is one.
func evictionIndex(rows []RequirementOutcomeRow) int {
	pick := func(skipLever bool) int {
		best, bestRank := -1, 0
		for index, row := range rows {
			if skipLever && isLeverOutcomeRow(row) {
				continue
			}
			if rank := outcomeImpactRank(row.Impact); best < 0 || rank <= bestRank {
				best, bestRank = index, rank
			}
		}
		return best
	}
	if index := pick(true); index >= 0 {
		return index
	}
	return pick(false)
}

// appendLeverOutcomeRow appends a lever's disclosure row. At the contract's
// outcome cap it first evicts the lowest-ranked row, so a servable answer is
// served rather than refused (D6080). It returns how many rows it evicted
// (0 or 1); the caller discloses that with withOutcomeEvictionLimitation.
func appendLeverOutcomeRow(existing []RequirementOutcomeRow, row RequirementOutcomeRow) ([]RequirementOutcomeRow, int) {
	if len(existing) < contractsv1.ContextFabricPlanRequirementOutcomeMaxCount {
		return appendOutcomeRows(existing, row), 0
	}
	index := evictionIndex(existing)
	kept := make([]RequirementOutcomeRow, 0, len(existing))
	kept = append(kept, existing[:index]...)
	kept = append(kept, existing[index+1:]...)
	return appendOutcomeRows(kept, row), 1
}

// withOutcomeEvictionLimitation states the cumulative evicted count in the
// limitations, replacing an earlier statement of it. Zero is a no-op.
func withOutcomeEvictionLimitation(limitations []string, evicted int) ([]string, int) {
	if evicted <= 0 {
		return limitations, 0
	}
	total := evicted
	kept := make([]string, 0, len(limitations)+1)
	for _, limitation := range limitations {
		if prior, ok := contractsv1.ParseContextFabricOutcomeEvictionLimitation(limitation); ok {
			total += prior
			continue
		}
		kept = append(kept, limitation)
	}
	sentence, ok := contractsv1.ContextFabricOutcomeEvictionLimitation(total)
	if !ok {
		return limitations, 0
	}
	return appendBoundedLimitations(kept, []string{sentence})
}
