package contextfabric

import "sort"

// DimensionFactKindRankingFamilyRow is one row of CHAOS-4468's deliverable:
// the dimension <-> FactKind <-> ranking-family table. RankingFamily is
// empty for every FactKind that is not one of cohort_ranking.go's five
// closed ranking signals (RankingSignalInvestmentMix and friends) -- most
// FactKinds contribute evidence without ever feeding RankCohort's formula,
// and an empty string here says so honestly rather than a placeholder.
type DimensionFactKindRankingFamilyRow struct {
	Dimension      HealthDimension
	Kind           FactKind
	CapabilityName string
	RankingFamily  string
}

// rankingFamilyByFactKind is the FactKind half of cohort_ranking.go's five
// RankingSignal* constants (RankCohort's own scoreMember reads exactly
// these five FactKinds -- see healthRiskSignal/deficiencySeveritySignal/
// readinessGapSignal/workloadWorstDays/investmentMixSignal). A FactKind not
// listed here does not feed RankCohort's formula at all.
var rankingFamilyByFactKind = map[FactKind]string{
	FactInvestment:              RankingSignalInvestmentMix,
	FactHealth:                  RankingSignalHealthRisk,
	FactOperationalDeficiencies: RankingSignalDeficiencySeverity,
	FactReadiness:               RankingSignalReadinessGap,
	FactWorkload:                RankingSignalWorkloadPressure,
}

// GenerateDimensionFactKindRankingFamilyTable is CHAOS-4468's mapping table,
// GENERATED from the live FactCapability registry rather than hand-
// maintained beside it (design doc §5.3): every row's Dimension comes
// straight off the registered capability's own declaration, so the table
// cannot drift from what the registry actually asserts -- there is no
// second copy for a producer's dimension to disagree with.
//
// Sorted by Kind for a deterministic rendering (chosen over registration
// order, which depends on providers.go's slice literal order and is not a
// property this table should expose).
func GenerateDimensionFactKindRankingFamilyTable(capabilities []FactCapability) []DimensionFactKindRankingFamilyRow {
	rows := make([]DimensionFactKindRankingFamilyRow, 0, len(capabilities))
	for _, capability := range capabilities {
		rows = append(rows, DimensionFactKindRankingFamilyRow{
			Dimension:      capability.Dimension,
			Kind:           capability.Kind,
			CapabilityName: capability.Name,
			RankingFamily:  rankingFamilyByFactKind[capability.Kind],
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Kind < rows[j].Kind })
	return rows
}

// RenderDimensionFactKindRankingFamilyMarkdown renders GENERATE's output as
// the markdown table checked into
// docs/design/context-fabric-dimension-factkind-ranking-family.md.
// chaos4633_dimension_mapping_test.go asserts the checked-in file equals
// this function's output byte-for-byte, so a registry change that is not
// reflected in the doc fails CI rather than drifting silently.
func RenderDimensionFactKindRankingFamilyMarkdown(rows []DimensionFactKindRankingFamilyRow) string {
	out := "| dimension | fact kind | capability | ranking family |\n"
	out += "|---|---|---|---|\n"
	for _, row := range rows {
		family := row.RankingFamily
		if family == "" {
			family = "—"
		}
		out += "| " + string(row.Dimension) + " | " + string(row.Kind) + " | " + row.CapabilityName + " | " + family + " |\n"
	}
	return out
}
