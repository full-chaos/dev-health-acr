package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7073 test T4 "catalogue truth": the field declarations in
// fact_field_declarations.go must say exactly what the real providers emit.
//
//   - Rule 1: every field and table column a provider emits is declared, with
//     the right type and nullability; every declaration is emitted by some
//     case (or is listed, with a reason, in declaredButNotExercised).
//   - Rule 4: a missing measurement fails. Every registered provider is a
//     declared kind with at least one case; an undeclared kind fails.

// directServableKinds are the declared kinds: the nine aggregate kinds
// (CHAOS-7073) and the twelve entity kinds (CHAOS-7120). Every registered
// provider must be one of them; there is no "not yet" list.
var directServableKinds = []contextfabric.FactKind{
	contextfabric.FactInvestment, contextfabric.FactHealth, contextfabric.FactMetrics,
	contextfabric.FactFlow, contextfabric.FactReadiness, contextfabric.FactWorkload,
	contextfabric.FactLandscape, contextfabric.FactOperationalDeficiencies, contextfabric.FactSourceHealth,
	contextfabric.FactIdentity, contextfabric.FactMembership, contextfabric.FactStatus,
	contextfabric.FactWork, contextfabric.FactActualCompletion, contextfabric.FactBlockers,
	contextfabric.FactRequiredChildren, contextfabric.FactPullRequests, contextfabric.FactReviews,
	contextfabric.FactContinuousIntegration, contextfabric.FactDeployments, contextfabric.FactIncidents,
}

// declaredButNotExercised lists declarations no case here can reach, with a
// one-line reason. Key: "<subject kind>:<field>" or
// "<subject kind>:<table>.<column>".
var declaredButNotExercised = map[contextfabric.FactKind]map[string]string{
	// The work-unit listing is emitted only when the read asks for units (a
	// context value no case here sets); investment_units_integration_test.go
	// exercises every one of these fields against a real ClickHouse.
	contextfabric.FactInvestment: investmentUnitFieldsNotExercised(),
}

func investmentUnitFieldsNotExercised() map[string]string {
	const reason = "work-unit listing field, emitted only when read_facts asks for units; exercised by investment_units_integration_test.go"
	out := map[string]string{}
	for _, subject := range []string{"team", "repository"} {
		for _, name := range []string{"next_cursor", "page_share_total", "repository_id", "scope_share_total", "scope_unit_rows", "share_in_scope", "unit_attribution_basis", "unit_effort_value", "unit_from", "unit_kind", "unit_mix_source", "unit_pull_request_count", "unit_refs_unresolved", "unit_theme_feature_delivery", "unit_theme_maintenance", "unit_theme_operational", "unit_theme_quality", "unit_theme_risk", "unit_to", "unit_unresolved_refs", "unit_weight", "units_refs_unresolved", "units_returned", "work_unit_id"} {
			out[subject+":"+name] = reason
		}
	}
	return out
}

type t4Case struct {
	name     string
	kind     contextfabric.FactKind
	subjects []contextfabric.SubjectRef
	tables   []fakeTable
	time     contextfabric.TimeContext
}

func t4Days(n int) []string {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	days := make([]string, n)
	for i := range days {
		days[i] = base.AddDate(0, 0, i).Format("2006-01-02")
	}
	return days
}

// t4Rows builds n rows via build(index, day).
func t4Rows(n int, build func(i int, day string) []any) [][]any {
	days := t4Days(n)
	rows := make([][]any, n)
	for i := range rows {
		rows[i] = build(i, days[i])
	}
	return rows
}

func t4LongID(prefix string, i int) string {
	return fmt.Sprintf("%s%03d", prefix, i) + strings.Repeat("x", 3900)
}

