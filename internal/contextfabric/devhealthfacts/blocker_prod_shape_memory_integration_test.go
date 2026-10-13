package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

const blockerProdShapeOrg = "7d3a9c58-1e46-4f02-b8a7-5c20e9d14b63"

// The production organization the team blocker statements peak on is small in
// work items and edges and large in append-only ownership history. This seeds
// that shape (row counts read from production on 2026-10-13: 2,103 blocks
// edges over 1,602 endpoints, 17k work items, 24k link rows, 39 teams of which
// 6 own repositories, 67k team_project_ownership versions over 88 pairs, 44k
// team_repo_ownership versions over 24 pairs) so the statements' memory is
// measured on the data that makes them peak.
func seedProdShapedBlockerOrganization(t *testing.T, ctx context.Context, direct interface {
	Exec(context.Context, string, ...any) error
}) {
	t.Helper()
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	org := blockerProdShapeOrg
	at := ts(2026, 9, 29, 0, 0, 0)
	repoUUIDExpr := func(expr string) string {
		return fmt.Sprintf("toUUID(concat('00000000-0000-4000-8000-0000000000', lpad(toString(%s), 2, '0')))", expr)
	}
	exec(`INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT `+repoUUIDExpr("number")+`, ?, concat('acme/repo-', toString(number)), 'github', ? FROM numbers(11)`, org, at)
	exec(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) SELECT concat('team-', toString(number)), concat('team-', toString(number)), NULL, ?, ?, 'linear', [], 1 FROM numbers(39)`, at, org)
	exec(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 24) % 6)), `+repoUUIDExpr("(number % 24) % 11")+`, concat('acme/repo-', toString((number % 24) % 11)), 'exact', 'native', 1, 100, 0,
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)),
  if(intDiv(number, 24) = 1847 AND number % 24 < 18, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24))
FROM numbers(44352)`, org)
	exec(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) SELECT concat('proj-', toString(number % 33)), ?, 'linear', NULL, concat('proj-', toString(number % 33)), 1, 'active', '', ? + toIntervalMinute(number) FROM numbers(47)`, org, at)
	exec(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 88) % 36)), concat('proj-', toString((number % 88) % 48)), NULL, 'native',
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)),
  if(intDiv(number, 88) = 766 AND number % 88 < 60, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88))
FROM numbers(67496)`, org)
	exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number), 12, '0')), `+repoUUIDExpr("number % 11")+`, ?, 'title', 'issue', if(number % 5 = 0, 'done', 'open'), '', ?, ?, '', 'linear', concat('proj-', toString(number % 33)), NULL, ? FROM numbers(17000)`, org, at, at, at)
	exec(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT `+repoUUIDExpr("number % 11")+`, ?, toUInt32(intDiv(number, 11) + 1), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(7755)`, org, at, at)
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT `+repoUUIDExpr("(number * 7) % 11")+`, concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 14017), 12, '0')), toUInt32((number * 31) % 705 + 1), 1, multiIf(number % 3 = 0, 'native', number % 3 = 1, 'explicit_text', 'heuristic'), '', ?, ? FROM numbers(23905)`, at, org)
	exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 1602), 12, '0')), concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString((number * 37 + 5 + intDiv(number, 1602)) % 1602), 12, '0')), 'blocks', 'blocks', ?, ? - toIntervalDay(number % 90) FROM numbers(2103)`, org, at, at)
	exec(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 17000), 12, '0')), concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString((number * 11 + 3) % 17000), 12, '0')), 'relates_to', 'relates', ?, NULL FROM numbers(21142)`, org, at)
	exec(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)
SELECT ?, `+repoUUIDExpr("number % 11")+`, concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 17000), 12, '0')), concat('team-', toString(number % 39)), NULL, 'project_ownership', 1, 'medium', ? + toIntervalMinute(intDiv(number, 17000)) FROM numbers(67872)`, org, at)
	exec(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id)
SELECT ?, NULL, `+repoUUIDExpr("number % 11")+`, 'work_item', concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 17000), 12, '0')), 'linear', concat('proj-', toString(number % 33)), concat('proj-', toString((number + 1) % 33)), '', '', '', ? + toIntervalMinute(number), ? + toIntervalMinute(number), concat('event-', toString(number)) FROM numbers(9075)`, org, at, at)
	exec(`INSERT INTO work_item_dependency_first_seen (org_id, source_work_item_id, target_work_item_id, relationship_type, first_seen_at)
