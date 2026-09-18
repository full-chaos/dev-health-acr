package devhealthfacts_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func teamSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:" + id, Label: id}
}

func organizationSubject(id string) contextfabric.SubjectRef {
	return contextfabric.SubjectRef{Kind: contextfabric.SubjectOrganization, CanonicalID: "organization:" + id, Label: id}
}

// healthRow shapes readScope's own widened SELECT (CHAOS-4418): scope_id,
// severity, has_risk, compounding_risk, computed_at, then one
// (has_norm, norm, weight) triple per riskRuleComponents entry in that
// list's own order (churn, complexity, ownership, review).
func healthRow(scopeID string) []any {
	return []any{
		scopeID, "elevated", uint8(1), float64(0.62), "2026-02-21 00:00:00",
		uint8(1), float64(0.3), float64(0.4), // churn
		uint8(1), float64(0.2), float64(0.3), // complexity
		uint8(1), float64(0.5), float64(0.2), // ownership
		uint8(1), float64(0.1), float64(0.1), // review
	}
}

// healthScalarMatch distinguishes readScope's own OLD rn=1-per-scope_id
// scalar/risk_rules query from CHAOS-4645's NEW per-day series queries
// (queryTeamHealthDailySeries / queryProjectHealthDailySeries): only
// readScope's tiebreak hash widens to churn_norm (CHAOS-4418), so this
// substring can never appear in either new query's statement text. A test
// that registers only healthDailySeriesMatch's canned rows under the old
// broad "FROM compounding_risk_daily" match would corrupt the OTHER
// query's scan the moment a team/project subject triggers both queries in
// one ReadFacts call -- see flow_test.go's identical flowDailySeriesMatch
// fix for the same bug class on FlowProvider.
const healthScalarMatch = "ifNull(churn_norm"

// healthProjectRollupMatch is healthScalarMatch's readProjectHealth
// counterpart: "ifNull(t.name, ”)" (the LEFT JOIN teams alias) appears only
// in the OLD project rollup query's team branch, never in
// queryProjectHealthDailySeries, which joins no `teams` table at all.
const healthProjectRollupMatch = "ifNull(t.name, '')"

// healthDailySeriesMatch distinguishes the CHAOS-4645 daily-series queries
// (queryTeamHealthDailySeries and queryProjectHealthDailySeries share this
// EXACT row_number() PARTITION BY signature, so one match constant covers
// both) from the pre-existing latest-row queries.
const healthDailySeriesMatch = "PARTITION BY scope_id, day ORDER BY computed_at DESC"

// healthTeamDailySeriesRow shapes one queryTeamHealthDailySeries output row:
// (scope_id, day, severity, has_risk, risk).
func healthTeamDailySeriesRow(scopeID, day, severity string, hasRisk uint8, risk float64) []any {
	return []any{scopeID, day, severity, hasRisk, risk}
}

// healthProjectDailySeriesRow shapes one queryProjectHealthDailySeries
// output row: (project_key, day, has_risk, risk, severity) -- has_risk/risk
// come from max(risk) and severity from argMax(severity, ...), so the
// column ORDER differs from healthTeamDailySeriesRow's even though both
// build the same healthDailyRow shape Go-side.
func healthProjectDailySeriesRow(projectKey, day string, hasRisk uint8, risk float64, severity string) []any {
	return []any{projectKey, day, hasRisk, risk, severity}
}

// healthSeverityMaxMatch distinguishes queryProjectHealthSeverityMax's own
// server-side worst-band aggregate ("argMax(severity, band)", a plain
// GROUP BY with no row_number()/PARTITION BY at all and no `teams` join)
// from every other query in this file -- healthDailySeriesMatch's
// PARTITION BY signature and healthProjectRollupMatch's teams-alias join
// never appear in it.
const healthSeverityMaxMatch = "argMax(severity, band)"

