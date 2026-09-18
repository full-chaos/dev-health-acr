package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos5931Uint8 converts a bool to the ClickHouse-flavored uint8 flag
// convention every row scanner in this package already uses.
func chaos5931Uint8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// chaos5931WorkloadRow shapes one readers.WorkloadProjectRow scan row with
// full control over hasP50/p50Days/insufficientHistory -- unlike
// workloadProjectRollupRow (workload_test.go), which hard-codes "no p50, no
// insufficient_history" for its own team_breakdown-only assertions and
// cannot express the cases this file needs.
func chaos5931WorkloadRow(provider, projectID, teamID, teamName, workScopeID string, throughputMean, throughputStddev float64, hasP50 bool, p50Days int64, insufficientHistory, highVariance bool, backlogSize int64) []any {
	return []any{
		provider + ":" + projectID, uint8(1), teamID, teamName, workScopeID,
		throughputMean, throughputStddev,
		chaos5931Uint8(hasP50), p50Days, chaos5931Uint8(insufficientHistory), chaos5931Uint8(highVariance),
		backlogSize, "2026-07-27 04:00:00",
	}
}

// chaos5931UnattributedWorkloadRow is chaos5931WorkloadRow's HasTeam=0
// counterpart: the source's own team_id was NULL, so the row carries real
// coverage but no contributing team to cite as evidence for it.
func chaos5931UnattributedWorkloadRow(provider, projectID, workScopeID string, throughputMean, throughputStddev float64, hasP50 bool, p50Days int64, insufficientHistory, highVariance bool, backlogSize int64) []any {
	return []any{
		provider + ":" + projectID, uint8(0), "", "", workScopeID,
		throughputMean, throughputStddev,
		chaos5931Uint8(hasP50), p50Days, chaos5931Uint8(insufficientHistory), chaos5931Uint8(highVariance),
		backlogSize, "2026-07-27 04:00:00",
	}
}

// TestChaos5931SingleTeamPromotesWorstP50 is the basic happy path: one
// team_breakdown row, its own p50_days promoted verbatim with the winning
// row's own flags.
func TestChaos5931SingleTeamPromotesWorstP50(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 1, 0, 0, 14, "team-1", "scope-a", false, true)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, true, 14, false, true, 120),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 14 {
		t.Fatalf("forecast_p50_days = %#v, want 14", fact.Fields["forecast_p50_days"])
	}
	if got := fact.Fields["p50_basis"].String; got == nil || *got != "worst_of_team_breakdown" {
		t.Fatalf("p50_basis = %#v, want worst_of_team_breakdown", fact.Fields["p50_basis"])
	}
	if got := fact.Fields["insufficient_history"].Boolean; got == nil || *got {
		t.Fatalf("insufficient_history = %#v, want false", fact.Fields["insufficient_history"])
	}
	if got := fact.Fields["high_variance"].Boolean; got == nil || !*got {
		t.Fatalf("high_variance = %#v, want true", fact.Fields["high_variance"])
	}
	if got := fact.Fields["team_breakdown_rows_shown"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_breakdown_rows_shown = %#v, want 1", fact.Fields["team_breakdown_rows_shown"])
	}
	if got := fact.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_breakdown_rows_total = %#v, want 1", fact.Fields["team_breakdown_rows_total"])
	}
	if len(fact.EvidenceRefIDs) != 2 {
		t.Fatalf("evidence_ref_ids = %#v, want project + 1 team", fact.EvidenceRefIDs)
	}
}

// TestChaos5931MultiTeamMaxWinsFlagsFromWinnerOnly pins CHAOS-5931's core
// contract: the WORST (longest) p50 wins across the project's team_breakdown
// population, and insufficient_history/high_variance ride along ONLY from
// the winning row -- never a different row's flags, never a fabricated
// combination.
func TestChaos5931MultiTeamMaxWinsFlagsFromWinnerOnly(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 2, 0, 0, 40, "team-2", "scope-b", false, true)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, true, 5, true, false, 120),
			chaos5931WorkloadRow("linear", "proj-1", "team-2", "Team Two", "scope-b", 9.0, 2.1, true, 40, false, true, 40),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 40 {
		t.Fatalf("forecast_p50_days = %#v, want 40 (the worst/longest of 5 and 40)", fact.Fields["forecast_p50_days"])
	}
	// team-1's own flags (insufficient_history=true, high_variance=false)
	// must NOT leak onto the promoted value -- only team-2, the winner,
	// contributes flags.
	if got := fact.Fields["insufficient_history"].Boolean; got == nil || *got {
		t.Fatalf("insufficient_history = %#v, want false (the winning row's own flag, not team-1's)", fact.Fields["insufficient_history"])
	}
	if got := fact.Fields["high_variance"].Boolean; got == nil || !*got {
		t.Fatalf("high_variance = %#v, want true (the winning row's own flag)", fact.Fields["high_variance"])
	}
}

