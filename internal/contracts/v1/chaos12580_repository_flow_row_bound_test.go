package v1

import (
	"strings"
	"testing"
)

// chaos12580_repository_flow_row_bound_test.go pins the repository_flow row
// bound class (CHAOS-12580) on the Go side: only a breakdown keyed exactly by
// (repository, theme) takes the 320 cap; every other breakdown keeps 64.
func TestCHAOS12580OnlyARepositoryThemeBreakdownTakesTheRepositoryFlowCap(t *testing.T) {
	t.Parallel()
	flow := claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(ContextFabricClaimedFactRepositoryFlowMaxRows))
	flow.Table.Key = []string{ContextFabricRenderRepositoryField, ContextFabricRenderThemeField}
	if got := ContextFabricClaimedFactRowsMax(flow.Table); got != ContextFabricClaimedFactRepositoryFlowMaxRows {
		t.Fatalf("(repository, theme) breakdown cap = %d, want %d", got, ContextFabricClaimedFactRepositoryFlowMaxRows)
	}
	for name, key := range map[string][]string{
		"(theme, repository)": {ContextFabricRenderThemeField, ContextFabricRenderRepositoryField},
		"(repository)":        {ContextFabricRenderRepositoryField},
		"(theme)":             {ContextFabricRenderThemeField},
	} {
		other := claimedFactWithTable(ContextFabricFactTableShapeBreakdown, distributionRows(ContextFabricClaimedFactMaxRows+1))
		other.Table.Key = key
		if got := ContextFabricClaimedFactRowsMax(other.Table); got != ContextFabricClaimedFactMaxRows {
			t.Errorf("%s breakdown cap = %d, want the default %d", name, got, ContextFabricClaimedFactMaxRows)
		}
		if err := other.Validate(); err == nil || !strings.Contains(err.Error(), "claimed fact rows exceed v1 bounds") {
			t.Errorf("%s breakdown at %d rows: Validate() = %v, want the row bound refusal", name, ContextFabricClaimedFactMaxRows+1, err)
		}
	}
	if ContextFabricClaimedFactRepositoryFlowMaxRows != ContextFabricRenderRepositoryFlowMaxRepositories*ContextFabricInvestmentThemeCount() {
		t.Fatalf("cap %d is not 64 repositories × %d themes", ContextFabricClaimedFactRepositoryFlowMaxRows, ContextFabricInvestmentThemeCount())
	}
}