// healthSeverityMaxRow shapes one queryProjectHealthSeverityMax output row:
// (project_key, severity).
func healthSeverityMaxRow(provider, projectID, severity string) []any {
	return []any{provider + ":" + projectID, severity}
}

func TestHealthProviderRepoScopeHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{healthRow("repo-1")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["severity"].String == nil || *fact.Fields["severity"].String != "elevated" {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	if fact.Fields["compounding_risk"].Number == nil || *fact.Fields["compounding_risk"].Number != 0.62 {
		t.Fatalf("fields = %#v", fact.Fields)
	}
	// CHAOS-4418: the formula's own 4 weighted components, not just the
	// combined score.
	riskRules := fact.Fields["risk_rules"].Rows
	if len(riskRules) != 4 {
		t.Fatalf("risk_rules = %#v, want 4 rows (churn/complexity/ownership/review)", riskRules)
	}
	churn := riskRules[0].Fields
	if churn["signal"].String == nil || *churn["signal"].String != "churn" {
		t.Fatalf("risk_rules[0].signal = %#v, want churn", churn["signal"])
	}
	if churn["norm_value"].Number == nil || *churn["norm_value"].Number != 0.3 {
		t.Fatalf("risk_rules[0].norm_value = %#v, want 0.3", churn["norm_value"])
	}
	if churn["weight"].Number == nil || *churn["weight"].Number != 0.4 {
		t.Fatalf("risk_rules[0].weight = %#v, want 0.4", churn["weight"])
	}
	if churn["weighted_contribution"].Number == nil || *churn["weighted_contribution"].Number != 0.3*0.4 {
		t.Fatalf("risk_rules[0].weighted_contribution = %#v, want 0.12", churn["weighted_contribution"])
	}
	// canonicalFieldRows (model_runtime.go) fails closed on more than one
	// Rows-shaped field per fact -- this fact must carry exactly one.
	rowsFieldCount := 0
	for _, value := range fact.Fields {
		if len(value.Rows) > 0 {
			rowsFieldCount++
		}
	}
	if rowsFieldCount != 1 {
		t.Fatalf("rows-shaped field count = %d, want exactly 1 (fields = %#v)", rowsFieldCount, fact.Fields)
	}
	if !strings.Contains(client.queries[len(client.queries)-1].statement, "scope = 'repo'") {
		t.Fatalf("statement = %q, want scope='repo'", client.queries[len(client.queries)-1].statement)
	}
}

func TestHealthProviderTeamScopeHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: healthScalarMatch, rows: [][]any{healthRow("CHAOS")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	if !strings.Contains(client.queries[len(client.queries)-1].statement, "scope = 'team'") {
		t.Fatalf("statement = %q, want scope='team'", client.queries[len(client.queries)-1].statement)
	}
}

func TestHealthProviderNoRiskScoreOmitsField(t *testing.T) {
	t.Parallel()
	row := healthRow("repo-1")
	row[2] = uint8(0)
	row[3] = float64(0)
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if _, ok := result.Facts[0].Fields["compounding_risk"]; ok {
		t.Fatalf("fields = %#v, want compounding_risk omitted", result.Facts[0].Fields)
	}
}

// TestHealthProviderUnrecordedNormIsNullNotZero pins AGENTS.md North Star
// check 12 (missing is not healthy -- unknown/zero are distinct) for the
// per-rule breakdown: a component whose has_norm flag is false must report
// norm_value/weighted_contribution as an explicit null, never a fabricated
// 0 that would understate its real, unrecorded contribution to the score.
func TestHealthProviderUnrecordedNormIsNullNotZero(t *testing.T) {
	t.Parallel()
	row := healthRow("repo-1")
	row[5], row[6] = uint8(0), float64(0) // churn_norm unrecorded
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{row}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	churn := result.Facts[0].Fields["risk_rules"].Rows[0].Fields
	if !churn["norm_value"].Null {
		t.Fatalf("risk_rules[0].norm_value = %#v, want an explicit null, not a fabricated zero", churn["norm_value"])
	}
	if !churn["weighted_contribution"].Null {
		t.Fatalf("risk_rules[0].weighted_contribution = %#v, want an explicit null", churn["weighted_contribution"])
	}
	// The signal name and its configured weight are still real, recorded
	// facts even though this particular org/day never computed the norm.
	if churn["signal"].String == nil || *churn["signal"].String != "churn" {
		t.Fatalf("risk_rules[0].signal = %#v, want churn even when its norm is unrecorded", churn["signal"])
	}
	if churn["weight"].Number == nil || *churn["weight"].Number != 0.4 {
		t.Fatalf("risk_rules[0].weight = %#v, want 0.4 even when the norm is unrecorded", churn["weight"])
	}
}

func TestHealthProviderZeroRowSubjectHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

func TestHealthProviderQueryErrorReturnsFactReadFailure(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", err: errors.New("boom")}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	var failure *contextfabric.FactReadFailure
	if !errors.As(err, &failure) || failure.State != contextfabric.SourceUnavailable {
		t.Fatalf("err = %v", err)
	}
}

func TestHealthProviderScopedToOrgAndRequestedSubjects(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	_, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-8"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if got := client.orgIDBinding(); got != "org-8" {
		t.Fatalf("org_id binding = %q", got)
	}
	if got := client.idsBinding(); len(got) != 1 || got[0] != "repo-1" {
		t.Fatalf("ids binding = %#v, want exactly the requested subject", got)
	}
	assertQueryScopedToOrgAndSubjects(t, client.queries[len(client.queries)-1].statement)
}