SELECT ?, concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(number % 1602), 12, '0')), concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString((number * 37 + 5 + intDiv(number, 1602)) % 1602), 12, '0')), 'blocks', ? - toIntervalDay(number % 120) FROM numbers(2103)`, org, at)
}

// The production statements peaked at 61.4 MiB (the age statement) and 53.7 MiB
// of the 64 MiB cap, while this shape reads 86% and 80% of that. The budget for
// the statement CI measures is therefore 48 MiB, the production target, scaled
// by the 0.86 calibration ratio.
const (
	blockerProdShapeCeiling       = uint64(41 << 20)
	blockerProdShapeTotalsCeiling = uint64(38 << 20)
	blockerProdShapeTotalsHead    = "SELECT team_id, toInt64(uniqExactIf(tgt, is_open))"
)

type blockerProdShapeStatement struct {
	hash, peak, readRows uint64
	head                 string
}

func blockerProdShapeStatements(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, label string, mark int64) []blockerProdShapeStatement {
	t.Helper()
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	rows, err := direct.Query(ctx, `SELECT normalized_query_hash, max(memory_usage), max(read_rows), any(substring(replaceRegexpAll(query, '\\s+', ' '), 1, 110))
FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select'
  AND has(tables, concat(currentDatabase(), '.work_item_dependencies'))
GROUP BY normalized_query_hash ORDER BY max(memory_usage) DESC`, mark)
	if err != nil {
		t.Fatalf("read query_log: %v", err)
	}
	defer rows.Close()
	var out []blockerProdShapeStatement
	for rows.Next() {
		var st blockerProdShapeStatement
		if err := rows.Scan(&st.hash, &st.peak, &st.readRows, &st.head); err != nil {
			t.Fatal(err)
		}
		out = append(out, st)
		t.Logf("PRODSHAPE[%s] statement %d peak %d bytes (%.1f MiB) read_rows %d: %s", label, st.hash, st.peak, float64(st.peak)/(1<<20), st.readRows, st.head)
	}
	if len(out) < 4 {
		t.Fatalf("%s: measured %d statements, want the team blocker statements: the measurement did not happen", label, len(out))
	}
	for _, st := range out {
		ceiling := blockerProdShapeCeiling
		if strings.HasPrefix(st.head, blockerProdShapeTotalsHead) {
			ceiling = blockerProdShapeTotalsCeiling
		}
		if st.peak >= ceiling {
			t.Errorf("%s: statement %d peaked at %d bytes, want under %d (the production target of 48 MiB scaled by the statement's calibration ratio: 0.86 age, 0.80 totals): %s", label, st.hash, st.peak, ceiling, st.head)
		}
	}
	return out
}

func TestTeamBlockerStatementsOnTheProductionShapeOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedProdShapedBlockerOrganization(t, ctx, direct)

	subjects := make([]contextfabric.SubjectRef, 0, 39)
	teamIDs := make([]string, 0, 39)
	for team := 0; team < 39; team++ {
		subjects = append(subjects, teamSubject(fmt.Sprintf("team-%d", team)))
		teamIDs = append(teamIDs, fmt.Sprintf("team-%d", team))
	}
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactBlockers)
	read := func(principal storage.Principal) contextfabric.FactProviderResult {
		t.Helper()
		result, err := provider.ReadFacts(ctx, principal, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, Kind: contextfabric.FactBlockers, Subjects: subjects,
		})
		if err != nil {
			t.Fatalf("ReadFacts: %v: %s", err, readFailureCause(err))
		}
		return result
	}
	orgWide := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"*"}}
	restricted := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"acme/repo-0", "acme/repo-1", "acme/repo-2", "acme/repo-3", "acme/repo-4", "acme/repo-5"}}

	read(orgWide)
	mark := serverNowMicros(t, ctx, direct)
	orgWideResult := read(orgWide)
	blockerProdShapeStatements(t, ctx, direct, "org-wide", mark)

	read(restricted)
	mark = serverNowMicros(t, ctx, direct)
	restrictedResult := read(restricted)
	blockerProdShapeStatements(t, ctx, direct, "restricted", mark)

	restore := devhealthfacts.SetBlockerAuthorizedIDsBoundForTest(0)
	read(restricted)
	mark = serverNowMicros(t, ctx, direct)
	inStatement := read(restricted)
	blockerProdShapeStatements(t, ctx, direct, "restricted-in-statement", mark)
	restore()
	for _, pair := range []struct {
		label     string
		got, want contextfabric.FactProviderResult
	}{{"restricted, authorization read once vs inside each statement", restrictedResult, inStatement}} {
		if len(pair.got.Facts) != len(pair.want.Facts) {
			t.Fatalf("%s: %d facts vs %d", pair.label, len(pair.got.Facts), len(pair.want.Facts))
		}
		for index := range pair.got.Facts {
			for _, name := range []string{"blocked_work_items_current", "blocker_dependencies_current", "blocker_dependencies_excluded_by_status_current", "blocker_dependencies_writer_unresolved_current", "project_blocked_work_items_current", "oldest_blocked_age_days"} {
				a, b := pair.got.Facts[index].Fields[name], pair.want.Facts[index].Fields[name]
				if (a.Integer == nil) != (b.Integer == nil) || (a.Integer != nil && *a.Integer != *b.Integer) {
					t.Errorf("%s: team fact %d field %s differs: %+v vs %+v", pair.label, index, name, a, b)
				}
			}
		}
	}
	served := func(result contextfabric.FactProviderResult) (teams int, blocked int64) {
		for _, fact := range result.Facts {
			if fact.Subject.Kind == contextfabric.SubjectTeam {
				teams++
				if v := fact.Fields["blocked_work_items_current"]; v.Integer != nil {
					blocked += *v.Integer
				}
			}
		}
		return teams, blocked
	}
	orgTeams, orgBlocked := served(orgWideResult)
	resTeams, resBlocked := served(restrictedResult)
	t.Logf("PRODSHAPE org-wide: %d of 39 teams served, %d blocked items; restricted: %d teams, %d blocked items", orgTeams, orgBlocked, resTeams, resBlocked)
	if orgTeams == 0 || orgBlocked == 0 {
		t.Fatalf("the production shape served no blocked items: the measurement is vacuous")
	}

	fragmentMark := serverNowMicros(t, ctx, direct)
	repoKeys := make([]string, 0, 11)
	for repo := 0; repo < 11; repo++ {
		repoKeys = append(repoKeys, fmt.Sprintf("00000000-0000-4000-8000-0000000000%02d", repo))
	}
	if err := devhealthfacts.RunBlockerFragmentsForTest(ctx, query, orgWide, blockerProdShapeOrg, repoKeys, teamIDs); err != nil {
		t.Fatalf("fragments: %v", err)
	}
	if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
		t.Fatalf("flush logs: %v", err)
	}
	for _, name := range devhealthfacts.BlockerFragmentNames {
		var peak, readRows uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), max(read_rows) FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select' AND position(query, ?) > 0`, fragmentMark, "'frag-"+name+"'").Scan(&peak, &readRows); err != nil {
			t.Fatalf("fragment %s: %v", name, err)
		}
		t.Logf("PRODSHAPE fragment %-18s peak %d bytes (%.1f MiB) read_rows %d", name, peak, float64(peak)/(1<<20), readRows)
	}
}

