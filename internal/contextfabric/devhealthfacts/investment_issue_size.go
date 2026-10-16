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

// investmentMeasureCount is the measure of a mix whose units each weigh 1:
// the project mixes, until they are read in issue size.
const investmentMeasureCount = "COUNT"

// sizedWorkUnitInvestments replaces the table name in a statement that reads
// work_unit_investments: the organization's rows with unit_size_weight and
// unit_issue_size added (readers.IssueSizedSource). The org predicate sits
// inside so the join never reads another organization's rows; the statement
// keeps its own predicates outside.
var sizedWorkUnitInvestments = mustSizedWorkUnitInvestments()

func mustSizedWorkUnitInvestments() string {
	const alias = "work_unit_investments"
	source, err := readers.IssueSizedSource(
		"(SELECT * FROM work_unit_investments WHERE org_id = {org_id:String}) AS "+alias, alias)
	if err != nil {
		panic(fmt.Sprintf("devhealthfacts: sized work unit source: %v", err))
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
