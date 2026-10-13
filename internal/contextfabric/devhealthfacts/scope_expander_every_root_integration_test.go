package devhealthfacts_test

import (
	"context"
	"sort"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// everyRootFixture: teams A and B both own repository "shared" (A also owns
// "own") and both own project P; A also owns project Q. Projects P and Q each
// have an issue linked to a pull request in "shared". Work item "both" is
// attributed to A and to B. Every statement that picks one origin per target
// must also name every other requested origin that reached it.
type everyRootFixture struct {
	orgID                  string
	shared, own            string
	teamA, teamB           contextfabric.SubjectRef
	projectP, projectQ     contextfabric.SubjectRef
	bothItem, pItem, qItem contextfabric.SubjectRef
}

func seedEveryRootFixture(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) everyRootFixture {
	t.Helper()
	for _, statement := range devhealthschema.DDL("project_membership_transitions") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	since := at.Add(-30 * 24 * time.Hour)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	f := everyRootFixture{
		orgID: orgID, shared: repoUUID(orgID + "-shared"), own: repoUUID(orgID + "-own"),
		teamA: teamSubject("A"), teamB: teamSubject("B"),
		projectP: projectSubject("linear", "P"), projectQ: projectSubject("linear", "Q"),
	}
	for _, repo := range []struct{ id, slug string }{{f.shared, "acme/shared"}, {f.own, "acme/own"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo.id, orgID, repo.slug, "github", at)
	}
	for _, team := range []string{"A", "B"} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
	}
	for _, own := range []struct{ team, repo, slug string }{{"A", f.shared, "acme/shared"}, {"B", f.shared, "acme/shared"}, {"A", f.own, "acme/own"}} {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.repo, own.slug, "exact", "native", uint8(1), uint16(100), int32(0), since, nil, at)
	}
	for _, project := range []string{"P", "Q"} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, "Project "+project, uint8(1), "active", "", at)
	}
	for _, own := range []struct{ team, project string }{{"A", "P"}, {"B", "P"}, {"A", "Q"}} {
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.project, nil, "native", since, nil, at)
	}
	for _, item := range []struct{ id, project string }{{"issue-P", "P"}, {"issue-Q", "Q"}, {"item-both", ""}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, f.shared, orgID, "linear", "issue "+item.id, "open", "", "", item.project, at)
	}
	for number, issue := range map[uint32]string{1: "issue-P", 2: "issue-Q"} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			f.shared, orgID, number, "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			f.shared, issue, number, float32(1), "native", "", at, orgID)
	}
	for _, attribution := range []struct {
		team    string
		primary uint8
	}{{"A", 1}, {"B", 2}} {
		exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, f.shared, "item-both", attribution.team, "native_team", attribution.primary, "high", at)
	}
	workItem := func(id string) contextfabric.SubjectRef {
		canonical, _, err := identity.Derive(identity.KindWorkItem, []string{f.shared, id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonical}
	}
	f.bothItem, f.pItem, f.qItem = workItem("item-both"), workItem("issue-P"), workItem("issue-Q")
	return f
}

type everyRootCase struct {
	name    string
	request contextfabric.FactScopeExpansionRequest
	want    map[string][]string
}