// The organization-wide shape is decided from the live principal on every
// call, and only an organization-wide grant with no requested restriction takes
// it.
func TestOrganizationWideAuthorizationIsDecidedFromTheLivePrincipal(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name      string
		scopes    []string
		requested []string
		want      bool
	}{
		{"organization-wide wildcard", []string{"*"}, nil, true},
		{"organization-wide by the empty-grant convention", nil, nil, true},
		{"wildcard beside a narrower entry", []string{"acme/repo-1", "*"}, nil, true},
		{"empty requested restriction", []string{"*"}, []string{}, true},
		{"one repository grant", []string{"acme/repo-1"}, nil, false},
		{"owner grant", []string{"acme/*"}, nil, false},
		{"project-reaching grant (a repository grant that reaches repo-less items through ownership)", []string{"acme/repo-1", "acme/repo-2"}, nil, false},
		{"wildcard with a requested selector", []string{"*"}, []string{"acme/repo-1"}, false},
		{"grant revoked to one repository", []string{"acme/repo-3"}, nil, false},
	} {
		got := devhealthfacts.OrganizationWideAuthorizationForTest(storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: c.scopes}, c.requested)
		if got != c.want {
			t.Errorf("%s: organization-wide shape = %v, want %v", c.name, got, c.want)
		}
		sql, _ := devhealthfacts.AuthorizedIDsForTest(storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: c.scopes}, c.requested, "production")
		if carriesLibraryRelation := strings.Contains(sql, "wia_"); carriesLibraryRelation == c.want {
			t.Errorf("%s: the served set carries the library relation = %v with the organization-wide shape = %v", c.name, carriesLibraryRelation, c.want)
		}
	}
	// Repository-id dimensions are not expressible by a principal; the decision
	// must still refuse them.
	idDimension := readers.AuthorizationScope{
		GrantedRepositoryIDs: []string{"00000000-0000-4000-8000-000000000001"},
		RepositorySelectors:  &readers.RepositorySelectorScope{Granted: readers.RepositorySelectorSet{All: true}},
	}
	if devhealthfacts.OrganizationWideAuthorizationScopeForTest(idDimension) {
		t.Error("an organization-wide grant with a granted repository id dimension took the organization-wide shape")
	}
	idDimension.GrantedRepositoryIDs, idDimension.RequestedRepositoryIDs = nil, []string{"00000000-0000-4000-8000-000000000001"}
	if devhealthfacts.OrganizationWideAuthorizationScopeForTest(idDimension) {
		t.Error("an organization-wide grant with a requested repository id dimension took the organization-wide shape")
	}
	// The same call, with a live principal whose grant changed, must follow it.
	granted := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"*"}}
	if !devhealthfacts.OrganizationWideAuthorizationForTest(granted, nil) {
		t.Fatal("the organization-wide principal did not take the organization-wide shape")
	}
	granted.RepositoryScopes = []string{"acme/repo-1"}
	if devhealthfacts.OrganizationWideAuthorizationForTest(granted, nil) {
		t.Fatal("a revoked grant still took the organization-wide shape")
	}
}