func t4Cases() []t4Case {
	var cases []t4Case
	org := []contextfabric.SubjectRef{organizationSubject("org-1")}
	team := []contextfabric.SubjectRef{teamSubject("CHAOS")}
	proj := []contextfabric.SubjectRef{projectSubject("linear", "proj-1")}
	repo := []contextfabric.SubjectRef{repoSubject("repo-1")}
	add := func(c t4Case) { cases = append(cases, c) }

	// ---- source_health (organization)
	withError := sourceHealthRow("gitlab")
	withError[4] = "rate limited"
	nullLabels := sourceHealthRow("")
	nullLabels[1] = ""
	add(t4Case{name: "source_health/organization", kind: contextfabric.FactSourceHealth, subjects: org, tables: []fakeTable{
		{match: "FROM backfill_log", rows: [][]any{sourceHealthRow("github"), withError, nullLabels}},
	}})

	// ---- operational_deficiencies (team)
	nullDeficiency := deficiencyRow("CHAOS")
	for i := 1; i <= 6; i++ {
		nullDeficiency[i] = ""
	}
	add(t4Case{name: "operational_deficiencies/team", kind: contextfabric.FactOperationalDeficiencies, subjects: team, tables: []fakeTable{
		{match: "WHERE rn = 1", rows: [][]any{deficiencyRow("CHAOS"), nullDeficiency}},
	}})

	// ---- landscape
	add(t4Case{name: "landscape/team", kind: contextfabric.FactLandscape, subjects: team, tables: []fakeTable{
		{match: "FROM ic_landscape_rolling_30d", rows: t4Rows(66, func(i int, _ string) []any {
			return landscapeTeamRow("CHAOS", fmt.Sprintf("map_%02d", i), 4, 1200, 30, 18.5, 6)
		})},
	}})
	add(t4Case{name: "landscape/project", kind: contextfabric.FactLandscape, subjects: proj, tables: []fakeTable{
		{match: "FROM team_project_ownership", rows: t4Rows(66, func(i int, _ string) []any {
			return landscapeProjectRow("linear", "proj-1", fmt.Sprintf("team-%02d", i), "churn_throughput", 1, 10, 1, 5.0, 2)
		})},
	}})

	// ---- readiness
	otherTeamRow := readinessRow("OTHER")
	otherTeamRow[1], otherTeamRow[2], otherTeamRow[7] = "", "", uint8(0)
	add(t4Case{name: "readiness/team", kind: contextfabric.FactReadiness,
		subjects: []contextfabric.SubjectRef{teamSubject("CHAOS"), teamSubject("OTHER")}, tables: []fakeTable{
			{match: readinessOriginalQueryMatch, rows: [][]any{readinessRow("CHAOS"), otherTeamRow}},
			{match: readinessDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
				return readinessDailySeriesRow("CHAOS", day, 18, 2, 20)
			})},
		}})
	unattributedReadiness := readinessProjectRollupRow("linear", "proj-1", "", "", "", "", 3, 1, 4, 0.75)
	unattributedReadiness[1] = uint8(0)
	add(t4Case{name: "readiness/project", kind: contextfabric.FactReadiness, subjects: proj, tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: [][]any{
			readinessProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", "linear", 18, 2, 20, 0.9),
			unattributedReadiness,
		}},
		{match: readinessDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
			return readinessDailySeriesRow("linear:proj-1", day, 18, 2, 20)
		})},
	}})

	// ---- workload
	add(t4Case{name: "workload/team", kind: contextfabric.FactWorkload, subjects: team, tables: []fakeTable{
		{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-a")}},
		{match: workloadDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
			return workloadDailySeriesRow("CHAOS", day, 160, 12.2, 1.28)
		})},
	}})
	add(t4Case{name: "workload/project_known_p50", kind: contextfabric.FactWorkload, subjects: proj, tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 1, 1, 0, 14, "team-1", "scope-a", false, true)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, true, 14, false, true, 120),
			chaos5931UnattributedWorkloadRow("linear", "proj-1", "", 1.0, 0.1, false, 0, false, false, 5),
		}},
		{match: workloadDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
			return workloadDailySeriesRow("linear:proj-1", day, 160, 12.2, 1.28)
		})},
	}})
	add(t4Case{name: "workload/project_p50_unavailable", kind: contextfabric.FactWorkload, subjects: proj, tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 0, 0, 1, 0, "", "", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			workloadProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, 120, 1),
		}},
	}})

	// ---- flow
	add(t4Case{name: "flow/team", kind: contextfabric.FactFlow, subjects: team, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("CHAOS", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("CHAOS", "2026-02-21", 15, 9, 4)}},
	}})
	add(t4Case{name: "flow/team_scope_rows_over_cap", kind: contextfabric.FactFlow, subjects: team, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: t4Rows(66, func(i int, _ string) []any {
			return workItemMetricsDailyRow("CHAOS", fmt.Sprintf("scope-%02d", i), 1, 1, 1)
		})},
	}})
	add(t4Case{name: "flow/team_daily_over_cap", kind: contextfabric.FactFlow, subjects: team, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("CHAOS", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any { return flowDailySeriesRow("CHAOS", day, 1, 1, 1) })},
	}})
	add(t4Case{name: "flow/team_dual_table_byte_bound", kind: contextfabric.FactFlow, subjects: team, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: t4Rows(64, func(i int, _ string) []any {
			return workItemMetricsDailyRow("CHAOS", t4LongID("scope-", i), 1, 1, 1)
		})},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("CHAOS", "2026-02-21", 15, 9, 4)}},
	}})
	add(t4Case{name: "flow/project", kind: contextfabric.FactFlow, subjects: proj, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("linear:proj-1", "2026-02-21", 15, 9, 4)}},
	}})
	add(t4Case{name: "flow/project_teams_over_cap", kind: contextfabric.FactFlow, subjects: proj, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: t4Rows(70, func(i int, _ string) []any {
			return projectWorkItemMetricsRow("linear", "proj-1", fmt.Sprintf("team-%02d", i), "scope-a", 1, 1)
		})},
	}})
	add(t4Case{name: "flow/project_daily_over_cap", kind: contextfabric.FactFlow, subjects: proj, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6)}},
		{match: flowDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any { return flowDailySeriesRow("linear:proj-1", day, 1, 1, 1) })},
	}})
	add(t4Case{name: "flow/project_dual_table_byte_bound", kind: contextfabric.FactFlow, subjects: proj, tables: []fakeTable{
		{match: "work_scope_id ORDER BY day DESC", rows: t4Rows(64, func(i int, _ string) []any {
			return projectWorkItemMetricsRow("linear", "proj-1", t4LongID("team-", i), "scope-a", 1, 1)
		})},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("linear:proj-1", "2026-02-21", 15, 9, 4)}},
	}})
	flowWindowStart := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	flowWindowEnd := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	flowWindow := contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &flowWindowStart, End: &flowWindowEnd}
	add(t4Case{name: "flow/team_window", kind: contextfabric.FactFlow, subjects: team, time: flowWindow, tables: []fakeTable{
		{match: "-- window totals", rows: [][]any{{"CHAOS", int64(15), int64(9), int64(1)}}},
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{workItemMetricsDailyRow("CHAOS", "scope-a", 10, 6, 4)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("CHAOS", "2026-02-21", 15, 9, 4)}},
	}})
	add(t4Case{name: "flow/project_window", kind: contextfabric.FactFlow, subjects: proj, time: flowWindow, tables: []fakeTable{
		{match: "-- window totals", rows: [][]any{{"linear:proj-1", int64(15), int64(9), int64(1)}}},
		{match: "work_scope_id ORDER BY day DESC", rows: [][]any{projectWorkItemMetricsRow("linear", "proj-1", "team-1", "scope-a", 10, 6)}},
		{match: flowDailySeriesMatch, rows: [][]any{flowDailySeriesRow("linear:proj-1", "2026-02-21", 15, 9, 4)}},
	}})
	add(t4Case{name: "flow/repository", kind: contextfabric.FactFlow, subjects: repo, tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{repoMetricsFlowRow("repo-1", 8, 6)}},
	}})

	// ---- metrics
	add(t4Case{name: "metrics/repository", kind: contextfabric.FactMetrics, subjects: repo, tables: []fakeTable{
		{match: "FROM repo_metrics_daily", rows: [][]any{metricsRow("repo-1")}},
	}})
	add(t4Case{name: "metrics/team", kind: contextfabric.FactMetrics, subjects: team, tables: []fakeTable{
		{match: "FROM team_metrics_daily", rows: [][]any{teamMetricsRow("CHAOS")}},
	}})
	add(t4Case{name: "metrics/project", kind: contextfabric.FactMetrics, subjects: proj, tables: []fakeTable{
		{match: "FROM team_project_ownership", rows: [][]any{
			projectRollupRow("linear", "proj-1", "team-1", "Team One", 42, 5, 3, 0.12, 0.07),
			projectRollupRow("linear", "proj-1", "team-2", "Team Two", 10, 1, 0, 0.1, 0),
		}},
	}})

	// ---- health
	noNorm := healthRow("repo-3")
	noNorm[8] = uint8(0)
	add(t4Case{name: "health/repository", kind: contextfabric.FactHealth,
		subjects: []contextfabric.SubjectRef{repoSubject("repo-1"), repoSubject("repo-2"), repoSubject("repo-3"), repoSubject("repo-4")}, tables: []fakeTable{
			{match: "FROM compounding_risk_daily", rows: [][]any{
				healthRow("repo-1"),
				healthRowFreshness("repo-2", "unknown", "2026-02-21", false, false),
				noNorm,
				healthRowFreshness("repo-4", "high", "2025-01-01", true, false),
			}},
		}})
	add(t4Case{name: "health/team", kind: contextfabric.FactHealth, subjects: team, tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("CHAOS")}},
		{match: healthDailySeriesMatch, rows: [][]any{
			healthTeamDailySeriesRow("CHAOS", "2026-02-21", "high", uint8(1), 0.61),
			healthTeamDailySeriesRow("CHAOS", "2026-02-20", "", uint8(0), 0),
		}},
	}})
	add(t4Case{name: "health/team_unknown_severity", kind: contextfabric.FactHealth, subjects: []contextfabric.SubjectRef{teamSubject("QUIET")}, tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRowFreshness("QUIET", "unknown", "2026-02-21", false, false)}},
	}})
	add(t4Case{name: "health/team_daily_over_cap", kind: contextfabric.FactHealth, subjects: team, tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("CHAOS")}},
		{match: healthDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
			return healthTeamDailySeriesRow("CHAOS", day, "high", uint8(1), 0.61)
		})},
	}})
	add(t4Case{name: "health/project", kind: contextfabric.FactHealth, subjects: proj, tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "elevated", 0.55),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "", "high", 0.81),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-2", "full.chaos/svc", "unknown", 0.1),
		}},
		{match: healthDailySeriesMatch, rows: t4Rows(66, func(i int, day string) []any {
			return healthProjectDailySeriesRow("linear:proj-1", day, uint8(1), 0.71, "high")
		})},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", 2, 3, "repo", "repo-1", "high")}},
	}})
	add(t4Case{name: "health/project_no_known_severity", kind: contextfabric.FactHealth, subjects: proj, tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "unknown", 0.1),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", 0, 1, "", "", "")}},
	}})

	// ---- investment
	add(t4Case{name: "investment/team", kind: contextfabric.FactInvestment, subjects: team, tables: teamMixTables("CHAOS")})
	priorRow := themeMixRow("CHAOS", "", map[string]float64{"feature_delivery": 30, "operational": 70}, 0)
	priorRow[0] = uint8(1)
	start := time.Date(2026, 5, 30, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	add(t4Case{name: "investment/team_range_prior", kind: contextfabric.FactInvestment, subjects: team,
		time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		tables: []fakeTable{ownsRepoTable("CHAOS"), {match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 1),
			priorRow,
		}}}})
	add(t4Case{name: "investment/repository", kind: contextfabric.FactInvestment, subjects: []contextfabric.SubjectRef{repoSubject("repo-CHAOS")}, tables: []fakeTable{
		{match: "FROM work_unit_investments", rows: [][]any{
			themeMixRow("CHAOS", "", map[string]float64{"feature_delivery": 60, "operational": 20, "maintenance": 10, "quality": 6, "risk": 4}, 1),
		}},
	}})
	nullStream := investmentProjectRollupRow("linear", "proj-1", "team-2", "", "", "", 10, 5, 2, 100, 4.0)
	projectAB := []fakeTable{
		{match: "FROM investment_metrics_daily", rows: [][]any{
			investmentProjectRollupRow("linear", "proj-1", "team-1", "Team One", "product", "growth", 30, 12, 4, 850, 18.5),
			nullStream,
		}},
		// The phased roll-up (CHAOS-7271): scope, repo themes, repo bugfix, evidence arm.
		{match: "AS unit_ids", rows: [][]any{{[]string{"unit-1"}, []int64{1}}}},
		{match: "groupArray(link_kind)", rows: [][]any{{[]string{"linear"}, []string{"proj-1"}, []string{"repo"}, []string{"r:repo-1"}, [][]string{{"team-1"}}}}},
		{match: "uniqExactIf(u.repo_id", rows: [][]any{
			{"linear:proj-1", 60.0, 20.0, 10.0, 6.0, 4.0, uint64(9), uint64(2), uint64(2)},
		}},
		{match: "sumIf(u.bugfix_share", rows: [][]any{{"linear:proj-1", 1.0}}},
		{match: "excluded_no_repo_link", rows: [][]any{{"linear:proj-1", uint64(3)}}},
	}
	add(t4Case{name: "investment/project_rollup", kind: contextfabric.FactInvestment, subjects: proj, tables: projectAB})
	add(t4Case{name: "investment/project_native_over_rollup", kind: contextfabric.FactInvestment, subjects: proj, tables: append(append([]fakeTable{}, projectAB...),
		nativePhasedTables(nativeMixUnits("proj-1", 7))...)})

	// ---- entity kinds (CHAOS-7120)
	work := []contextfabric.SubjectRef{workItemSubject("repo-1", "WIDGET-101")}
	add(t4Case{name: "identity/repository", kind: contextfabric.FactIdentity, subjects: repo, tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", "example-org/widget-service", "synthetic"}}},
	}})
	add(t4Case{name: "identity/repository_no_labels", kind: contextfabric.FactIdentity, subjects: repo, tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1", "", ""}}},
	}})
	add(t4Case{name: "identity/work_item", kind: contextfabric.FactIdentity, subjects: work, tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "Investigate checkout flake", "repo-1"}, {"WIDGET-101", "", "repo-1"}}},
	}})
	add(t4Case{name: "membership/repository", kind: contextfabric.FactMembership, subjects: repo, tables: []fakeTable{
		{match: "FROM repos", rows: [][]any{{"repo-1"}}},
	}})
	add(t4Case{name: "membership/work_item", kind: contextfabric.FactMembership, subjects: work, tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service"}, {"WIDGET-101", "repo-1", ""}}},
	}})
	add(t4Case{name: "status/work_item", kind: contextfabric.FactStatus, subjects: work, tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "in_progress", "repo-1", ""}, {"WIDGET-101", "", "repo-1", ""}}},
	}})
	add(t4Case{name: "work/work_item", kind: contextfabric.FactWork, subjects: work, tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{{"WIDGET-101", "Investigate checkout flake", "repo-1"}, {"WIDGET-101", "", "repo-1"}}},
	}})
	add(t4Case{name: "actual_completion/work_item", kind: contextfabric.FactActualCompletion, subjects: work, tables: []fakeTable{
		{match: "FROM work_items", rows: [][]any{
			{"WIDGET-101", uint8(1), time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), "repo-1"},
			{"WIDGET-101", uint8(0), time.Unix(0, 0).UTC(), "repo-1"},
		}},
	}})
	add(t4Case{name: "actual_completion/project", kind: contextfabric.FactActualCompletion, subjects: proj, tables: []fakeTable{
		{match: completionRollupQueryMatch, rows: [][]any{completionRollupRow("linear", "proj-1", 5, 1, 1, 3)}},
	}})
	add(t4Case{name: "blockers/work_item", kind: contextfabric.FactBlockers, subjects: work, tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{
			{"WIDGET-099", "WIDGET-101", "repo-1", "repo-1", uint64(1)},
			{"WIDGET-098", "WIDGET-101", "repo-1", "repo-2", uint64(2)},
		}},
	}})
	add(t4Case{name: "required_children/work_item", kind: contextfabric.FactRequiredChildren, subjects: work, tables: []fakeTable{
		{match: "FROM work_item_dependencies", rows: [][]any{
			{"WIDGET-101", "WIDGET-200", "related_to", "repo-1", "repo-1", uint64(1)},
			{"WIDGET-101", "WIDGET-201", "", "repo-1", "", uint64(0)},
		}},
	}})
	add(t4Case{name: "pull_requests/pull_request", kind: contextfabric.FactPullRequests, subjects: []contextfabric.SubjectRef{pullRequestSubject("repo-1", "1042")}, tables: []fakeTable{
		{match: "FROM git_pull_requests", rows: [][]any{{"repo-1", uint32(1042), "open"}, {"repo-1", uint32(1042), ""}}},
	}})
	add(t4Case{name: "reviews/pull_request_review", kind: contextfabric.FactReviews, subjects: []contextfabric.SubjectRef{reviewSubject("repo-1", "review-1")}, tables: []fakeTable{
		{match: "FROM git_pull_request_reviews", rows: [][]any{{"review-1", "approved", "repo-1"}, {"review-1", "", "repo-1"}}},
	}})
	add(t4Case{name: "continuous_integration/ci_pipeline_run", kind: contextfabric.FactContinuousIntegration, subjects: []contextfabric.SubjectRef{ciRunSubject("repo-1", "run-1")}, tables: []fakeTable{
		{match: "FROM ci_pipeline_runs", rows: [][]any{{"run-1", "success", "repo-1"}, {"run-1", "", "repo-1"}}},
	}})
	noDurations := cicdMetricsRow("repo-1")
	noDurations[4], noDurations[6], noDurations[8] = uint8(0), uint8(0), uint8(0)
	add(t4Case{name: "continuous_integration/repository", kind: contextfabric.FactContinuousIntegration, subjects: repo, tables: []fakeTable{
		{match: "FROM cicd_metrics_daily", rows: [][]any{cicdMetricsRow("repo-1"), noDurations}},
	}})
	add(t4Case{name: "deployments/deployment", kind: contextfabric.FactDeployments, subjects: []contextfabric.SubjectRef{deploymentSubject("repo-1", "deploy-1")}, tables: []fakeTable{
		{match: "FROM deployments", rows: [][]any{{"deploy-1", "success", "production", "repo-1"}, {"deploy-1", "", "", "repo-1"}}},
	}})
	noDeployDurations := deployMetricsRow("repo-1")
	noDeployDurations[4], noDeployDurations[5], noDeployDurations[6], noDeployDurations[7] = uint8(0), float64(0), uint8(0), float64(0)
	add(t4Case{name: "deployments/repository", kind: contextfabric.FactDeployments, subjects: repo, tables: []fakeTable{
		{match: "FROM deploy_metrics_daily", rows: [][]any{deployMetricsRow("repo-1"), noDeployDurations}},
	}})
	add(t4Case{name: "incidents/incident", kind: contextfabric.FactIncidents, subjects: []contextfabric.SubjectRef{incidentSubject("incident-1")}, tables: []fakeTable{
		{match: "FROM operational_incidents", rows: [][]any{{"incident-1", "open", "high"}, {"incident-1", "", ""}}},
	}})
	cases = append(cases, t4TeamRollupCases()...)
	return cases
}