// TestHealthProviderRowForUnrequestedRepositoryNeverAppears is the F5
// result-content guard.
func TestHealthProviderRowForUnrequestedRepositoryNeverAppears(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{healthRow("repo-other-org")}}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 {
		t.Fatalf("facts = %#v, want empty -- the returned row belongs to an unrequested repository", result.Facts)
	}
}

// healthProjectRollupRow shapes one row of the project rollup UNION output:
// (project_key, scope, scope_id, scope_name, severity, hasRisk, risk,
// computed_at) -- the SAME 8-column shape both the team and repo UNION
// branches select, so one canned row list stands in for either or both.
func healthProjectRollupRow(provider, projectID, scope, scopeID, scopeName, severity string, risk float64) []any {
	return []any{provider + ":" + projectID, scope, scopeID, scopeName, severity, uint8(1), risk, "2026-02-21 00:00:00"}
}

// TestHealthProviderProjectRollupBreaksDownByTeamAndRepoNeverSums pins
// CHAOS-4363's two-layer contract: a project's health rollup carries BOTH
// its owning teams' own compounding_risk_daily rows (scope='team') AND the
// repositories those teams own (scope='repo'), never summed or averaged
// into one project-level score.
func TestHealthProviderProjectRollupBreaksDownByTeamAndRepoNeverSums(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "elevated", 0.55),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "high", 0.81),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", "high")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	if fact.Fields["rollup_basis"].String == nil || *fact.Fields["rollup_basis"].String != "team_project_ownership_and_team_repo_ownership" {
		t.Fatalf("rollup_basis = %#v", fact.Fields["rollup_basis"])
	}
	if fact.Fields["team_count"].Integer == nil || *fact.Fields["team_count"].Integer != 1 {
		t.Fatalf("team_count = %#v, want 1", fact.Fields["team_count"])
	}
	if fact.Fields["repo_count"].Integer == nil || *fact.Fields["repo_count"].Integer != 1 {
		t.Fatalf("repo_count = %#v, want 1", fact.Fields["repo_count"])
	}
	if _, hasTop := fact.Fields["compounding_risk"]; hasTop {
		t.Fatalf("fields = %#v, want no project-level compounding_risk", fact.Fields)
	}
	// The team row is "elevated", the repo row is "high" -- severity
	// promotes the worst band across both, disclosing which combined
	// population produced it.
	if got := fact.Fields["severity"].String; got == nil || *got != "high" {
		t.Fatalf("severity = %#v, want high (the worst band across team+repo)", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_basis"].String; got == nil || *got != "worst_of_team_and_repo_breakdown" {
		t.Fatalf("severity_basis = %#v", fact.Fields["severity_basis"])
	}
	rows := fact.Fields["risk_breakdown"].Rows
	if len(rows) != 2 {
		t.Fatalf("risk_breakdown rows = %#v, want 2", rows)
	}
	if got := rows[0].Fields["scope"].String; got == nil || *got != "team" {
		t.Fatalf("row[0].scope = %#v, want team", got)
	}
	if got := rows[1].Fields["scope"].String; got == nil || *got != "repo" {
		t.Fatalf("row[1].scope = %#v, want repo", got)
	}
	if len(fact.EvidenceRefIDs) != 3 {
		t.Fatalf("evidence_ref_ids = %#v, want project + 1 team + 1 repo", fact.EvidenceRefIDs)
	}
}

