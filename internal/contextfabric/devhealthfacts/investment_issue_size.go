package devhealthfacts

import (
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-go/readers"
)

// The investment mix weighs every work unit by its issue size (D6027): the sum
// of the unit's issues' positive story points, which ops materialises per unit
// in work_unit_issue_size; a unit with no positive size counts 1 and is
// "unsized". The weight, the size and the coverage text are defined once, in
// dev-health-go readers (IssueSizedSource, UnitSizeWeightExpr,
// SizingCoverage); this file only wires the source into the statements.

// investmentMeasureIssueSize is the measure the mix is read in.
const investmentMeasureIssueSize = "ISSUE_SIZE"

// sizedUnits adds unit_size_weight and unit_issue_size to a statement that
// has already folded work_unit_investments to one row per work unit (it must
// project work_unit_id and org_id): the join to the unit sizes
// (readers.IssueSizedSource) runs over the folded units, never over the
// table, so the read of the table keeps its plan and its memory. The result
// is "(...) AS sized_units", ready for a FROM.
func sizedUnits(folded string) string {
	return sizedSource("(" + folded + ")")
}

// sizedUnitsForOrg is sizedUnits over a fold that does not carry org_id: the
// organization column is added here, over the fold, so no fold aliases the
// organization parameter beside its own org_id filter, where the alias would
// win over the table column.
func sizedUnitsForOrg(folded string) string {
	return sizedSource("(SELECT folded_units.*, {org_id:String} AS org_id FROM (" + folded + ") AS folded_units)")
}

func sizedSource(source string) string {
	const alias = "sized_units"
	source, err := readers.IssueSizedSource(source+" AS "+alias, alias)
	if err != nil {
		panic(fmt.Sprintf("devhealthfacts: sized units: %v", err))
	}
	return source
}

// investmentSizingFields adds the measure and its sizing coverage to a mix
// fact (D6027: coverage rides with every ISSUE_SIZE answer), exactly as ops'
// REST investment response and explanation serve it (investment/response.go
// SizingCoverage, UnsizedUnitCount, SizingNote; investmentexplain
// SizingCoverage.Share, Unsized, Note): denominator is the sum of all themes
// the shares are taken over; sizing_coverage is left out for a mix with no unit
// (no share to state, never 0); unsized_unit_count is always served; the note
// is left out unless fewer than half of the units carry a size.
func investmentSizingFields(fields map[string]contextfabric.FactValue, total float64, workUnits, sizedUnits int64) {
	coverage := readers.SizingCoverage{SizedUnits: int(sizedUnits), Units: int(workUnits)}
	fields["measure"] = contextfabric.StringFactValue(investmentMeasureIssueSize)
	fields["denominator"] = contextfabric.NumberFactValue(roundMixEffort(total))
	if share := coverage.Share(); share != nil {
		fields["sizing_coverage"] = contextfabric.NumberFactValue(*share)
	}
	fields["unsized_unit_count"] = contextfabric.IntegerFactValue(int64(*coverage.Unsized()))
	if note := coverage.Note(); note != "" {
		fields["sizing_note"] = contextfabric.StringFactValue(note)
	}
}
