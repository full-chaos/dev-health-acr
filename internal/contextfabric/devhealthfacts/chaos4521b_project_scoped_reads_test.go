package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-4521b. A project's flow was read through team_project_ownership,
// and that was wrong twice over -- both observed on live data (org
// 70d529e0, 2026-08-29):
//
//  1. It reached no real project. The join keys on projects.project_key,
//     which is NULL for every real Linear project (CHAOS-4530).
//  2. When it DID resolve, it aggregated work_item_metrics_daily by
//     team_id across every work scope that team touched -- so a project's
//     flow was assembled from other projects' rows.
//
// Defect 2 is invisible to a row assertion: the fake returns whatever it is
// handed regardless of the SQL. Only the statement shows an aggregation
// keyed on the team rather than on the project.
func TestChaos4521b_ProjectFlowKeysOnTheProjectsOwnWorkScope(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM work_item_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactFlow)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactFlow, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	}); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if len(client.queries) != 1 {
		t.Fatalf("queries = %d, want exactly one", len(client.queries))
	}
	statement := client.queries[0].statement

	if strings.Contains(statement, "team_project_ownership") {
		t.Errorf("project flow still joins team_project_ownership; work_item_metrics_daily carries the project id\n%s", statement)
	}
	if !strings.Contains(statement, "work_scope_id = p.id") || !strings.Contains(statement, "work_scope_id = p.project_key") {
		t.Errorf("project flow does not match work_scope_id against the project identity (id or key)\n%s", statement)
	}
	// The aggregation must be per (project, team), not per team alone --
	// defect 2. Grouping by team only is what pulled in other projects.
	if !strings.Contains(statement, "GROUP BY p.provider, p.id, wm.team_id") {
		t.Errorf("project flow does not aggregate per project and team\n%s", statement)
	}
	if !strings.Contains(statement, "org_id = {org_id:String}") {
		t.Errorf("project flow lost its org scoping\n%s", statement)
	}
	// The project_key arm must be guarded on a NON-EMPTY key. Every real
	// Linear project carries project_key NULL, which the resolution
	// coalesces to ''; without this guard such a project would match any
	// daily row whose work_scope_id is also '' -- and, more to the point,
	// the guard is what keeps a NULL-key project from colliding with the
	// `{org}:linear:CHAOS` pseudo-project row that CHAOS-4530 may or may
	// not remove. This assertion makes the change correct either way.
	if !strings.Contains(statement, "p.project_key != ''") {
		t.Errorf("the project_key arm is not guarded on a non-empty key\n%s", statement)
	}
	// The ambiguity guard travels with it: a key resolving to two projects
	// must not attribute one project's rows to the other.
	if !strings.Contains(statement, "p.key_resolution_count = 1") {
		t.Errorf("the project_key arm dropped its ambiguity guard\n%s", statement)
	}
}

// The three rollups whose source tables carry no project dimension keep the
// ownership hop -- removing it would be a silent capability loss, not a fix
// -- and say WHY they are empty in terms a reader can act on. PR-A's
// generic "the source was reached and held no rows" is true but reads as
// "this project has no health", when what happened is that the question
// could not be routed to the project at all.
func TestChaos4521b_TeamScopedRollupsExplainAnEmptyProjectRead(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		kind        contextfabric.FactKind
		sourceTable string
	}{
		{"health", contextfabric.FactHealth, "FROM compounding_risk_daily"},
		{"investment", contextfabric.FactInvestment, "FROM investment_metrics_daily"},
		{"landscape", contextfabric.FactLandscape, "FROM ic_landscape_rolling_30d"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: testCase.sourceTable, rows: nil}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), testCase.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: testCase.kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if result.State != contextfabric.SourceNoData {
				t.Fatalf("state = %q, want %q", result.State, contextfabric.SourceNoData)
			}
			if !strings.Contains(result.Reason, "team-scoped") || !strings.Contains(result.Reason, "no owning team") {
				t.Errorf("reason = %q, want it to name the team-scoped routing, not a generic empty read", result.Reason)
			}
			if !strings.Contains(client.queries[0].statement, "team_project_ownership") {
				t.Errorf("%s has no project dimension; it must KEEP the ownership hop", testCase.name)
			}
		})
	}
}

// A read whose subjects are not all projects must NOT be relabelled with an
// ownership explanation that does not apply to it. This is the guard that
// stops the new reason spreading into every empty read.
func TestChaos4521b_TheOwnershipExplanationDoesNotLeakIntoOtherSubjectKinds(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactHealth,
		Subjects: []contextfabric.SubjectRef{
			projectSubject("linear", "proj-1"),
			repoSubject("repo-1"),
		},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if strings.Contains(result.Reason, "team-scoped") {
		t.Errorf("reason = %q: a mixed read whose repository half legitimately held no rows must not be given an ownership explanation", result.Reason)
	}
}