func t4ValueType(v contextfabric.FactValue) string {
	switch {
	case v.Rows != nil || v.Table != nil:
		return "table"
	case v.String != nil:
		return "string"
	case v.Integer != nil:
		return "integer"
	case v.Number != nil:
		return "number"
	case v.Boolean != nil:
		return "boolean"
	case v.Null:
		return "null"
	default:
		return "empty"
	}
}

// t4Check compares one emitted value with its declared type. It returns a
// problem description, or "".
func t4Check(v contextfabric.FactValue, declType contextfabric.FactFieldType, nullable bool) string {
	got := t4ValueType(v)
	switch {
	case got == "null":
		if !nullable {
			return "emitted null but declared non-nullable"
		}
	case got != string(declType):
		return fmt.Sprintf("emitted %s but declared %s", got, declType)
	}
	return ""
}

// t4Observed is what the cases emitted: "<kind>|<subject kind>|<field>" and
// "<kind>|<subject kind>|<table>.<column>".
type t4Observed struct {
	seen     map[string]bool
	subjects map[string]bool
	cases    map[contextfabric.FactKind]int
}

// t4Run drives every case and reports Rule 1's forward direction (emitted
// implies declared, same type and nullability) through report.
func t4Run(t *testing.T, cases []t4Case, capabilityOf func(contextfabric.FactKind) contextfabric.FactCapability, report func(format string, args ...any)) t4Observed {
	t.Helper()
	observed := t4Observed{seen: map[string]bool{}, subjects: map[string]bool{}, cases: map[contextfabric.FactKind]int{}}
	for _, c := range cases {
		client := &fakeClient{tables: c.tables}
		provider := findProvider(t, devhealthfacts.NewProviders(client), c.kind)
		timeContext := c.time
		if timeContext.Axis == "" {
			timeContext = contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
		}
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{Time: timeContext, Kind: c.kind, Subjects: c.subjects})
		if err != nil {
			report("case %s: ReadFacts() error = %v", c.name, err)
			continue
		}
		if len(result.Facts) == 0 {
			report("case %s: produced no fact; a case that emits nothing measures nothing", c.name)
			continue
		}
		observed.cases[c.kind]++
		capability := capabilityOf(c.kind)
		for _, fact := range result.Facts {
			subjectKind := fact.Subject.Kind
			observed.subjects[fmt.Sprintf("%s|%s", c.kind, subjectKind)] = true
			for name, value := range fact.Fields {
				decl, ok := capability.FieldDeclaration(name, subjectKind)
				if !ok {
					report("%s %s fact (case %s): emitted field %q is not declared", c.kind, subjectKind, c.name, name)
					continue
				}
				observed.seen[fmt.Sprintf("%s|%s|%s", c.kind, subjectKind, name)] = true
				if problem := t4Check(value, decl.Type, decl.Nullable); problem != "" {
					report("%s %s fact (case %s): field %q %s", c.kind, subjectKind, c.name, name, problem)
					continue
				}
				if decl.Type != contextfabric.FactFieldTable {
					continue
				}
				if value.Table != nil && value.Table.Shape == contextfabric.FactTableTimeSeries && !decl.DailySeries {
					report("%s %s fact (case %s): table %q is emitted as a time series but not declared a daily series, so no column of it is classified additive or non-additive", c.kind, subjectKind, c.name, name)
				}
				for _, row := range value.Rows {
					for column, cell := range row.Fields {
						columnDecl, ok := decl.Column(column)
						if !ok {
							report("%s %s fact (case %s): table %q emitted column %q is not declared", c.kind, subjectKind, c.name, name, column)
							continue
						}
						observed.seen[fmt.Sprintf("%s|%s|%s.%s", c.kind, subjectKind, name, column)] = true
						if problem := t4Check(cell, columnDecl.Type, columnDecl.Nullable); problem != "" {
							report("%s %s fact (case %s): table %q column %q %s", c.kind, subjectKind, c.name, name, column, problem)
						}
					}
				}
			}
		}
	}
	return observed
}