// TestHealthProviderProjectRollupAllUnknownSeverityDisclosesReason covers
// the "only unknown" cell: a project whose breakdown rows all report
// "unknown" still gets a fact (rollup_basis/team_count/risk_breakdown
// stay honest disclosures of what WAS read), but severity/severity_basis
// are absent -- never a defaulted "low" -- and severity_unavailable_reason
// names why.
func TestHealthProviderProjectRollupAllUnknownSeverityDisclosesReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "unknown", 0.10),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "unknown", 0.20),
		}},
		// The server-side aggregate's own HAVING max(band) > 0 excludes an
		// all-unknown project, so it never appears in this query's output.
		{match: healthSeverityMaxMatch, rows: nil},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1 (the breakdown is real, only its severity is undetermined)", result.Facts)
	}
	fact := result.Facts[0]
	if _, ok := fact.Fields["severity"]; ok {
		t.Fatalf("fields = %#v, want severity absent when every breakdown row is unknown", fact.Fields)
	}
	if _, ok := fact.Fields["severity_basis"]; ok {
		t.Fatalf("fields = %#v, want severity_basis absent when severity is undetermined", fact.Fields)
	}
	if got := fact.Fields["severity_unavailable_reason"].String; got == nil || *got != "no_known_severity_breakdown_rows" {
		t.Fatalf("severity_unavailable_reason = %#v", fact.Fields["severity_unavailable_reason"])
	}
	if len(fact.Fields["risk_breakdown"].Rows) != 2 {
		t.Fatalf("risk_breakdown rows = %#v, want 2 (both unknown rows still disclosed)", fact.Fields["risk_breakdown"].Rows)
	}
}

// TestHealthProviderProjectRollupExcludesUnknownFromWorstBand pins the
// exclusion half of the rule separately from the all-unknown case above: a
// mix of one unknown row and one known row must promote the KNOWN band,
// never let the unknown row win or suppress the known reading.
func TestHealthProviderProjectRollupExcludesUnknownFromWorstBand(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "unknown", 0.10),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "low", 0.05),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", "low")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "low" {
		t.Fatalf("severity = %#v, want low (the unknown row must not win or suppress the known reading)", fact.Fields["severity"])
	}
}

// TestHealthProviderProjectRollupTieAtHighReportsOneValue pins the ties-at-
// high cell: two rows both reporting "high" must still promote a single
// "high" value, never a duplicated or malformed one.
func TestHealthProviderProjectRollupTieAtHighReportsOneValue(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "high", 0.90),
			healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "high", 0.95),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", "high")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	fact := result.Facts[0]
	if got := fact.Fields["severity"].String; got == nil || *got != "high" {
		t.Fatalf("severity = %#v, want high", fact.Fields["severity"])
	}
}

