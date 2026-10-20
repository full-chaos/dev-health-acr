package devhealthfacts_test

import (
	"context"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/statementthreads"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// everyRootFixture: teams A and B both own repository "shared" (A also owns
// "own") and both own project P; A also owns project Q. Projects P and Q each
// have an issue linked to a pull request in "shared". Work item "both" is
// attributed to A and to B and is blocked by "both-blocker". Team C and
// project R reach the same subjects but are never requested: they must never
// appear as a root.
type everyRootFixture struct {
	orgID                  string
	shared, own            string
	teamA, teamB, teamC    contextfabric.SubjectRef
	projectP, projectQ     contextfabric.SubjectRef
	projectR               contextfabric.SubjectRef
	bothItem, pItem, qItem contextfabric.SubjectRef
}

func seedEveryRootFixture(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, orgID string) everyRootFixture {
	t.Helper()
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
		teamA: teamSubject("A"), teamB: teamSubject("B"), teamC: teamSubject("C"),
		projectP: projectSubject("linear", "P"), projectQ: projectSubject("linear", "Q"), projectR: projectSubject("linear", "R"),
	}
	for _, repo := range []struct{ id, slug string }{{f.shared, "acme/shared"}, {f.own, "acme/own"}} {
		exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?,?,?,?,?)`, repo.id, orgID, repo.slug, "github", at)
	}
	for _, team := range []string{"A", "B", "C"} {
		exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) VALUES (?, ?, NULL, ?, ?, ?, [], ?)`,
			team, team, at, orgID, "linear", uint8(1))
	}
	for _, own := range []struct{ team, repo, slug string }{{"A", f.shared, "acme/shared"}, {"B", f.shared, "acme/shared"}, {"C", f.shared, "acme/shared"}, {"A", f.own, "acme/own"}} {
		exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.repo, own.slug, "exact", "native", uint8(1), uint16(100), int32(0), since, nil, at)
	}
	for _, project := range []string{"P", "Q", "R"} {
		exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			project, orgID, "linear", nil, "Project "+project, uint8(1), "active", "", at)
	}
	for _, own := range []struct{ team, project string }{{"A", "P"}, {"B", "P"}, {"C", "P"}, {"A", "Q"}} {
		exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			orgID, "linear", own.team, own.project, nil, "native", since, nil, at)
	}
	for _, item := range []struct{ id, project string }{{"issue-P", "P"}, {"issue-Q", "Q"}, {"issue-R", "R"}, {"item-both", ""}, {"item-both-blocker", ""}} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			item.id, f.shared, orgID, "linear", "issue "+item.id, "open", "", "", item.project, at)
	}
	for number, issue := range map[uint32]string{1: "issue-P", 2: "issue-Q", 3: "issue-R"} {
		exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body) VALUES (?,?,?,?,?,?,?,NULL,NULL,?,?)`,
			f.shared, orgID, number, "pr", "open", at, at, "feature", "")
		exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			f.shared, issue, number, float32(1), "native", "", at, orgID)
	}
	for _, attribution := range []struct {
		team    string
		primary uint8
	}{{"A", 1}, {"B", 2}, {"C", 2}} {
		exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			orgID, f.shared, "item-both", attribution.team, "native_team", attribution.primary, "high", at)
	}
	exec(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?)`,
		"item-both-blocker", "item-both", "blocks", "Blocks", at, orgID)
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

// fillEveryRootOrganization adds rows the guarded statements must not scale
// with: work items, attributions, ownership and link rows of teams and
// projects nobody requested.
func fillEveryRootOrganization(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, f everyRootFixture, prefix string, rows uint64) {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second)
	for label, statement := range map[string]struct {
		sql  string
		args []any
	}{
		"work items": {`INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, url, parent_id, project_id, updated_at)
SELECT concat(?, '-', repeat('x', 64), '-', toString(number)), ?, ?, 'linear', 'filler', 'open', '', '', concat('FILLER-', toString(number % 500)), ? FROM numbers(?)`,
			[]any{prefix, f.shared, f.orgID, at, rows}},
		"attributions": {`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at)
SELECT ?, ?, concat(?, '-', repeat('x', 64), '-', toString(number)), concat('FILLER-', toString(number % 500)), 'native_team', 1, 'high', ? FROM numbers(?)`,
			[]any{f.orgID, f.shared, prefix, at, rows}},
		"ownership": {`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('FILLER-', toString(number % 500)), generateUUIDv4(), concat('filler/', ?, toString(number)), 'exact', 'native', 1, 100, 0, ?, NULL, ? FROM numbers(?)`,
			[]any{f.orgID, prefix, at, at, rows / 10}},
		"links": {`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT ?, concat(?, '-', repeat('x', 64), '-', toString(number)), toUInt32(number % 1000 + 10), 1, 'native', '', ?, ? FROM numbers(?)`,
			[]any{f.shared, prefix, at, f.orgID, rows}},
	} {
		if err := direct.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("%s filler: %v", label, err)
		}
	}
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
			map[string][]string{key(f.teamA): ids(f.projectP, f.projectQ), key(f.teamB): ids(f.projectP), key(f.teamC): ids(f.projectP)}},
	}
}

