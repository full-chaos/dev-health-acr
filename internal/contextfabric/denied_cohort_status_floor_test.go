package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func deniedCohortResult(status InvestigationStatus, code contractsv1.ContextFabricCoverageDetailCode, degrading bool) InvestigationResult {
	count := 2
	return InvestigationResult{
		Status: status,
		Coverage: Coverage{Details: []CoverageDetail{{
			DetailID: "cov-graph-01", Source: "context-fabric:graph", Code: code, Degrading: degrading, Count: &count,
		}}},
	}
}

func TestDeniedCohortFloorsOnlyACompleteAnswerWithTheDegradingDenialRow(t *testing.T) {
	denied := contractsv1.ContextFabricCoverageDetailGraphCohortDeniedByAuthorization
	cases := []struct {
		name   string
		result InvestigationResult
		want   InvestigationStatus
	}{
		{"complete with the denial row", deniedCohortResult(InvestigationComplete, denied, true), InvestigationPartial},
		{"denial row not degrading", deniedCohortResult(InvestigationComplete, denied, false), InvestigationComplete},
		{"other degrading code", deniedCohortResult(InvestigationComplete, contractsv1.ContextFabricCoverageDetailGraphNoMemberFound+"x", true), InvestigationComplete},
		{"already degraded stays degraded", deniedCohortResult(InvestigationDegraded, denied, true), InvestigationDegraded},
		{"refusal stays", func() InvestigationResult {
			r := deniedCohortResult(InvestigationComplete, denied, true)
			r.RefusalBasis = "x"
			return r
		}(), InvestigationComplete},
		{"no row", InvestigationResult{Status: InvestigationComplete}, InvestigationComplete},
	}
	for _, c := range cases {
		got := ApplyServedStatusFloors(c.result)
		if got.Status != c.want {
			t.Errorf("%s: status = %q, want %q", c.name, got.Status, c.want)
		}
	}
}