// TestHealthProviderMixedRootSubjectsProjectSeverityNeverLeaksIntoRepo is
// the mixed-root case: a repository subject and a project subject
// requested in ONE ReadFacts call must each roll up independently -- the
// repository's own scalar severity (readScope's rn=1-per-scope_id read)
// stays exactly what it always was, unaffected by the project rollup's
// worst-band computation running in the same call.
func TestHealthProviderMixedRootSubjectsProjectSeverityNeverLeaksIntoRepo(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		// healthScalarMatch ("ifNull(churn_norm") is unique to readScope's
		// own statement -- unlike a bare "scope = 'repo'" substring, it
		// never also matches readProjectHealth's repo-layer subquery
		// (compoundingRiskLatestSubquery("repo", ...), which selects no
		// risk_rules columns at all).
		{match: healthScalarMatch, rows: [][]any{healthRow("repo-1")}},
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "low", 0.05),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", "low")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1"), projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 2 {
		t.Fatalf("facts = %#v, want 2 (both roots served in the same call, neither skipped)", result.Facts)
	}
	var repoFact, projectFact *contextfabric.CanonicalFact
	for i := range result.Facts {
		switch result.Facts[i].Subject.Kind {
		case contextfabric.SubjectRepository:
			repoFact = &result.Facts[i]
		case contextfabric.SubjectProject:
			projectFact = &result.Facts[i]
		}
	}
	if repoFact == nil || projectFact == nil {
		t.Fatalf("facts = %#v, want one repository fact and one project fact", result.Facts)
	}
	// healthRow's own scalar severity ("elevated") is untouched by the
	// project rollup's worst-band computation running in the same call.
	if got := repoFact.Fields["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("repository severity = %#v, want elevated (readScope's own scalar, unaffected)", repoFact.Fields["severity"])
	}
	if got := projectFact.Fields["severity"].String; got == nil || *got != "low" {
		t.Fatalf("project severity = %#v, want low (the worst -- and only -- band in its own breakdown)", projectFact.Fields["severity"])
	}
}

func TestHealthProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: healthProjectRollupMatch, rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceNoData {
		t.Fatalf("result = %+v", result)
	}
}

const maxHealthRowsPerQueryForTest = 200

func healthRows(n int) [][]any {
	rows := make([][]any, n)
	for i := 0; i < n; i++ {
		rows[i] = healthRow("repo-" + strconv.Itoa(i))
	}
	return rows
}

func TestHealthProviderTruncatesWhenRowCountReachesLimit(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: healthRows(maxHealthRowsPerQueryForTest)}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{repoSubject("repo-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if !result.Truncated {
		t.Fatalf("result.Truncated = false, want true when the row count reaches the limit")
	}
	if len(client.queries) == 0 || !strings.Contains(strings.ToUpper(client.queries[len(client.queries)-1].statement), "LIMIT") {
		t.Fatalf("query statement = %#v, want a LIMIT clause", client.queries)
	}
}