func t4Capability(kind contextfabric.FactKind) contextfabric.FactCapability {
	provider := findProviderT4(kind)
	return provider.Capability()
}

func findProviderT4(kind contextfabric.FactKind) contextfabric.FactProvider {
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		if provider.Capability().Kind == kind {
			return provider
		}
	}
	panic("no provider for kind " + string(kind))
}

// t4Reverse reports every declaration (per applicable subject kind) that no
// case emitted, unless listed in declaredButNotExercised, and every stale
// allowlist entry.
func t4Reverse(observed t4Observed, kinds []contextfabric.FactKind, allow map[contextfabric.FactKind]map[string]string, report func(format string, args ...any)) {
	for _, kind := range kinds {
		capability := t4Capability(kind)
		allowed := allow[kind]
		want := func(subject contextfabric.SubjectKind, path string, seenKey string) {
			key := fmt.Sprintf("%s:%s", subject, path)
			if observed.seen[seenKey] {
				if _, listed := allowed[key]; listed {
					report("%s: %q is listed in declaredButNotExercised but a case now exercises it; remove the entry", kind, key)
				}
				return
			}
			if _, listed := allowed[key]; listed {
				return
			}
			report("%s: declared %q is emitted by no case (add a case, or list it in declaredButNotExercised with a reason)", kind, key)
		}
		for _, subject := range capability.SupportedSubjectKinds {
			if !observed.subjects[fmt.Sprintf("%s|%s", kind, subject)] {
				report("%s: no case produced a fact for supported subject kind %s", kind, subject)
			}
			for _, field := range capability.Fields {
				if !field.AppliesTo(subject) {
					continue
				}
				want(subject, field.Name, fmt.Sprintf("%s|%s|%s", kind, subject, field.Name))
				for _, column := range field.Columns {
					want(subject, field.Name+"."+column.Name, fmt.Sprintf("%s|%s|%s.%s", kind, subject, field.Name, column.Name))
				}
			}
		}
		declared := map[string]bool{}
		for _, subject := range capability.SupportedSubjectKinds {
			for _, field := range capability.Fields {
				if !field.AppliesTo(subject) {
					continue
				}
				declared[fmt.Sprintf("%s:%s", subject, field.Name)] = true
				for _, column := range field.Columns {
					declared[fmt.Sprintf("%s:%s.%s", subject, field.Name, column.Name)] = true
				}
			}
		}
		for key, reason := range allowed {
			if strings.TrimSpace(reason) == "" {
				report("%s: declaredButNotExercised %q has no reason", kind, key)
			}
			if !declared[key] {
				report("%s: declaredButNotExercised %q names no declaration", kind, key)
			}
		}
	}
}

