package contextfabric

import (
	"context"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type limitationRecogniser struct {
	id      string
	matches func(string) bool
}

var serviceLimitationRecognisers = []limitationRecogniser{
	{"fixed", func(limitation string) bool {
		return slices.Contains(contractsv1.ContextFabricServiceAuthoredLimitations(), limitation)
	}},
	{"grouping_refusal", contractsv1.IsContextFabricGroupingRefusalLimitation},
	{"grouping_overlap", contractsv1.IsContextFabricGroupingOverlapLimitation},
	{"grouping_ungrouped", contractsv1.IsContextFabricGroupingUngroupedLimitation},
	{"grouping_cut", contractsv1.IsContextFabricGroupingCutLimitation},
	{"grouping_unplaceable", contractsv1.IsContextFabricGroupingUnplaceableLimitation},
	{"refusal_basis", contractsv1.IsContextFabricRefusalBasisLimitation},
	{"group_read_unread", contractsv1.IsContextFabricGroupReadUnreadLimitation},
	{"group_list_over_bound", contractsv1.IsContextFabricGroupListOverBoundLimitation},
	{"cohort_narrowing", contractsv1.IsContextFabricCohortNarrowingLimitation},
	{"fact_row_truncation", contractsv1.IsContextFabricFactRowTruncationLimitation},
	{"claim_depth", contractsv1.IsContextFabricClaimDepthLimitation},
	{"budget_trim", contractsv1.IsContextFabricBudgetTrimLimitation},
	{"path_drop", contractsv1.IsContextFabricPathDropLimitation},
	{"work_item_member_filter", contractsv1.IsContextFabricWorkItemMemberFilterLimitation},
	{"pull_request_status", contractsv1.IsContextFabricPullRequestStatusLimitation},
	{"member_status", contractsv1.IsContextFabricMemberStatusLimitation},
	{"degraded_without", contractsv1.IsContextFabricDegradedWithoutLimitation},
	{"work_item_repository", contractsv1.IsContextFabricWorkItemRepositoryLimitation},
	{"work_item_listed", contractsv1.IsContextFabricWorkItemListedLimitation},
	{"work_item_synthesis_coverage", contractsv1.IsContextFabricWorkItemSynthesisCoverageLimitation},
	{"work_item_census_repository_scope", contractsv1.IsContextFabricWorkItemCensusRepositoryScopeLimitation},
	{"stated_range_conflict", contractsv1.IsContextFabricStatedRangeConflictLimitation},
	{"comparison_period_unread", contractsv1.IsContextFabricComparisonPeriodUnreadLimitation},
	{"coverage_entries_dropped", contractsv1.IsContextFabricCoverageEntriesDroppedLimitation},
	{"project_status_missing", contractsv1.IsContextFabricProjectStatusMissingLimitation},
	{"work_item_authorization_gap", contractsv1.IsContextFabricWorkItemAuthorizationGapLimitation},
}

const unnamedLimitationRecogniser = "other"

func serviceLimitationRecogniserID(limitation string) string {
	for _, recogniser := range serviceLimitationRecognisers {
		if recogniser.matches(limitation) {
			return recogniser.id
		}
	}
	return unnamedLimitationRecogniser
}

// stripServiceAuthoredLookalikes removes from a model or client written
// draft every limitation the service-authored recognisers accept. The engine
// composes its own disclosures after the draft is vetted, so a draft entry the
// recognisers accept is a copy or a lookalike, never a disclosure the engine
// made. It returns the kept list and the distinct recogniser ids that matched.
func stripServiceAuthoredLookalikes(limitations []string) (kept []string, dropped int, recognisers []string) {
	kept = make([]string, 0, len(limitations))
	for _, limitation := range limitations {
		if !contractsv1.IsContextFabricServiceAuthoredLimitation(limitation) {
			kept = append(kept, limitation)
			continue
		}
		dropped++
		if id := serviceLimitationRecogniserID(limitation); !slices.Contains(recognisers, id) {
			recognisers = append(recognisers, id)
		}
	}
	slices.Sort(recognisers)
	return kept, dropped, recognisers
}

type serviceLimitationLookalikeRecorder interface {
	RecordServiceLimitationLookalikesDropped(ctx context.Context, principal storage.Principal, dropped int, recognisers []string)
}