// TestChaos5931TieAtMaxReportsOneValue pins the ties-at-max cell: two rows
// reporting the identical worst p50 must still promote a single value,
// never a duplicated or malformed one.
func TestChaos5931TieAtMaxReportsOneValue(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 2, 0, 0, 40, "team-1", "scope-a", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, true, 40, false, false, 120),
			chaos5931WorkloadRow("linear", "proj-1", "team-2", "Team Two", "scope-b", 9.0, 2.1, true, 40, false, false, 40),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 40 {
		t.Fatalf("forecast_p50_days = %#v, want 40", fact.Fields["forecast_p50_days"])
	}
}

// TestChaos5931NullP50AmongOthersExcludedFromMax pins the exclusion half of
// the rule: a row with no recorded p50 must never win the max, and must
// never suppress a real reading elsewhere in the same breakdown.
func TestChaos5931NullP50AmongOthersExcludedFromMax(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 1, 0, 1, 20, "team-2", "scope-b", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, false, 0, false, false, 120),
			chaos5931WorkloadRow("linear", "proj-1", "team-2", "Team Two", "scope-b", 9.0, 2.1, true, 20, false, false, 40),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 20 {
		t.Fatalf("forecast_p50_days = %#v, want 20 (the NULL row must not win or suppress the known reading)", fact.Fields["forecast_p50_days"])
	}
	if got := fact.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 2 {
		t.Fatalf("team_breakdown_rows_total = %#v, want 2 (both rows disclosed, even the NULL one)", fact.Fields["team_breakdown_rows_total"])
	}
	rows := fact.Fields["team_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2 -- the NULL-p50 row must not be dropped", rows)
	}
}

// TestChaos5931AllNullP50DisclosesReasonNoFabricatedZero covers the "only
// NULL" cell: a project whose breakdown rows all report no p50 still gets a
// fact (rollup_basis/team_count/team_breakdown stay honest disclosures of
// what WAS read), but forecast_p50_days/p50_basis are absent -- never a
// fabricated 0 days -- and p50_unavailable_reason names why.
func TestChaos5931AllNullP50DisclosesReasonNoFabricatedZero(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 0, 0, 2, 0, "", "", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, false, 0, false, false, 120),
			chaos5931WorkloadRow("linear", "proj-1", "team-2", "Team Two", "scope-b", 9.0, 2.1, false, 0, false, false, 40),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1 (the breakdown is real, only its p50 is undetermined)", result.Facts)
	}
	fact := result.Facts[0]
	if _, ok := fact.Fields["forecast_p50_days"]; ok {
		t.Fatalf("fields = %#v, want forecast_p50_days absent when every breakdown row is NULL", fact.Fields)
	}
	if _, ok := fact.Fields["p50_basis"]; ok {
		t.Fatalf("fields = %#v, want p50_basis absent when forecast_p50_days is undetermined", fact.Fields)
	}
	if got := fact.Fields["p50_unavailable_reason"].String; got == nil || *got != "no_known_forecast_p50_days" {
		t.Fatalf("p50_unavailable_reason = %#v", fact.Fields["p50_unavailable_reason"])
	}
	if len(fact.Fields["team_breakdown"].Rows) != 2 {
		t.Fatalf("team_breakdown rows = %#v, want 2 (both NULL rows still disclosed)", fact.Fields["team_breakdown"].Rows)
	}
}