// TestHealthProviderTeamReadsDailyHealthSeries is CHAOS-4645's core team
// shape (design doc §5.2): a genuine time_series alongside the existing
// scalar severity/compounding_risk and risk_rules breakdown, additive --
// those must stay exactly as before this ticket.
func TestHealthProviderTeamReadsDailyHealthSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("team-1")}},
		{match: healthDailySeriesMatch, rows: [][]any{
			healthTeamDailySeriesRow("team-1", "2026-02-21", "high", uint8(1), 0.61),
			healthTeamDailySeriesRow("team-1", "2026-02-20", "elevated", uint8(1), 0.42),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	// Additive: the pre-existing scalars/breakdown are untouched.
	if fact.Fields["severity"].String == nil || *fact.Fields["severity"].String != "elevated" {
		t.Fatalf("severity = %#v, want unchanged at elevated (healthRow's own scalar)", fact.Fields["severity"])
	}
	if fact.Fields["compounding_risk"].Number == nil || *fact.Fields["compounding_risk"].Number != 0.62 {
		t.Fatalf("compounding_risk = %#v, want unchanged at 0.62", fact.Fields["compounding_risk"])
	}
	if len(fact.Fields["risk_rules"].Rows) != 4 {
		t.Fatalf("risk_rules rows = %#v, want unchanged at 4", fact.Fields["risk_rules"].Rows)
	}
	table := fact.Fields["daily_health"].Table
	if table == nil {
		t.Fatal("daily_health field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_health.Shape = %q, want time_series", table.Shape)
	}
	if err := fact.Fields["daily_health"].Validate(); err != nil {
		t.Fatalf("daily_health fails FactValue.Validate(): %v", err)
	}
	// CHAOS-4680: severity is a per-day categorical OBSERVATION, not a
	// quantity, and must be declared as one -- a Measures column is now
	// producer-validated numeric-only (FactTable.Validate), so a
	// regression that puts severity back in Measures fails Validate()
	// above, since the cell below is a string. This assertion pins WHERE
	// it lives, not merely that validation happened to pass.
	for _, measure := range table.Measures {
		if measure == "severity" {
			t.Fatalf("daily_health.Measures = %v, must not classify severity (a categorical observation) as a measure", table.Measures)
		}
	}
	foundObservation := false
	for _, observation := range table.Observations {
		if observation == "severity" {
			foundObservation = true
		}
	}
	if !foundObservation {
		t.Fatalf("daily_health.Observations = %v, want severity declared as an observation", table.Observations)
	}
	rows := fact.Fields["daily_health"].Rows
	if len(rows) != 2 {
		t.Fatalf("daily_health rows = %d, want 2", len(rows))
	}
	if got := rows[0].Fields["day"].String; got == nil || *got != "2026-02-21" {
		t.Fatalf("daily_health rows[0].day = %#v, want 2026-02-21", rows[0].Fields["day"])
	}
	if got := rows[0].Fields["compounding_risk"].Number; got == nil || *got != 0.61 {
		t.Fatalf("daily_health rows[0].compounding_risk = %#v, want 0.61", rows[0].Fields["compounding_risk"])
	}
	if got := rows[0].Fields["severity"].String; got == nil || *got != "high" {
		t.Fatalf("daily_health rows[0].severity = %#v, want high", rows[0].Fields["severity"])
	}
}

// TestHealthProviderTeamDailySeriesOmitsNullRisk pins the has/value split
// (AGENTS.md North Star check 12): a day whose compounding_risk was never
// computed must report an explicit null, never a fabricated 0.
func TestHealthProviderTeamDailySeriesOmitsNullRisk(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthScalarMatch, rows: [][]any{healthRow("team-1")}},
		{match: healthDailySeriesMatch, rows: [][]any{
			healthTeamDailySeriesRow("team-1", "2026-02-21", "unknown", uint8(0), 0),
		}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{teamSubject("team-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	rows := result.Facts[0].Fields["daily_health"].Rows
	if len(rows) != 1 {
		t.Fatalf("daily_health rows = %#v, want 1", rows)
	}
	if _, ok := rows[0].Fields["compounding_risk"]; ok {
		t.Fatalf("daily_health rows[0] = %#v, want compounding_risk omitted (never a fabricated 0)", rows[0].Fields)
	}
}

// TestHealthProviderProjectReadsDailyHealthSeries mirrors the team case for
// the project rollup (CHAOS-4645, design doc §5.2), additive alongside the
// existing risk_breakdown.
func TestHealthProviderProjectReadsDailyHealthSeries(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{
		{match: healthProjectRollupMatch, rows: [][]any{
			healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "elevated", 0.55),
		}},
		{match: healthDailySeriesMatch, rows: [][]any{
			healthProjectDailySeriesRow("linear:proj-1", "2026-02-21", uint8(1), 0.71, "high"),
		}},
		{match: healthSeverityMaxMatch, rows: [][]any{healthSeverityMaxRow("linear", "proj-1", "elevated")}},
	}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %#v, want 1", result.Facts)
	}
	fact := result.Facts[0]
	// Additive: risk_breakdown is untouched.
	if len(fact.Fields["risk_breakdown"].Rows) != 1 {
		t.Fatalf("risk_breakdown rows = %#v, want unchanged at 1", fact.Fields["risk_breakdown"].Rows)
	}
	table := fact.Fields["daily_health"].Table
	if table == nil {
		t.Fatal("daily_health field is missing")
	}
	if table.Shape != contextfabric.FactTableTimeSeries {
		t.Fatalf("daily_health.Shape = %q, want time_series", table.Shape)
	}
	if err := fact.Fields["daily_health"].Validate(); err != nil {
		t.Fatalf("daily_health fails FactValue.Validate(): %v", err)
	}
	// CHAOS-4680: severity is a per-day categorical OBSERVATION, not a
	// quantity, and must be declared as one -- a Measures column is now
	// producer-validated numeric-only (FactTable.Validate), so a
	// regression that puts severity back in Measures fails Validate()
	// above, since the cell below is a string. This assertion pins WHERE
	// it lives, not merely that validation happened to pass.
	for _, measure := range table.Measures {
		if measure == "severity" {
			t.Fatalf("daily_health.Measures = %v, must not classify severity (a categorical observation) as a measure", table.Measures)
		}
	}
	foundObservation := false
	for _, observation := range table.Observations {
		if observation == "severity" {
			foundObservation = true
		}
	}
	if !foundObservation {
		t.Fatalf("daily_health.Observations = %v, want severity declared as an observation", table.Observations)
	}
	rows := fact.Fields["daily_health"].Rows
	if len(rows) != 1 {
		t.Fatalf("daily_health rows = %d, want 1", len(rows))
	}
	if got := rows[0].Fields["compounding_risk"].Number; got == nil || *got != 0.71 {
		t.Fatalf("daily_health rows[0].compounding_risk = %#v, want 0.71 (the MAX across the project's contributing scopes that day)", rows[0].Fields["compounding_risk"])
	}
	if got := rows[0].Fields["severity"].String; got == nil || *got != "high" {
		t.Fatalf("daily_health rows[0].severity = %#v, want high (the severity of the scope that produced the max risk)", rows[0].Fields["severity"])
	}
	// CHAOS-4681: before this ticket, a project's top-level Fields carried
	// no scalar matching daily_health's sole declared Measure --
	// genkitruntime.modelFacingFacts drops daily_health itself before
	// synthesis, so a project-subject health trend could never be claimed
	// at all. The freshest day's declared Measure is now copied in under
	// its own field name.
	if fact.Fields["compounding_risk"].Number == nil || *fact.Fields["compounding_risk"].Number != 0.71 {
		t.Fatalf("compounding_risk = %#v, want a scalar sibling matching the declared measure (0.71)", fact.Fields["compounding_risk"])
	}
	// fact.Fields["severity"] is the worst-band severity across the
	// project's own risk_breakdown rows (here, the single team-1 row:
	// "elevated"), computed independently of daily_health's freshest-day
	// scope -- the two draw from different populations (every breakdown
	// row accumulated across all owning teams+repos, vs. one specific
	// day's own winning scope) and must not be conflated: the daily
	// series' freshest day reports "high" above, while the promoted
	// severity here stays "elevated".
	if got := fact.Fields["severity"].String; got == nil || *got != "elevated" {
		t.Fatalf("severity = %#v, want elevated (the risk_breakdown row's own band, independent of daily_health's freshest day)", fact.Fields["severity"])
	}
	if got := fact.Fields["severity_basis"].String; got == nil || *got != "worst_of_team_and_repo_breakdown" {
		t.Fatalf("severity_basis = %#v", fact.Fields["severity_basis"])
	}
}