func TestCHAOS7073CatalogueTruthEveryEmittedFieldIsDeclaredAndEveryDeclarationIsEmitted(t *testing.T) {
	report := func(format string, args ...any) { t.Errorf(format, args...) }
	observed := t4Run(t, t4Cases(), t4Capability, report)
	t4Reverse(observed, directServableKinds, declaredButNotExercised, report)
}

// TestCHAOS7073EveryRegisteredProviderIsDeclared is Rule 4: a measurement
// that did not happen fails. Every registered kind is declared and has a
// catalogue-truth case (CHAOS-7120 removed the not-yet list).
func TestCHAOS7073EveryRegisteredProviderIsDeclared(t *testing.T) {
	cases := t4Cases()
	perKind := map[contextfabric.FactKind]int{}
	for _, c := range cases {
		perKind[c.kind]++
	}
	direct := map[contextfabric.FactKind]bool{}
	for _, kind := range directServableKinds {
		direct[kind] = true
	}
	if len(directServableKinds) != 21 || len(direct) != 21 {
		t.Fatalf("expected 21 distinct declared kinds, have %d (%d distinct)", len(directServableKinds), len(direct))
	}
	registered := map[contextfabric.FactKind]bool{}
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		capability := provider.Capability()
		kind := capability.Kind
		registered[kind] = true
		declared := len(capability.Fields) > 0
		switch {
		case !declared:
			t.Errorf("%s: registered provider declares no fields; every kind must be declared", kind)
		case !direct[kind]:
			t.Errorf("%s: declares fields but is not in the tested directServableKinds list", kind)
		case perKind[kind] == 0:
			t.Errorf("%s: declared kind has no catalogue-truth case", kind)
		}
	}
	if len(registered) != 21 {
		t.Errorf("registered providers = %d kinds, want 21", len(registered))
	}
	for kind := range direct {
		if !registered[kind] {
			t.Errorf("%s: listed as direct servable but no provider is registered", kind)
		}
	}
}