// TestChaos5931ProjectPresentOnlyInAggregateStillServed pins the
// population-source fix directly, mirroring
// TestHealthProviderProjectPresentOnlyInAggregateStillServed: two projects
// are requested together, but the row-level breakdown scan (the fake
// client's canned response) answers for only ONE of them -- as the shared,
// row-capped real scan would for a project sitting past its budget. The p50
// aggregate answers for BOTH. The project absent from the row-level scan
// must still be served, with an empty team_breakdown (never a fabricated
// one), an honest rows_shown=0 vs rows_total disclosure, and a citable
// evidence ref for the winning team the aggregate names.
func TestChaos5931ProjectPresentOnlyInAggregateStillServed(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{
			workloadP50MaxRow("linear", "proj-shown", 1, 0, 0, 5, "team-shown", "scope-shown", false, false),
			workloadP50MaxRow("linear", "proj-late", 1, 0, 0, 90, "team-late", "scope-late", true, false),
		}},
		// Only proj-shown has a row-level breakdown row; proj-late has none,
		// simulating a project the shared scan never reached.
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-shown", "team-shown", "Team Shown", "scope-shown", 3.2, 0.8, true, 5, false, false, 120),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-shown"), projectSubject("linear", "proj-late")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 -- proj-late must be served even though the row-level scan never reached it", result.Facts)
	}
	var late *contextfabric.CanonicalFact
	for i := range result.Facts {
		if result.Facts[i].Subject.CanonicalID == projectSubject("linear", "proj-late").CanonicalID {
			late = &result.Facts[i]
		}
	}
	if late == nil {
		t.Fatalf("facts = %#v, want proj-late present", result.Facts)
	}
	if got := late.Fields["forecast_p50_days"].Integer; got == nil || *got != 90 {
		t.Fatalf("proj-late forecast_p50_days = %#v, want 90 (from the aggregate, independent of the row-level scan)", late.Fields["forecast_p50_days"])
	}
	if got := late.Fields["insufficient_history"].Boolean; got == nil || !*got {
		t.Fatalf("proj-late insufficient_history = %#v, want true (the winning row's own flag)", late.Fields["insufficient_history"])
	}
	if _, ok := late.Fields["team_breakdown"]; ok {
		t.Fatalf("fields = %#v, want team_breakdown absent -- the row-level scan produced zero rows for this project, and a FactTable cannot declare zero rows", late.Fields)
	}
	if got := late.Fields["team_breakdown_rows_shown"].Integer; got == nil || *got != 0 {
		t.Fatalf("team_breakdown_rows_shown = %#v, want 0", late.Fields["team_breakdown_rows_shown"])
	}
	if got := late.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 1 {
		t.Fatalf("team_breakdown_rows_total = %#v, want 1 (the aggregate's own uncapped count)", late.Fields["team_breakdown_rows_total"])
	}
	var citesLateTeam bool
	for _, ref := range late.EvidenceRefIDs {
		if strings.Contains(ref, "team-late") {
			citesLateTeam = true
		}
	}
	if !citesLateTeam {
		t.Fatalf("evidence_ref_ids = %#v, want a citable ref for team-late even though its row never reached the display scan", late.EvidenceRefIDs)
	}
}

// TestChaos5931MixedRootRequestNeitherRootSuppressed is the mixed-root case:
// a team subject and a project subject requested in ONE ReadFacts call must
// each roll up independently -- the team's own scalar forecast_p50_days
// (readTeamWorkload's own read) stays exactly what it always was, unaffected
// by the project rollup's worst-case computation running in the same call,
// and neither root starves the other's query on the shared fake client.
func TestChaos5931MixedRootRequestNeitherRootSuppressed(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 1, 0, 0, 7, "team-p", "scope-p", false, false)}},
		{match: workloadProjectRollupMatch, rows: [][]any{
			chaos5931WorkloadRow("linear", "proj-1", "team-p", "Team P", "scope-p", 1.5, 0.2, true, 7, false, false, 10),
		}},
		{match: workloadBaseQueryMatch, rows: [][]any{workloadRow("CHAOS", "scope-team")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS"), projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 (both roots served in the same call, neither skipped)", result.Facts)
	}
	var teamFact, projectFact *contextfabric.CanonicalFact
	for i := range result.Facts {
		switch result.Facts[i].Subject.Kind {
		case contextfabric.SubjectTeam:
			teamFact = &result.Facts[i]
		case contextfabric.SubjectProject:
			projectFact = &result.Facts[i]
		}
	}
	if teamFact == nil || projectFact == nil {
		t.Fatalf("facts = %#v, want one team fact and one project fact", result.Facts)
	}
	// workloadRow's own scalar forecast_p50_days (14) is untouched by the
	// project rollup's worst-case computation running in the same call.
	if got := teamFact.Fields["forecast_p50_days"].Integer; got == nil || *got != 14 {
		t.Fatalf("team forecast_p50_days = %#v, want 14 (readTeamWorkload's own scalar, unaffected)", teamFact.Fields["forecast_p50_days"])
	}
	if got := projectFact.Fields["forecast_p50_days"].Integer; got == nil || *got != 7 {
		t.Fatalf("project forecast_p50_days = %#v, want 7 (the promoted worst case in its own breakdown)", projectFact.Fields["forecast_p50_days"])
	}
	if got := projectFact.Fields["p50_basis"].String; got == nil || *got != "worst_of_team_breakdown" {
		t.Fatalf("project p50_basis = %#v, want worst_of_team_breakdown", projectFact.Fields["p50_basis"])
	}
}