func (f everyRootFixture) cases() []everyRootCase {
	repository := func(id string) string {
		return contextfabric.FactSubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + id})
	}
	key := contextfabric.FactSubjectKey
	ids := func(refs ...contextfabric.SubjectRef) []string {
		out := make([]string, 0, len(refs))
		for _, ref := range refs {
			out = append(out, ref.CanonicalID)
		}
		sort.Strings(out)
		return out
	}
	now := time.Now().UTC()
	request := func(policy contextfabric.FactScopePolicy, kind contextfabric.FactKind, target contextfabric.SubjectKind, axis contextfabric.TimeContext, origins ...contextfabric.SubjectRef) contextfabric.FactScopeExpansionRequest {
		return contextfabric.FactScopeExpansionRequest{
			Principal: storage.Principal{OrgID: f.orgID}, RequirementKind: kind, Origins: origins,
			Policy: policy, TargetKind: target, TimeContext: axis, Limit: 200,
		}
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}
	validTime := contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &now}
	return []everyRootCase{
		{"project repositories", request(contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, current, f.projectP, f.projectQ),
			map[string][]string{repository(f.shared): ids(f.projectP, f.projectQ)}},
		{"project repositories as of", request(contextfabric.FactScopePolicyProjectWorkItemRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, validTime, f.projectP, f.projectQ),
			map[string][]string{repository(f.shared): ids(f.projectP, f.projectQ)}},
		{"team repositories", request(contextfabric.FactScopePolicyTeamPrimaryAttributionRepository, contextfabric.FactMetrics, contextfabric.SubjectRepository, current, f.teamA, f.teamB),
			map[string][]string{repository(f.shared): ids(f.teamA, f.teamB), repository(f.own): ids(f.teamA)}},
		{"team work items", request(contextfabric.FactScopePolicyTeamPrimaryAttributionWorkItemStatus, contextfabric.FactStatus, contextfabric.SubjectWorkItem, current, f.teamA, f.teamB),
			map[string][]string{key(f.bothItem): ids(f.teamA, f.teamB)}},
		{"project work items", request(contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus, contextfabric.SubjectWorkItem, current, f.projectP, f.projectQ),
			map[string][]string{key(f.pItem): ids(f.projectP), key(f.qItem): ids(f.projectQ)}},
		{"team owned repositories", request(contextfabric.FactScopePolicyTeamRepositoryOwnershipContinuousIntegration, contextfabric.FactContinuousIntegration, contextfabric.SubjectRepository, current, f.teamA, f.teamB),
			map[string][]string{repository(f.shared): ids(f.teamA, f.teamB), repository(f.own): ids(f.teamA)}},
		{"project linked repositories", request(contextfabric.FactScopePolicyProjectIssuePullRequestDeployments, contextfabric.FactDeployments, contextfabric.SubjectRepository, current, f.projectP, f.projectQ),
			map[string][]string{repository(f.shared): ids(f.projectP, f.projectQ)}},
		{"project owning teams", request(contextfabric.FactScopePolicyProjectTeamOwnershipOperationalDeficiencies, contextfabric.FactOperationalDeficiencies, contextfabric.SubjectTeam, current, f.projectP, f.projectQ),
			map[string][]string{key(f.teamA): ids(f.projectP, f.projectQ), key(f.teamB): ids(f.projectP)}},
	}
}

// Each statement that picks one origin per target with min(origin) also
// names every requested origin that reached the target.
func TestScopeExpanderNamesEveryRequestedRootOfASharedTarget(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	fixture := seedEveryRootFixture(t, ctx, direct, sharedTestOrgID(t))
	expander := devhealthfacts.NewScopeExpander(query)
	for _, tc := range fixture.cases() {
		t.Run(tc.name, func(t *testing.T) {
			result, err := expander.ExpandFactScope(ctx, tc.request)
			if err != nil {
				t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
			}
			got := map[string][]string{}
			for key, roots := range result.TargetRoots {
				for _, root := range roots {
					got[key] = append(got[key], root.CanonicalID)
				}
				sort.Strings(got[key])
			}
			for key, want := range tc.want {
				if len(got[key]) != len(want) {
					t.Fatalf("target %s roots %v, want %v (targets %+v)", key, got[key], want, result.Targets)
				}
				for index := range want {
					if got[key][index] != want[index] {
						t.Fatalf("target %s roots %v, want %v", key, got[key], want)
					}
				}
				if first, ok := result.TargetRoot[key]; !ok || first.CanonicalID != result.TargetRoots[key][0].CanonicalID {
					t.Fatalf("target %s TargetRoot %+v, want the first listed root %+v", key, first, result.TargetRoots[key])
				}
			}
		})
	}
}

