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

func healthRow(scopeID string) []any {
	return []any{scopeID, "elevated", uint8(1), float64(0.62), "2026-02-21 00:00:00"}
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
	if !strings.Contains(client.queries[len(client.queries)-1].statement, "scope = 'repo'") {
		t.Fatalf("statement = %q, want scope='repo'", client.queries[len(client.queries)-1].statement)
	}
}

func TestHealthProviderTeamScopeHappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: [][]any{healthRow("CHAOS")}}}}
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
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
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
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: [][]any{
		healthProjectRollupRow("linear", "proj-1", "team", "team-1", "Team One", "elevated", 0.55),
		healthProjectRollupRow("linear", "proj-1", "repo", "repo-1", "full.chaos/svc", "high", 0.81),
	}}}}
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

func TestHealthProviderProjectRollupNoOwningTeamsHasNoFactEntry(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM team_project_ownership", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-404")},
	})
	if err != nil {
		t.Fatalf("ReadFacts() error = %v", err)
	}
	if len(result.Facts) != 0 || result.State != contextfabric.SourceAvailable {
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