// everyRootStatementFragments names each expander statement by a fragment its
// text carries with or without the origin id arrays.
// devhealthschema:not-a-production-replica: these names select the guarded statements' query_log rows; they declare no table.
var everyRootStatementFragments = []struct{ label, table, fragment string }{
	{"projectRepositories", "work_items", "min(concat(p.provider, ':', p.id)), argMax(l.provenance, l.link_rank)"},
	{"projectRepositoriesAsOf", "work_items", "max(a.via_history) AS via_history"},
	{"teamRepositories", "team_repo_ownership", "argMin(source, source_rank) AS best_source"},
	{"teamWorkItems", "work_items", "min(a.team_id) AS origin_id"},
	{"projectWorkItems", "work_items", "p.id)) AS origin_id"},
	{"teamOwnedRepositoryCandidates", "team_repo_ownership", "min(own.team_id)"},
	{"projectLinkedRepositories", "work_items", "AS link_state"},
	{"projectOwningTeams", "team_project_ownership", "AS project_key, ifNull(any(t.name)"},
}

// One dataset, every changed statement: each names every requested origin
// that reached a target and no origin the request did not name; read_facts
// on the real expander serves a work item two teams reach once, naming both;
// and each statement's peak memory follows its own evidence, measured at two
// sizes of unrelated organization data (slope and ceiling).
func TestScopeExpanderKeepsEveryRequestedRootOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	rawQuery, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	query := statementthreads.API(rawQuery)
	for _, statement := range devhealthschema.DDL(append(append([]string{}, sharedClickHouseTables...), "project_membership_transitions", "work_item_dependencies", "work_item_relations_read")...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	fixture := seedEveryRootFixture(t, ctx, direct, "6a3e1f90-7b2c-4d58-9e14-3c8b5f0a2d71")
	expander := devhealthfacts.NewScopeExpander(query)
	unrequested := map[string]bool{fixture.teamC.CanonicalID: true, fixture.projectR.CanonicalID: true}

	t.Run("read_facts two teams", func(t *testing.T) {
		registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(query), contextfabric.FactRegistryOptions{ScopeExpander: expander})
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
			t.Fatalf("shared work item facts %+v, want one naming both teams; coverage %+v", shared, response.Coverage)
		}
		for _, row := range response.Coverage {
			if row.Outcome == directread.OutcomeUnavailable || strings.Contains(row.Reason, "counted under only one") {
				t.Fatalf("row %+v is not complete", row)
			}
		}
	})

	for _, tc := range fixture.cases() {
		t.Run("roots/"+tc.name, func(t *testing.T) {
			result, err := expander.ExpandFactScope(ctx, tc.request)
			if err != nil {
				t.Fatalf("ExpandFactScope: %v: %s", err, readFailureCause(err))
			}
			got := map[string][]string{}
			for key, roots := range result.TargetRoots {
				for _, root := range roots {
					if unrequested[root.CanonicalID] {
						t.Fatalf("target %s names an origin the request did not name: %s", key, root.CanonicalID)
					}
					got[key] = append(got[key], root.CanonicalID)
				}
				sort.Strings(got[key])
			}
			for key, want := range tc.want {
				if strings.Join(got[key], ",") != strings.Join(want, ",") {
					t.Fatalf("target %s roots %v, want %v (targets %+v)", key, got[key], want, result.Targets)
				}
				if first, ok := result.TargetRoot[key]; !ok || first.CanonicalID != result.TargetRoots[key][0].CanonicalID {
					t.Fatalf("target %s TargetRoot %+v, want the first listed root %+v", key, first, result.TargetRoots[key])
				}
			}
		})
	}

	fillEveryRootOrganization(t, ctx, direct, fixture, "small", 100_000)
	t.Run("memory", func(t *testing.T) {
		recorder := &recordingQueryClient{inner: query}
		recordingExpander := devhealthfacts.NewScopeExpander(recorder)
		runPeak := func(label, table, fragment, statement string, bindings []contextpacket.ClickHouseBinding) uint64 {
			mark := serverNowMicros(t, ctx, direct)
			rows, err := query.Query(ctx, statement, bindings)
			if err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			for rows.Next() {
			}
			if err := rows.Err(); err != nil {
				t.Fatalf("%s rows: %v", label, err)
			}
			_ = rows.Close()
			return peakOf(t, ctx, direct, table, fragment, mark)
		}
		// measure runs each statement as served (with the origin id arrays)
		// and the same text with the arrays removed, same bindings, same
		// organization size, interleaved three times: the minimum of each is
		// the reading, and each variant's own spread is its run-to-run noise.
		type reading struct{ with, base, withSpread, baseSpread uint64 }
		measure := func() map[string]reading {
			recorder.reset()
			for _, tc := range fixture.cases() {
				if _, err := recordingExpander.ExpandFactScope(ctx, tc.request); err != nil {
					t.Fatalf("%s: ExpandFactScope: %v: %s", tc.name, err, readFailureCause(err))
				}
			}
			readings := map[string]reading{}
			for _, statement := range everyRootStatementFragments {
				captured, ok := recorder.find(statement.fragment)
				if !ok {
					t.Fatalf("%s: no statement carrying %q was sent", statement.label, statement.fragment)
				}
				stripped := stripOriginArrays(captured.statement)
				if strings.Contains(stripped, "groupUniqArray(p.id)") || strings.Contains(stripped, "groupUniqArray(origin_team_id)") || strings.Contains(stripped, "groupUniqArray(a.team_id)") || strings.Contains(stripped, "origin_ids") {
					t.Fatalf("%s: arrays left after stripping:\n%s", statement.label, stripped)
				}
				var with, base []uint64
				for attempt := 0; attempt < 3; attempt++ {
					with = append(with, runPeak(statement.label, statement.table, statement.fragment, captured.statement, captured.bindings))
					base = append(base, runPeak(statement.label+" without arrays", statement.table, statement.fragment, stripped, captured.bindings))
				}
				readings[statement.label] = reading{
					with: slices.Min(with), base: slices.Min(base),
					withSpread: slices.Max(with) - slices.Min(with), baseSpread: slices.Max(base) - slices.Min(base),
				}
			}
			return readings
		}
		measure()
		small := measure()
		fillEveryRootOrganization(t, ctx, direct, fixture, "large", 200_000)
		large := measure()
		for _, statement := range everyRootStatementFragments {
			label := statement.label
			t.Logf("%s: small %+v, large %+v", label, small[label], large[label])
			for _, size := range []struct {
				name    string
				reading reading
			}{{"small", small[label]}, {"large", large[label]}} {
				if size.reading.base == 0 || size.reading.with == 0 {
					t.Fatalf("%s %s: measurement did not happen (%+v)", label, size.name, size.reading)
				}
				// Over the share AND beyond both the two variants' own
				// run-to-run spread and the absolute floor: a few-MB statement
				// swings past 15% on noise alone.
				if float64(size.reading.with) > arrayMemoryShare*float64(size.reading.base) && size.reading.with > size.reading.base+max(arrayMemoryFloor, size.reading.withSpread+size.reading.baseSpread) {
					t.Errorf("%s at the %s size: %d bytes with the origin arrays, %d without: over %.2fx", label, size.name, size.reading.with, size.reading.base, arrayMemoryShare)
				}
			}
			// The arrays' cost may not grow with the organization beyond the
			// slope. The larger-size cost is a difference of two minimums, so
			// its noise is the two variants' own run-to-run spreads there.
			smallDelta := int64(small[label].with) - int64(small[label].base)
			largeDelta := int64(large[label].with) - int64(large[label].base)
			noise := large[label].withSpread + large[label].baseSpread
			ratioMargin := func(r reading) int64 {
				return int64(max(arrayMemoryShare*float64(r.base), float64(r.base+max(arrayMemoryFloor, r.withSpread+r.baseSpread)))) - int64(r.with)
			}
			slopeTolerance := statementMemorySlope*float64(max(smallDelta, 0)) + float64(noise) + float64(arrayMemoryFloor)
			t.Logf("MARGIN %s small_ratio=%d large_ratio=%d slope=%d", label, ratioMargin(small[label]), ratioMargin(large[label]), int64(slopeTolerance)-largeDelta)
			if float64(largeDelta) > slopeTolerance {
				t.Errorf("%s: the origin arrays' cost grew with the organization: %d bytes at the larger size, %d at the smaller (noise %d)", label, largeDelta, smallDelta, noise)
			}
		}
	})
}