// TestCHAOS7073DeclarationsPassRegistryValidation builds the registry the
// production way; a declaration validateFieldDeclarations rejects fails here.
func TestCHAOS7073DeclarationsPassRegistryValidation(t *testing.T) {
	if _, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(&fakeClient{}), contextfabric.FactRegistryOptions{}); err != nil {
		t.Fatalf("NewFactCapabilityRegistry() error = %v", err)
	}
}

// TestCHAOS7073SafetyMarksOnDeclarations pins the marks the direct read tool
// relies on: health scores carry drivers, subject references are declared,
// and repository facts never claim an aggregate.
func TestCHAOS7073SafetyMarksOnDeclarations(t *testing.T) {
	health := t4Capability(contextfabric.FactHealth)
	for _, subject := range health.SupportedSubjectKinds {
		for _, name := range []string{"compounding_risk", "severity"} {
			decl, ok := health.FieldDeclaration(name, subject)
			if !ok || !decl.Score || decl.DriversTable == "" {
				t.Errorf("health %s %s: want Score with a drivers table, got %+v ok=%v", subject, name, decl, ok)
			}
			if subject != contextfabric.SubjectRepository && !decl.Aggregate {
				t.Errorf("health %s %s: want Aggregate", subject, name)
			}
			if subject == contextfabric.SubjectRepository && decl.Aggregate {
				t.Errorf("health repository %s: must not be Aggregate", name)
			}
		}
	}
	deficiencies := t4Capability(contextfabric.FactOperationalDeficiencies)
	if decl, ok := deficiencies.FieldDeclaration("severity", contextfabric.SubjectTeam); !ok || decl.Score {
		t.Errorf("operational_deficiencies severity is a rule label, not a score: %+v ok=%v", decl, ok)
	}
	risk, _ := health.FieldDeclaration("risk_breakdown", contextfabric.SubjectProject)
	scopeID, ok := risk.Column("scope_id")
	if !ok || scopeID.SubjectRef == nil || scopeID.SubjectRef.KindColumn != "scope" {
		t.Errorf("health risk_breakdown.scope_id must be a kind-column subject reference, got %+v", scopeID)
	}
	if scopeName, _ := risk.Column("scope_name"); scopeName.SubjectRef != nil {
		t.Errorf("health risk_breakdown.scope_name is a label and declares no subject reference")
	}
}