// Each changed statement's peak memory follows its own evidence: measured
// with the same requested origins at two sizes of unrelated organization data,
// it may not grow past the slope, and stays under the ceiling.
func TestScopeExpanderEveryRootStatementsKeepTheirMemoryBudget(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(sharedClickHouseTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	const orgID = "6a3e1f90-7b2c-4d58-9e14-3c8b5f0a2d71"
	fixture := seedEveryRootFixture(t, ctx, direct, orgID)
	at := time.Now().UTC().Truncate(time.Second)
	filler := func(prefix string, rows uint64) {
		t.Helper()
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)
SELECT concat(?, '-', repeat('x', 64), '-', toString(number)), ?, ?, 'linear', 'filler', 'open', '', '', concat('FILLER-', toString(number % 500)), ? FROM numbers(?)`,
			prefix, fixture.shared, orgID, at, rows); err != nil {
			t.Fatalf("work item filler: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, concat(?, '-', repeat('x', 64), '-', toString(number)), concat('FILLER-', toString(number % 500)), 'native_team', 1, 'high', ? FROM numbers(?)`,
			orgID, fixture.shared, prefix, at, rows); err != nil {
			t.Fatalf("attribution filler: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('FILLER-', toString(number % 500)), generateUUIDv4(), concat('filler/', ?, toString(number)), 'exact', 'native', 1, 100, 0, ?, NULL, ? FROM numbers(?)`,
			orgID, prefix, at, at, rows/10); err != nil {
			t.Fatalf("ownership filler: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat(?, '-', repeat('x', 64), '-', toString(number)), toUInt32(number % 1000 + 10), 1, 'native', '', ?, ? FROM numbers(?)`,
			fixture.shared, prefix, at, orgID, rows); err != nil {
			t.Fatalf("link filler: %v", err)
		}
	}
	expander := devhealthfacts.NewScopeExpander(query)
	read := func() statementMemory {
		mark := serverNowMicros(t, ctx, direct)
		for _, tc := range fixture.cases() {
			if _, err := expander.ExpandFactScope(ctx, tc.request); err != nil {
				t.Fatalf("%s: ExpandFactScope: %v: %s", tc.name, err, readFailureCause(err))
			}
		}
		measured := statementMemory{}
		for _, table := range []string{"work_items", "team_repo_ownership", "team_project_ownership"} {
			for hash, peak := range statementMemorySince(t, ctx, direct, statementFilter{Table: table, QueryContains: "arraySort(groupUniqArray("}, mark) {
				measured[hash] = peak
			}
		}
		return measured
	}
	filler("small", 100_000)
	read()
	small := read()
	filler("large", 200_000)
	large := read()
	assertStatementMemoryFollowsEvidence(t, ctx, direct, "every-root expansion statements", small, large, len(fixture.cases()))
}

// read_facts for two teams' blockers on the real expander: a work item both
// teams reach is served once, names both teams, and both rows are complete.
func TestDirectReadCountsASharedWorkItemForBothTeamsOnTheRealExpander(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)
	fixture := seedEveryRootFixture(t, ctx, direct, sharedTestOrgID(t))
	for _, statement := range devhealthschema.DDL("work_item_dependencies") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"item-both-blocker", fixture.shared, fixture.orgID, "linear", "blocker", "open", "", "", "", at); err != nil {
		t.Fatal(err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"item-both-blocker", "item-both", "blocks", "Blocks", at, fixture.orgID); err != nil {
		t.Fatal(err)
	}
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: devhealthfacts.NewScopeExpander(query)})
	if err != nil {
		t.Fatal(err)
	}
	reader := directread.NewFactsReader(directread.NewSubjectGate(grantedRepositoryGraph{reached: "acme/shared"}, nil), directread.NewFactReader(registry.DirectReadFactSource()), nil)
	response, err := reader.Read(observability.WithRequestID(ctx, "req_0000000000000000000000000000b07e"), storage.Principal{OrgID: fixture.orgID, Subject: "every-root", CredentialID: "every-root"}, directread.FactsRequest{
		Kinds:    []string{"blockers"},
		Subjects: []directread.RequestSubject{{Kind: "team", CanonicalID: fixture.teamA.CanonicalID}, {Kind: "team", CanonicalID: fixture.teamB.CanonicalID}},
	})
	if err != nil {
		t.Fatalf("read_facts: %v", err)
	}
	var shared []directread.ServedFact
	for _, fact := range response.Facts {
		if fact.Subject.Kind == string(contextfabric.SubjectWorkItem) && fact.Subject.CanonicalID == fixture.bothItem.CanonicalID {
			shared = append(shared, fact)
		}
	}
	if len(shared) != 1 || len(shared[0].ReachedFrom) != 2 {
		t.Fatalf("shared work item facts %+v, want one naming both teams", shared)
	}
	for _, row := range response.Coverage {
		if row.Kind == "blockers" && row.Outcome == directread.OutcomeUnavailable {
			t.Fatalf("row %+v reads unavailable", row)
		}
	}
}