// The origin id arrays may add at most this share over the same statement
// without them; an extra cost under the floor is never counted (a few-MB
// statement's peak moves by more than that between runs).
const (
	arrayMemoryShare = 1.15
	arrayMemoryFloor = uint64(2 << 20)
)

func peakOf(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, table, fragment string, mark int64) uint64 {
	t.Helper()
	peak := uint64(0)
	for _, measured := range statementMemorySince(t, ctx, direct, statementFilter{Table: table, QueryContains: fragment}, mark) {
		peak = max(peak, measured)
	}
	return peak
}

type capturedStatement struct {
	statement string
	bindings  []contextpacket.ClickHouseBinding
}

// recordingQueryClient sends every statement to the real client and keeps
// its text and bindings, so a test can re-run a statement it did not build.
type recordingQueryClient struct {
	inner    contextpacket.ClickHouseQueryClient
	mu       sync.Mutex
	captured []capturedStatement
}

func (c *recordingQueryClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.mu.Lock()
	c.captured = append(c.captured, capturedStatement{statement: statement, bindings: append([]contextpacket.ClickHouseBinding(nil), bindings...)})
	c.mu.Unlock()
	return c.inner.Query(ctx, statement, bindings)
}

func (c *recordingQueryClient) reset() {
	c.mu.Lock()
	c.captured = nil
	c.mu.Unlock()
}