// TestCHAOS7120SafetyMarksOnEntityDeclarations pins the marks of the twelve
// entity kinds the direct read gate relies on.
func TestCHAOS7120SafetyMarksOnEntityDeclarations(t *testing.T) {
	ref := func(kind contextfabric.FactKind, subject contextfabric.SubjectKind, field string) *contextfabric.FactSubjectRefDeclaration {
		decl, ok := t4Capability(kind).FieldDeclaration(field, subject)
		if !ok {
			t.Fatalf("%s %s %s: not declared", kind, subject, field)
		}
		return decl.SubjectRef
	}
	for _, tc := range []struct {
		kind    contextfabric.FactKind
		subject contextfabric.SubjectKind
		field   string
		want    contextfabric.FactSubjectRefDeclaration
	}{
		// A bare work_item_id cannot be gated: opaque.
		{contextfabric.FactBlockers, contextfabric.SubjectWorkItem, "blocked_by_work_item_id", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDOpaque}},
		{contextfabric.FactRequiredChildren, contextfabric.SubjectWorkItem, "required_child_work_item_id", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDOpaque}},
		// The canonical id is gated like a work item root.
		{contextfabric.FactBlockers, contextfabric.SubjectWorkItem, "blocked_by_work_item_ref", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDCanonical}},
		{contextfabric.FactRequiredChildren, contextfabric.SubjectWorkItem, "required_child_work_item_ref", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectWorkItem, IDForm: contextfabric.FactSubjectIDCanonical}},
		{contextfabric.FactMembership, contextfabric.SubjectWorkItem, "repository_id", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectRepository, IDForm: contextfabric.FactSubjectIDRepositoryUUID}},
		{contextfabric.FactMembership, contextfabric.SubjectRepository, "organization_id", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectOrganization, IDForm: contextfabric.FactSubjectIDCanonical}},
		{contextfabric.FactIdentity, contextfabric.SubjectRepository, "id", contextfabric.FactSubjectRefDeclaration{Kind: contextfabric.SubjectRepository, IDForm: contextfabric.FactSubjectIDRepositoryUUID}},
	} {
		got := ref(tc.kind, tc.subject, tc.field)
		if got == nil || got.Kind != tc.want.Kind || got.IDForm != tc.want.IDForm || got.KindColumn != "" {
			t.Errorf("%s %s %s: subject ref = %+v, want %+v", tc.kind, tc.subject, tc.field, got, tc.want)
		}
	}
	// Labels and ratios are not scores; no entity field is an aggregate
	// (actual_completion's project counts are over the caller's own items).
	entity := directServableKinds[9:]
	for _, kind := range entity {
		capability := t4Capability(kind)
		for _, field := range capability.Fields {
			// Team rollup declarations (deployments, incidents, pull requests,
			// blockers) are a team-subject shape, pinned by
			// TestTeamRollupDeclarationMarks; this pin covers the entity roots.
			if len(field.SubjectKinds) == 1 && field.SubjectKinds[0] == contextfabric.SubjectTeam {
				continue
			}
			if field.Score || field.Aggregate {
				t.Errorf("%s %s: Score=%v Aggregate=%v, want neither on an entity kind", kind, field.Name, field.Score, field.Aggregate)
			}
			if field.Type == contextfabric.FactFieldTable {
				t.Errorf("%s %s: entity kinds emit no tables", kind, field.Name)
			}
		}
	}
	if len(entity) != 12 || entity[0] != contextfabric.FactIdentity {
		t.Fatalf("entity kinds = %v, want the twelve CHAOS-7120 kinds", entity)
	}
}