// TestChaos5931UnattributedForecastNeverWinsOrCountsAsKnown pins the
// team-derived contract directly: an unattributed row (no contributing
// team) with a LARGER p50 than every attributed row must neither win the
// promotion nor be counted as known -- it has no citable evidence, and
// crediting it would award ranking points for a reading nobody can
// attribute to a team.
func TestChaos5931UnattributedForecastNeverWinsOrCountsAsKnown(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 1, 1, 0, 10, "team-1", "scope-a", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931UnattributedWorkloadRow("linear", "proj-1", "scope-unattributed", 1.0, 0.1, true, 999, false, false, 4),
			chaos5931WorkloadRow("linear", "proj-1", "team-1", "Team One", "scope-a", 3.2, 0.8, true, 10, false, false, 120),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["forecast_p50_days"].Integer; got == nil || *got != 10 {
		t.Fatalf("forecast_p50_days = %#v, want 10 (team-1's own reading -- the unattributed row's 999 must never win)", fact.Fields["forecast_p50_days"])
	}
	if got := fact.Fields["p50_known_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("p50_known_count = %#v, want 1 (the unattributed row is never counted as known)", fact.Fields["p50_known_count"])
	}
	if got := fact.Fields["p50_excluded_unattributed_count"].Integer; got == nil || *got != 1 {
		t.Fatalf("p50_excluded_unattributed_count = %#v, want 1", fact.Fields["p50_excluded_unattributed_count"])
	}
	for _, ref := range fact.EvidenceRefIDs {
		if strings.Contains(ref, "scope-unattributed") {
			t.Fatalf("evidence_ref_ids = %#v, want no ref for the unattributed row's own scope", fact.EvidenceRefIDs)
		}
	}
}

// TestChaos5931AllUnattributedDisclosesReasonNotFabricatedValue covers the
// "only unattributed" cell: every reachable row carries a REAL p50 but none
// has a contributing team, so the project must disclose the unavailable
// reason, never fabricate a value off a reading nobody can attribute.
func TestChaos5931AllUnattributedDisclosesReasonNotFabricatedValue(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: workloadP50MaxMatch, rows: [][]any{workloadP50MaxRow("linear", "proj-1", 0, 2, 0, 0, "", "", false, false)}},
		{match: workloadBaseQueryMatch, rows: [][]any{
			chaos5931UnattributedWorkloadRow("linear", "proj-1", "scope-a", 1.0, 0.1, true, 30, false, false, 4),
			chaos5931UnattributedWorkloadRow("linear", "proj-1", "scope-b", 1.0, 0.1, true, 60, false, false, 4),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactWorkload)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactWorkload, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if _, ok := fact.Fields["forecast_p50_days"]; ok {
		t.Fatalf("fields = %#v, want forecast_p50_days absent -- both readings are real but neither has a contributing team", fact.Fields)
	}
	if got := fact.Fields["p50_unavailable_reason"].String; got == nil || *got != "no_known_forecast_p50_days" {
		t.Fatalf("p50_unavailable_reason = %#v", fact.Fields["p50_unavailable_reason"])
	}
	if got := fact.Fields["p50_excluded_unattributed_count"].Integer; got == nil || *got != 2 {
		t.Fatalf("p50_excluded_unattributed_count = %#v, want 2", fact.Fields["p50_excluded_unattributed_count"])
	}
	if got := fact.Fields["team_breakdown_rows_total"].Integer; got == nil || *got != 2 {
		t.Fatalf("team_breakdown_rows_total = %#v, want 2 (both real, unattributed rows still disclosed)", fact.Fields["team_breakdown_rows_total"])
	}
}