func (c *recordingQueryClient) find(fragment string) (capturedStatement, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, captured := range c.captured {
		if strings.Contains(captured.statement, fragment) {
			return captured, true
		}
	}
	return capturedStatement{}, false
}

// stripOriginArrays removes the origin id array expressions (and the
// work-item projection's masked copy) with their list comma, leaving the
// statement as it was before the arrays were added. Only an array over an
// origin column is removed: the statements carry other aggregates of the same
// shape (the work-item authorization's repository lists) that must stay.
func stripOriginArrays(statement string) string {
	isOrigin := func(expression string) bool {
		if strings.HasPrefix(expression, "if(authorized = 1, origin_ids,") {
			return true
		}
		inner := strings.TrimSuffix(strings.TrimPrefix(expression, "arraySort(groupUniqArray("), "))")
		switch inner {
		case "p.id", "a.origin_project_id", "origin_team_id", "a.team_id", "own.team_id":
			return true
		}
		return strings.HasPrefix(inner, "concat(p.provider, ") && strings.HasSuffix(inner, "p.id)")
	}
	for _, open := range []string{"arraySort(groupUniqArray(", "if(authorized = 1, origin_ids,"} {
		from := 0
		for {
			found := strings.Index(statement[from:], open)
			if found < 0 {
				break
			}
			start := from + found
			depth, end := 0, start
			for index := start; index < len(statement); index++ {
				if statement[index] == '(' {
					depth++
				} else if statement[index] == ')' {
					depth--
					if depth == 0 {
						end = index + 1
						break
					}
				}
			}
			if !isOrigin(statement[start:end]) {
				from = end
				continue
			}
			rest := strings.TrimLeft(statement[end:], " ")
			if strings.HasPrefix(rest, "AS ") {
				alias := strings.TrimPrefix(rest, "AS ")
				aliasEnd := strings.IndexAny(alias, " ,\n")
				if aliasEnd < 0 {
					aliasEnd = len(alias)
				}
				end = len(statement) - len(rest) + len("AS ") + aliasEnd
			}
			before := strings.TrimRight(statement[:start], " \n\t")
			if strings.HasSuffix(before, ",") {
				statement = strings.TrimSuffix(before, ",") + statement[end:]
				from = len(strings.TrimSuffix(before, ","))
				continue
			}
			after := strings.TrimLeft(statement[end:], " \n\t")
			if strings.HasPrefix(after, ",") {
				statement = statement[:start] + strings.TrimLeft(strings.TrimPrefix(after, ","), " ")
				from = start
				continue
			}
			statement = statement[:start] + statement[end:]
			from = start
		}
	}
	return statement
}
