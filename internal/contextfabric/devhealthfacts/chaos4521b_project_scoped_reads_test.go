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
	// CHAOS-4645: readProjectFlow now issues a SECOND query
	// (queryProjectFlowDailySeries, for the additive daily_flow time_series)
	// after the original -- queries[0] is still the original query this
	// test's assertions are about, since call order is preserved.
	if len(client.queries) != 2 {
		t.Fatalf("queries = %d, want exactly two (original + CHAOS-4645 daily series)", len(client.queries))
	}
	statement := client.queries[0].statement

	if strings.Contains(statement, "team_project_ownership") {
		t.Errorf("project flow still joins team_project_ownership; work_item_metrics_daily carries the project id\n%s", statement)
	}
	// CHAOS-4521b re-plan: the identity alternatives moved OUT of the ON
	// clause and INTO rows, because ClickHouse 24.8 -- the version acr's
	// own fixtures pin -- rejects a JOIN ON containing OR (Code: 403). The
	// join is a plain equality against p.scope; the two identity values are
	// the two unioned scope rows.
	if !strings.Contains(statement, "work_scope_id = p.scope") {
		t.Errorf("project flow does not match work_scope_id against the resolved identity scope\n%s", statement)
	}
	// RESPELLED for dev-health-go v0.6.3 (CHAOS-4751), not relaxed. The two
	// scope rows used to be two UNION ALL branches ("id AS scope",
	// "project_key AS scope"); the expansion now reads its row source once
	// and fans them out with ARRAY JOIN, so the pair of substrings becomes
	// one literal naming both. It is STRONGER: it also pins that the id
	// value is unconditional while the key value appears only in the
	// guarded arm, which two independent Contains checks could not tell
	// apart.
	if !strings.Contains(statement, "if(key_scope_emitted, [id, project_key], [id]) AS scope") {
		t.Errorf("the identity resolution does not expand BOTH the canonical id and the project key into scope rows\n%s", statement)
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
	if !strings.Contains(statement, "project_key != ''") {
		t.Errorf("the project_key scope row is not guarded on a non-empty key\n%s", statement)
	}
	// The ambiguity guard travels with it: a key resolving to two projects
	// must not attribute one project's rows to the other.
	if !strings.Contains(statement, "key_resolution_count = 1") {
		t.Errorf("the project_key scope row dropped its ambiguity guard\n%s", statement)
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
	//    Counting the reads rather than merely finding one join: a future
	//    arm added as a LEFT JOIN, or one reading a different table, would
	//    reintroduce the hazard silently.
	//
	//    The count is pinned as a literal, not derived from UNION ALL
	//    separators (as it was before CHAOS-4552). That derivation assumed
	//    each top-level arm was its own UNION ALL branch, each embedding
	//    its own copy of the identity expansion -- true of the OLD
	//    two-arm shape, not the new one, where the arms union on the
	//    OWNERSHIP side instead and there is only ONE top-level join
	//    against `projects`.
	//
	//    ONE is dev-health-go's own pinned count for
	//    readers.ProjectOwnershipJoinSQL's rendered SQL alone
	//    (readers/ownership_test.go's TestChaos4552_OwnershipJoinScansProjectsOnce,
	//    plus TestChaos4751_IdentityExpansionScansProjectsOnce for the
	//    expansion's own half); nothing in ReadProjectInvestment's own
	//    wrapping adds another. It was TWO until dev-health-go v0.6.3
	//    (CHAOS-4751): the identity expansion used to spell its row source
	//    twice, once per UNION ALL branch of its own id-row/key-row
	//    expansion, and now reads it once and fans the scope rows out with
	//    ARRAY JOIN. That change was proven byte-identical against the
	//    v0.6.2 rendering on real ClickHouse 24.8 and 26.7, so this literal
	//    moves with the upstream shape and nothing about what the statement
	//    MEANS changed here.
	if projectsReads := strings.Count(statement, "FROM projects FINAL"); projectsReads != 1 {
		t.Errorf("not every read resolves through `projects` the expected number of times: got %d, want 1\n%s", projectsReads, statement)
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
	if !strings.Contains(statement, "project_key != ''") || !strings.Contains(statement, "key_resolution_count = 1") {
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

// CHAOS-4521b, codex on #331. `estimate_coverage_metrics_daily.team_id`
// and `capacity_forecasts.team_id` are Nullable. Before the project reads
// keyed on work_scope_id, the team came from the ownership join where it
// could not be NULL, so the case was unreachable; reading the daily row
// directly makes it reachable.
//
// An unattributed row must stay a row -- that coverage IS the project's,
// and dropping it would lose real measurements -- while not being counted
// or cited as a team. Three surfaces have to agree, and this pins all
// three, because getting two right and one wrong is the likely failure:
//
//   - team_count excludes it;
//   - no `acr:v1:team:` evidence ref is minted for it;
//   - the row's own team_id renders NULL, not "".
func TestChaos4521b_AnUnattributedRowIsKeptButNotCountedAsATeam(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		kind        contextfabric.FactKind
		sourceTable string
		rows        [][]any
	}{
		{
			// CHAOS-4645: narrowed from the broad "FROM
			// estimate_coverage_metrics_daily" to readinessOriginalQueryMatch
			// (readiness_test.go) -- readProjectReadiness now also issues a
			// SECOND, differently-shaped query (queryProjectReadinessDailySeries)
			// against the same table, and a broad match here would feed this
			// case's 12-column fixture into that query's fewer-column scan
			// targets, panicking on a type assertion inside fakeScanner.Scan.
			name:        "readiness",
			kind:        contextfabric.FactReadiness,
			sourceTable: readinessOriginalQueryMatch,
			rows: [][]any{
				{"linear:proj-1", uint8(0), "", "", "scope-a", "linear", "2026-02-22", int64(3), int64(1), int64(4), uint8(1), float64(0.75)},
				{"linear:proj-1", uint8(1), "team-1", "Team One", "scope-b", "linear", "2026-02-22", int64(18), int64(2), int64(20), uint8(1), float64(0.9)},
			},
		},
		{
			// CHAOS-4645: same fix, workloadBaseQueryMatch (workload_test.go)
			// -- readProjectWorkload also gained queryProjectWorkloadDailySeries
			// against capacity_forecasts.
			name:        "workload",
			kind:        contextfabric.FactWorkload,
			sourceTable: workloadBaseQueryMatch,
			rows: [][]any{
				{"linear:proj-1", uint8(0), "", "", "scope-a", float64(1.0), float64(0.1), uint8(0), int64(0), uint8(0), uint8(0), int64(4), "2026-07-27 04:00:00"},
				{"linear:proj-1", uint8(1), "team-1", "Team One", "scope-b", float64(3.2), float64(0.8), uint8(0), int64(0), uint8(0), uint8(1), int64(120), "2026-07-27 04:00:00"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := &fakeClient{tables: []fakeTable{{match: testCase.sourceTable, rows: testCase.rows}}}
			provider := findProvider(t, devhealthfacts.NewProviders(client), testCase.kind)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
				Kind: testCase.kind, Subjects: []contextfabric.SubjectRef{projectSubject("linear", "proj-1")},
			})
			if err != nil {
				t.Fatalf("ReadFacts: %v", err)
			}
			if len(result.Facts) != 1 {
				t.Fatalf("facts = %d, want 1", len(result.Facts))
			}
			fact := result.Facts[0]

			// team_count counts TEAMS. One row is attributed; the other is
			// not a team at all.
			if count := fact.Fields["team_count"].Integer; count == nil || *count != 1 {
				t.Errorf("team_count = %v, want 1 -- an unattributed row is not a team", fact.Fields["team_count"])
			}
			// Both rows survive: the unattributed one carries real coverage.
			rows := fact.Fields["team_breakdown"].Rows
			if len(rows) != 2 {
				t.Fatalf("team_breakdown = %d rows, want 2 -- an unattributed row must not be dropped", len(rows))
			}
			// ...and it renders team_id NULL rather than "".
			var sawUnattributed bool
			for _, row := range rows {
				teamID := row.Fields["team_id"]
				if teamID.Null {
					sawUnattributed = true
					continue
				}
				if teamID.String == nil || *teamID.String == "" {
					t.Errorf("a row rendered team_id as an empty string; unattributed must be NULL, not a team whose id is blank")
				}
			}
			if !sawUnattributed {
				t.Errorf("no row rendered team_id as NULL; the unattributed row was reported as a team")
			}
			// No malformed `acr:v1:team:` citation for the missing team.
			for _, ref := range fact.EvidenceRefIDs {
				if strings.HasSuffix(ref, ":team:") || strings.HasSuffix(ref, "team:") && strings.Count(ref, ":") >= 3 && strings.HasSuffix(ref, ":") {
					t.Errorf("evidence ref %q cites a team with an empty id", ref)
				}
			}
		})
	}
}