// TestCHAOS7073ValidateFieldDeclarationsRejects drives validateFieldDeclarations
// through NewFactCapabilityRegistry with a stub provider.
func TestCHAOS7073ValidateFieldDeclarationsRejects(t *testing.T) {
	base := func(fields []contextfabric.FactFieldDeclaration) contextfabric.FactCapability {
		capability := findProviderT4(contextfabric.FactSourceHealth).Capability()
		capability.Fields = fields
		return capability
	}
	table := contextfabric.FactFieldDeclaration{Name: "t", Type: contextfabric.FactFieldTable, Columns: []contextfabric.FactColumnDeclaration{
		{Name: "kind", Type: contextfabric.FactFieldString},
		{Name: "id", Type: contextfabric.FactFieldString, SubjectRef: &contextfabric.FactSubjectRefDeclaration{KindColumn: "kind_missing", KindByValue: map[string]contextfabric.SubjectKind{"a": contextfabric.SubjectTeam}, IDForm: contextfabric.FactSubjectIDTeamID}},
	}}
	cases := []struct {
		name   string
		fields []contextfabric.FactFieldDeclaration
		want   string
	}{
		{"score without drivers table", []contextfabric.FactFieldDeclaration{{Name: "risk", Type: contextfabric.FactFieldNumber, Score: true}}, "score without a declared drivers table"},
		{"score with undeclared drivers table", []contextfabric.FactFieldDeclaration{{Name: "risk", Type: contextfabric.FactFieldNumber, Score: true, DriversTable: "nope"}}, "score without a declared drivers table"},
		{"kind column not declared", []contextfabric.FactFieldDeclaration{table}, "is not declared"},
		{"duplicate field for the same subject kind", []contextfabric.FactFieldDeclaration{
			{Name: "a", Type: contextfabric.FactFieldString},
			{Name: "a", Type: contextfabric.FactFieldInteger, SubjectKinds: []contextfabric.SubjectKind{contextfabric.SubjectOrganization}},
		}, "declared twice"},
	}
	for _, tc := range cases {
		provider := &t4StubProvider{capability: base(tc.fields)}
		_, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to contain %q", tc.name, err, tc.want)
		}
	}
	valid := &t4StubProvider{capability: base([]contextfabric.FactFieldDeclaration{{Name: "a", Type: contextfabric.FactFieldString}})}
	if _, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{valid}, contextfabric.FactRegistryOptions{}); err != nil {
		t.Errorf("valid declaration rejected: %v", err)
	}
}

type t4StubProvider struct{ capability contextfabric.FactCapability }

func (p *t4StubProvider) Capability() contextfabric.FactCapability { return p.capability }
func (p *t4StubProvider) ReadFacts(context.Context, storage.Principal, contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return contextfabric.FactProviderResult{}, nil
}
