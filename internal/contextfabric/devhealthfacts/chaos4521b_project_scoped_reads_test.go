package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"
	"time"

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
			if !strings.Contains(result.Reason, "team-scoped") || !strings.Contains(result.Reason, "only through team ownership") {
				t.Errorf("reason = %q, want it to name the team-scoped routing, not a generic empty read", result.Reason)
			}
			// codex P2: it must name the ROUTING and stop there. no_data is
			// equally consistent with ownership resolving teams whose metric
			// rows were absent, so claiming an ownership OUTCOME would assert
			// the stronger of two indistinguishable causes -- the same
			// failure class as CHAOS-4521 itself.
			if strings.Contains(result.Reason, "no owning team") || strings.Contains(result.Reason, "resolved no") {
				t.Errorf("reason = %q asserts an ownership outcome the read never observed", result.Reason)
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

// The `{org}:linear:CHAOS` pseudo-project after ops PR #2010, which is the
// shape that actually ships: the row is REMOVED from `projects` but is
// STILL written to `team_project_ownership` (provider=linear,
// project_id='{org}:linear:CHAOS', project_key='CHAOS', team_id='CHAOS'),
// because team_repo_ownership derivation looks it up there. GitLab rows are
// untouched.
//
// So the ownership join meets an ownership row whose project no longer
// exists. Three structural properties make that harmless, and this pins all
// three -- the fake client cannot evaluate SQL, so the statement is the
// only place they are visible.
//
// Measured against the live plane in exactly that shape (local real data,
// org 70d529e0: `projects` filtered to exclude the pseudo row, all 615
// pseudo ownership rows left in place). The identity join resolved exactly
// two subjects -- the two GitLab projects, through the key arm -- and the
// pseudo ownership row attributed to NOTHING. Separately: the number of
// real projects whose project_key equals 'CHAOS' or '{org}:linear:CHAOS'
// is ZERO, so the key arm cannot route the pseudo row onto a real project.
func TestChaos4521b_ThePseudoProjectOwnershipRowAttributesToNothing(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM investment_metrics_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
	if _, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	}); err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	statement := client.queries[0].statement

	// 1. EVERY arm is an INNER JOIN against `projects`. An ownership row
	//    whose project row was deleted therefore produces no output at all,
	//    through any arm -- which is what makes the pseudo-project's
	//    surviving ownership rows inert once CHAOS-4530's cleanup deletes
	//    the `projects` row, with no special case in acr.
	//
	//    Deliberately NOT an id-shape predicate (team-lead ruling): the
	//    `{org}:linear:<teamKey>` format is producer-owned, and guessing at
	//    it here would couple acr to a shape it does not define. The
	//    property that actually holds -- no projects row, no attribution --
	//    is the one worth pinning, and it needs no knowledge of the format.
	//
	//    Counting arms rather than merely finding one join: a future arm
	//    added as a LEFT JOIN, or one reading a different table, would
	//    reintroduce the hazard silently.
	if projectsReads := strings.Count(statement, "FROM projects FINAL"); projectsReads != strings.Count(statement, "UNION ALL")+1 {
		t.Errorf("not every arm resolves through `projects`: %d reads for %d arms\n%s", projectsReads, strings.Count(statement, "UNION ALL")+1, statement)
	}
	if strings.Contains(statement, "LEFT JOIN (\n\tSELECT provider") {
		t.Errorf("an ownership arm is a LEFT JOIN; a missing projects row would no longer drop it\n%s", statement)
	}
	// 2. Subject resolution must NOT pre-filter on a non-empty project_key.
	//    That filter is what dropped every real Linear project before the
	//    join could run (CHAOS-4530).
	if strings.Contains(statement, "WHERE project_key != '' AND key_resolution_count = 1 AND concat(provider") {
		t.Errorf("subject resolution still drops projects with a NULL project_key\n%s", statement)
	}
	// 3. The key arm stays guarded on BOTH a non-empty key and an
	//    unambiguous one. Without the first, a NULL-key project ('' after
	//    the coalesce) could match a stray empty identity; without the
	//    second, one project's ownership could be attributed to another.
	if !strings.Contains(statement, "p.project_key != ''") || !strings.Contains(statement, "p.key_resolution_count = 1") {
		t.Errorf("the ownership key arm lost a guard\n%s", statement)
	}
}

// codex P2, the other half: a HISTORICAL empty read already carries a more
// specific reason than the routing one -- outOfRetentionReason, which is a
// statement about TIME rather than routing. Overwriting it would erase the
// distinction §19.8.3 exists to draw and replace a true reason with a less
// true one.
func TestChaos4521b_TheRoutingReasonNeverOverwritesTheRetentionReason(t *testing.T) {
	t.Parallel()
	client := &fakeClient{tables: []fakeTable{{match: "FROM compounding_risk_daily", rows: nil}}}
	provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactHealth)
	asOf := time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &asOf},
		Kind: contextfabric.FactHealth, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	if result.State != contextfabric.SourceNoData {
		t.Fatalf("state = %q, want %q", result.State, contextfabric.SourceNoData)
	}
	if !strings.Contains(result.Reason, "predate the retained corpus") {
		t.Errorf("reason = %q, want the retention reason preserved on a historical read", result.Reason)
	}
	if strings.Contains(result.Reason, "team-scoped") {
		t.Errorf("reason = %q: the routing reason overwrote a more specific temporal one", result.Reason)
	}
}