// The organization-wide shape authorizes exactly the work items the library
// relation authorizes for an organization-wide scope, and the production set
// of every other scope is the library relation itself. The differential has
// teeth: forced on a restricted scope, the same shape authorizes more than the
// library does.
func TestOrganizationWideAuthorizationEqualsTheLibraryRelationOnRealClickHouse(t *testing.T) {
	ctx := context.Background()
	_, direct := newCHAOS3780IntegrationClient(t, ctx)
	for _, statement := range devhealthschema.DDL(
		"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions",
	) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	seedProdShapedBlockerOrganization(t, ctx, direct)
	at := ts(2026, 9, 29, 0, 0, 0)
	exec := func(statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, statement)
		}
	}
	zero := "00000000-0000-0000-0000-000000000000"
	for _, item := range []struct{ id, repo, project string }{
		{"orphan", zero, ""}, {"in-owned-project", zero, "proj-1"}, {"in-unowned-project", zero, "proj-never"},
		{"linked-repo-less", zero, ""}, {"two-repos", "00000000-0000-4000-8000-000000000001", ""}, {"two-repos", "00000000-0000-4000-8000-000000000002", ""},
	} {
		exec(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced) VALUES (?, ?, ?, 't', 'issue', 'open', '', ?, ?, '', 'linear', ?, NULL, ?)`,
			item.id, item.repo, blockerProdShapeOrg, at, at, item.project, at)
	}
	exec(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, 'linked-repo-less', 1, 1, 'native', '', ?, ?)`,
		"00000000-0000-4000-8000-000000000001", at, blockerProdShapeOrg)

	ids := func(principal storage.Principal, requested []string, mode string) []string {
		sql, bindings := devhealthfacts.AuthorizedIDsForTest(principal, requested, mode)
		sql = strings.ReplaceAll(sql, "{org_id:String}", "'"+blockerProdShapeOrg+"'")
		args := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			args = append(args, clickhouse.Named(binding.Name, binding.Value))
		}
		rows, err := direct.Query(ctx, "SELECT work_item_id FROM "+sql+" ORDER BY work_item_id", args...)
		if err != nil {
			t.Fatalf("authorized ids (%s): %v", mode, err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		return out
	}
	equal := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for index := range a {
			if a[index] != b[index] {
				return false
			}
		}
		return true
	}
	for _, c := range []struct {
		name      string
		scopes    []string
		requested []string
	}{
		{"organization-wide", []string{"*"}, nil},
		{"organization-wide by convention", nil, nil},
		{"one repository grant", []string{"acme/repo-1"}, nil},
		{"owner grant", []string{"acme/*"}, nil},
		{"two repository grants (repo-less items through ownership and links)", []string{"acme/repo-1", "acme/repo-2"}, nil},
		{"wildcard with a requested selector", []string{"*"}, []string{"acme/repo-1"}},
	} {
		principal := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: c.scopes}
		production, library := ids(principal, c.requested, "production"), ids(principal, c.requested, "library")
		if !equal(production, library) {
			t.Errorf("%s: production authorizes %d work items, the library relation %d", c.name, len(production), len(library))
		}
		if devhealthfacts.OrganizationWideAuthorizationForTest(principal, c.requested) && len(production) < 17000 {
			t.Errorf("%s: organization-wide shape authorizes only %d work items", c.name, len(production))
		}
	}
	restricted := storage.Principal{OrgID: blockerProdShapeOrg, RepositoryScopes: []string{"acme/repo-1"}}
	if equal(ids(restricted, nil, "fast"), ids(restricted, nil, "library")) {
		t.Fatal("the organization-wide shape forced on a restricted scope authorizes the same work items as the library: the differential cannot tell a wrongly fired fast path")
	}
}
