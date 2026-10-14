package devhealthfacts_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// Peak memory of every statement that carries the work-item authorization
// relation, per call site, at the production shape, at two organization sizes
// and in two data part states (one merged part per table, and a pinned layout
// of many small parts with merges stopped). It asserts nothing about the
// numbers: it reports them (SITEMEM lines) so a conversion is decided from a
// measurement. Three axes besides the sites: the principal (restricted and
// organization-wide), the relation (as served = whole; and the relation alone
// over the whole organization against the same relation over the site's own
// candidate ids), and for the blocker statements the thread setting.
const sitesOrg = "5b0f7c1a-3e92-4d68-a1c4-8f2d6e903b17"

type sitesLabelClient struct {
	inner contextpacket.ClickHouseQueryClient
	tag   string
	mu    sync.Mutex
	// raw holds each statement that reads work_item_dependencies, as the
	// production code sent it, for the thread replay.
	raw []capturedStatement
}

func (c *sitesLabelClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, "work_item_dependencies") {
		c.mu.Lock()
		c.raw = append(c.raw, capturedStatement{statement: statement, bindings: append([]contextpacket.ClickHouseBinding(nil), bindings...)})
		c.mu.Unlock()
	}
	return c.inner.Query(ctx, "/* "+c.tag+" */ "+statement, bindings)
}

type sitesSeed struct {
	scale     int
	multipart bool
}

func seedAuthorizationSites(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, s sitesSeed) {
	t.Helper()
	org := sitesOrg
	at := ts(2026, 9, 29, 0, 0, 0)
	repo := func(expr string) string {
		return fmt.Sprintf("toUUID(concat('00000000-0000-4000-8000-0000000000', lpad(toString(%s), 2, '0')))", expr)
	}
	sub := strings.NewReplacer(
		"{{REPO}}", repo("number % 11"),
		"{{LINKREPO}}", repo("(number * 7) % 11"),
		"{{WI}}", fmt.Sprint(17000*s.scale),
		"{{LINKWI}}", fmt.Sprint(14017*s.scale),
		"{{ID}}", "concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(%s), 12, '0'))",
	)
	id := func(expr string) string {
		return fmt.Sprintf("concat('linear:7a1e4b0c-5d2f-4c8a-9e31-', lpad(toString(%s), 12, '0'))", expr)
	}
	tables := []string{"repos", "teams", "team_repo_ownership", "work_items", "work_item_dependencies", "work_item_dependency_first_seen", "work_graph_issue_pr",
		"git_pull_requests", "projects", "project_membership_transitions", "team_project_ownership", "work_item_team_attributions"}
	const workGraphEdgesDDL = `CREATE TABLE work_graph_edges (edge_id String, source_type String, source_id String, target_type String, target_id String, edge_type String, repo_id UUID, provider String, provenance String, confidence Float64, evidence Nullable(String), discovered_at DateTime64(3), last_synced DateTime64(3), org_id String) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, edge_id)`
	for _, statement := range append(devhealthschema.DDL(tables...), workGraphEdgesDDL) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if s.multipart {
		for _, table := range append(tables, "work_graph_edges") {
			// devhealthschema:not-a-production-replica: test merge control over tables the shared DDL creates; it declares no table.
			if err := direct.Exec(ctx, `SYSTEM STOP MERGES `+table); err != nil {
				t.Fatalf("stop merges on %s: %v", table, err)
			}
		}
	}
	// ins runs statement (which ends in numbers(?, ?)) over total numbers, in
	// one insert (one part) or in chunks of 1,000 rows (many parts).
	ins := func(statement string, total int, args ...any) {
		t.Helper()
		step := total
		if s.multipart && total > 1000 {
			step = 1000
		}
		for lo := 0; lo < total; lo += step {
			n := step
			if lo+n > total {
				n = total - lo
			}
			if err := direct.Exec(ctx, statement, append(append([]any(nil), args...), lo, n)...); err != nil {
				t.Fatalf("seed: %v\n%s", err, statement)
			}
		}
	}
	scale := s.scale
	ins(`INSERT INTO repos (id, org_id, repo, provider, last_synced) SELECT `+repo("number")+`, ?, concat('acme/repo-', toString(number)), 'github', ? FROM numbers(?, ?)`, 11, org, at)
	ins(`INSERT INTO teams (id, name, description, updated_at, org_id, provider, project_keys, is_active) SELECT concat('team-', toString(number)), concat('team-', toString(number)), NULL, ?, ?, 'linear', [], 1 FROM numbers(?, ?)`, 39, at, org)
	ins(`INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 24) % 6)), `+repo("(number % 24) % 11")+`, concat('acme/repo-', toString((number % 24) % 11)), 'exact', 'native', 1, 100, 0,
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)),
  if(intDiv(number, 24) = 1847 AND number % 24 < 18, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 24))
FROM numbers(?, ?)`, 44352, org)
	ins(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) SELECT concat('proj-', toString(number % 33)), ?, 'linear', NULL, concat('proj-', toString(number % 33)), 1, 'active', '', ? + toIntervalMinute(number) FROM numbers(?, ?)`, 47, org, at)
	ins(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at)
SELECT ?, 'linear', concat('team-', toString((number % 88) % 36)), concat('proj-', toString((number % 88) % 48)), NULL, 'native',
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)),
  if(intDiv(number, 88) = 766 AND number % 88 < 60, NULL, toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88)) + toIntervalMinute(30)),
  toDateTime64('2026-01-01 00:00:00', 3, 'UTC') + toIntervalHour(intDiv(number, 88))
FROM numbers(?, ?)`, 67496, org)
	ins(sub.Replace(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT `+id("number")+`, {{REPO}}, ?, 'title', 'issue', if(number % 5 = 0, 'done', 'open'), '', ?, ?, '', 'linear', concat('proj-', toString(number % 33)), NULL, ? FROM numbers(?, ?)`), 17000*scale, org, at, at, at)
	// Repo-less work items, half of them issue endpoints in the work graph.
	ins(`INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, updated_at, created_at, parent_id, provider, project_id, completed_at, last_synced)
SELECT concat('linear:repoless-', lpad(toString(number), 12, '0')), toUUID('00000000-0000-0000-0000-000000000000'), ?, 'title', 'issue', 'open', '', ?, ?, '', 'linear', concat('proj-', toString(number % 33)), NULL, ? FROM numbers(?, ?)`, 3000*scale, org, at, at, at)
	ins(`INSERT INTO work_graph_edges (edge_id, source_type, source_id, target_type, target_id, edge_type, repo_id, provider, provenance, confidence, evidence, discovered_at, last_synced, org_id)
SELECT concat('edge-', toString(number)), 'issue', concat('linear:repoless-', lpad(toString(number), 12, '0')), 'pr', concat('pr-', toString(number)), 'implements', toUUID('00000000-0000-0000-0000-000000000000'), 'linear', 'native', 0.9, 'linear_attachment', ?, ?, ? FROM numbers(?, ?)`, 1500*scale, at, at, org)
	ins(`INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced, created_at, merged_at, closed_at, head_branch, body)
SELECT `+repo("number % 11")+`, ?, toUInt32(intDiv(number, 11) + 1), 'pr', 'open', ?, ?, NULL, NULL, 'feature', '' FROM numbers(?, ?)`, 7755*scale, org, at, at)
	ins(sub.Replace(`INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)
SELECT {{LINKREPO}}, `+id("number % {{LINKWI}}")+`, toUInt32((number * 31) % 705 + 1), 1, multiIf(number % 3 = 0, 'native', number % 3 = 1, 'explicit_text', 'heuristic'), '', ?, ? FROM numbers(?, ?)`), 23905*scale, at, org)
	ins(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, `+id("number % 1602")+`, `+id("(number * 37 + 5 + intDiv(number, 1602)) % 1602")+`, 'blocks', 'blocks', ?, ? - toIntervalDay(number % 90) FROM numbers(?, ?)`, 2103, org, at, at)
	ins(sub.Replace(`INSERT INTO work_item_dependencies (org_id, source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, relation_started_at)
SELECT ?, `+id("number % {{WI}}")+`, `+id("(number * 11 + 3) % {{WI}}")+`, 'relates_to', 'relates', ?, NULL FROM numbers(?, ?)`), 21142*scale, org, at)
	ins(sub.Replace(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, team_name, source, is_primary, confidence, computed_at)
SELECT ?, {{REPO}}, `+id("number % {{WI}}")+`, concat('team-', toString(number % 39)), NULL, 'project_ownership', 1, 'medium', ? + toIntervalMinute(intDiv(number, {{WI}})) FROM numbers(?, ?)`), 67872*scale, org, at)
	ins(`INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id)
SELECT ?, NULL, `+repo("number % 11")+`, 'work_item', `+id("number % 17000")+`, 'linear', concat('proj-', toString(number % 33)), concat('proj-', toString((number + 1) % 33)), '', '', '', ? + toIntervalMinute(number), ? + toIntervalMinute(number), concat('event-', toString(number)) FROM numbers(?, ?)`, 9075, org, at, at)
	if !s.multipart {
		for _, table := range append(tables, "work_graph_edges") {
			// devhealthschema:not-a-production-replica: test merge control over tables the shared DDL creates; it declares no table.
			if err := direct.Exec(ctx, `OPTIMIZE TABLE `+table+` FINAL`); err != nil {
				t.Fatalf("optimize %s: %v", table, err)
			}
		}
	}
}

func sitesPartCount(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) uint64 {
	t.Helper()
	var parts uint64
	if err := direct.QueryRow(ctx, `SELECT count() FROM system.parts WHERE database = currentDatabase() AND active AND table IN ('work_items', 'work_graph_issue_pr', 'team_project_ownership', 'team_repo_ownership')`).Scan(&parts); err != nil {
		t.Fatalf("count parts: %v", err)
	}
	return parts
}

func TestWorkItemAuthorizationSitesPeakMemoryOnRealClickHouse(t *testing.T) {
	for _, state := range []struct {
		name      string
		multipart bool
	}{{"merged", false}, {"multipart", true}} {
		for _, size := range []struct {
			name  string
			scale int
		}{{"small", 1}, {"large", 3}} {
			state, size := state, size
			t.Run(state.name+"_"+size.name, func(t *testing.T) {
				measureAuthorizationSites(t, state.name, size.name, sitesSeed{scale: size.scale, multipart: state.multipart})
			})
		}
	}
}

func measureAuthorizationSites(t *testing.T, state, size string, seed sitesSeed) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClientWithOptions(t, ctx, func(options *runtimeclickhouse.Options) {
		maxBytesToRead := config.DefaultClickHouseMaxBytesToRead
		options.MaxBytesToRead = &maxBytesToRead
	})
	seedAuthorizationSites(t, ctx, direct, seed)
	parts := sitesPartCount(t, ctx, direct)
	t.Logf("SITEMEM layout state=%s size=%s active parts (work_items, links, two ownership tables) = %d", state, size, parts)
	if seed.multipart && parts < 40 {
		t.Fatalf("multipart layout holds %d active parts, want at least 40: the layout is not pinned", parts)
	}

	var counter int
	var counterMu sync.Mutex
	nextTag := func() string {
		counterMu.Lock()
		defer counterMu.Unlock()
		counter++
		return fmt.Sprintf("m9576#%d#", counter)
	}
	// peak reads the largest memory_usage among the statements tagged tag.
	peak := func(mark int64, tag string) (uint64, uint64) {
		t.Helper()
		if err := direct.Exec(ctx, `SYSTEM FLUSH LOGS`); err != nil {
			t.Fatalf("flush logs: %v", err)
		}
		var maxMemory, statements uint64
		if err := direct.QueryRow(ctx, `SELECT max(memory_usage), count() FROM system.query_log WHERE `+queryLogSinceMark+` AND type IN ('QueryFinish', 'ExceptionWhileProcessing') AND query_kind = 'Select' AND position(query, ?) > 0`, mark, tag).Scan(&maxMemory, &statements); err != nil {
			t.Fatalf("read query_log: %v", err)
		}
		return maxMemory, statements
	}
	mib := func(v uint64) float64 { return float64(v) / (1 << 20) }
	// measure runs fn once unmeasured, then three times, and reports the
	// minimum peak of the three.
	measure := func(label string, fn func(client contextpacket.ClickHouseQueryClient) error) {
		t.Helper()
		var best uint64
		var statements uint64
		var lastErr error
		for run := 0; run < 4; run++ {
			tag := nextTag()
			client := &sitesLabelClient{inner: query, tag: tag}
			mark := serverNowMicros(t, ctx, direct)
			lastErr = fn(client)
			p, n := peak(mark, tag)
			if run == 0 {
				continue
			}
			statements = n
			if best == 0 || p < best {
				best = p
			}
		}
		if statements == 0 {
			t.Fatalf("SITEMEM %s: no statement was logged: the measurement did not happen (last error %v)", label, lastErr)
		}
		t.Logf("SITEMEM state=%s size=%s %-58s min-of-3 peak %6.2f MiB statements=%d err=%v", state, size, label, mib(best), statements, lastErr)
	}

	orgWide := storage.Principal{OrgID: sitesOrg, RepositoryScopes: []string{"*"}}
	restricted := storage.Principal{OrgID: sitesOrg, RepositoryScopes: []string{"acme/repo-0", "acme/repo-1", "acme/repo-2", "acme/repo-3", "acme/repo-4", "acme/repo-5"}}
	principals := []struct {
		name string
		p    storage.Principal
	}{{"restricted", restricted}, {"org-wide", orgWide}}

	projects := []contextfabric.SubjectRef{projectSubject("linear", "proj-1"), projectSubject("linear", "proj-2")}
	allProjects := make([]contextfabric.SubjectRef, 0, 33)
	for p := 0; p < 33; p++ {
		allProjects = append(allProjects, projectSubject("linear", fmt.Sprintf("proj-%d", p)))
	}
	teams := make([]contextfabric.SubjectRef, 0, 39)
	for team := 0; team < 39; team++ {
		teams = append(teams, teamSubject(fmt.Sprintf("team-%d", team)))
	}
	current := contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}

	for _, pr := range principals {
		pr := pr
		// Site 1: projectWorkItems (scope expander, project roots).
		measure("site1 project work items expander "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
			_, err := devhealthfacts.NewScopeExpander(client).ExpandFactScope(ctx, contextfabric.FactScopeExpansionRequest{
				Principal: pr.p, RequirementKind: contextfabric.FactStatus, Origins: projects,
				Policy: contextfabric.FactScopePolicyProjectWorkItemStatus, TargetKind: contextfabric.SubjectWorkItem, TimeContext: current, Limit: 200,
			})
			return err
		})
		// Site 3: blocker statements (dependencies.go workItemAuthorizedIDsSQL).
		measure("site3 blockers provider "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
			provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactBlockers)
			_, err := provider.ReadFacts(ctx, pr.p, contextfabric.FactQuery{Time: current, Kind: contextfabric.FactBlockers, Subjects: teams})
			return err
		})
		// Site 3, thread axis: replay the statements the provider sent with
		// the thread setting as served and with two threads.
		capture := &sitesLabelClient{inner: query, tag: nextTag()}
		provider := findProvider(t, devhealthfacts.NewProviders(capture), contextfabric.FactBlockers)
		if _, err := provider.ReadFacts(ctx, pr.p, contextfabric.FactQuery{Time: current, Kind: contextfabric.FactBlockers, Subjects: teams}); err != nil {
			t.Logf("SITEMEM site3 capture read: %v", err)
		}
		for _, threads := range []string{"max_threads = 1", "max_threads = 2"} {
			threads := threads
			measure("site3 blockers replay "+threads+" "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
				var firstErr error
				for _, raw := range capture.raw {
					statement := raw.statement
					if strings.Contains(statement, "max_threads = 1") {
						statement = strings.Replace(statement, "max_threads = 1", threads, 1)
					} else {
						continue
					}
					rows, err := client.Query(ctx, statement, raw.bindings)
					if err != nil {
						if firstErr == nil {
							firstErr = err
						}
						continue
					}
					for rows.Next() {
					}
					rows.Close()
				}
				return firstErr
			})
		}
		// Site 4: project actual completion.
		measure("site4 project actual completion "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
			provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactActualCompletion)
			_, err := provider.ReadFacts(ctx, pr.p, contextfabric.FactQuery{Time: current, Kind: contextfabric.FactActualCompletion, Subjects: allProjects})
			return err
		})
		// Site 5: membership S1.
		measure("site5 membership S1 "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
			gate, err := contextfabric.NewWorkItemMembershipGate(1, 1)
			if err != nil {
				return err
			}
			reader, err := devhealthfacts.NewWorkItemMembershipReader(client, devhealthfacts.WorkItemMembershipReaderOptions{Gate: gate, Now: func() time.Time { return ts(2026, 9, 30, 0, 0, 0) }})
			if err != nil {
				return err
			}
			lease, _, err := reader.BeginWorkItemMembership(ctx, pr.p, contextfabric.WorkItemMembershipRequest{Anchor: contextfabric.WorkItemMembershipAnchor{Subject: projects[0]}})
			if lease != nil {
				lease.Release()
			}
			return err
		})
		// Site 6: repo-less single-id admission.
		measure("site6 repo-less admission "+pr.name, func(client contextpacket.ClickHouseQueryClient) error {
			_, err := devhealthfacts.NewRepoLessWorkItemAdmitter(client).AdmitsRepoLessWorkItem(ctx, pr.p, "linear:repoless-000000000007")
			return err
		})
	}

	// Relation level: the library authorization relation over the whole
	// organization against the same relation over each site's candidate ids.
	// This is the part of a statement a conversion can change.
	relation := func(label string, principal storage.Principal, candidates string) {
		t.Helper()
		sql, bindings := devhealthfacts.AuthorizationRelationForTest(principal, candidates)
		sql = strings.ReplaceAll(sql, "{org_id:String}", "'"+sitesOrg+"'")
		args := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			args = append(args, clickhouse.Named(binding.Name, binding.Value))
		}
		var best, statements uint64
		var authorized uint64
		for run := 0; run < 4; run++ {
			tag := nextTag()
			mark := serverNowMicros(t, ctx, direct)
			if err := direct.QueryRow(ctx, "/* "+tag+" */ SELECT count() FROM "+sql, args...).Scan(&authorized); err != nil {
				t.Fatalf("relation %s: %v", label, err)
			}
			p, n := peak(mark, tag)
			if run == 0 {
				continue
			}
			statements = n
			if best == 0 || p < best {
				best = p
			}
		}
		if statements == 0 {
			t.Fatalf("relation %s: no statement was logged", label)
		}
		t.Logf("SITEMEM state=%s size=%s %-58s min-of-3 peak %6.2f MiB authorized=%d", state, size, "relation "+label, mib(best), authorized)
	}
	candidateSets := []struct{ name, sql string }{
		{"whole organization", ""},
		{"site1/5 project ids", `(SELECT work_item_id FROM work_items WHERE org_id = '` + sitesOrg + `' AND project_id IN ('proj-1', 'proj-2'))`},
		{"site3 blocker endpoints", `(SELECT arrayJoin([source_work_item_id, target_work_item_id]) FROM work_item_dependencies FINAL WHERE org_id = '` + sitesOrg + `' AND relationship_type = 'blocks')`},
		{"site6 one id", `(SELECT 'linear:repoless-000000000007')`},
		{"site7 issue endpoints", `(SELECT source_id FROM work_graph_edges WHERE org_id = '` + sitesOrg + `' AND source_type = 'issue')`},
	}
	for _, pr := range principals {
		for _, set := range candidateSets {
			relation(pr.name+" over "+set.name, pr.p, set.sql)
		}
	}

	// Site 7: the repo-less issue gate statement (mirror), whole relation.
	for _, pr := range principals {
		pr := pr
		sql, bindings := devhealthfacts.RepoLessIssueStatementForTest(pr.p, `(SELECT source_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = toUUID('00000000-0000-0000-0000-000000000000') AND source_type = 'issue' UNION ALL SELECT target_id FROM work_graph_edges WHERE org_id = {org_id:String} AND repo_id = toUUID('00000000-0000-0000-0000-000000000000') AND target_type = 'issue')`)
		sql = strings.ReplaceAll(sql, "{org_id:String}", "'"+sitesOrg+"'")
		args := make([]any, 0, len(bindings))
		for _, binding := range bindings {
			args = append(args, clickhouse.Named(binding.Name, binding.Value))
		}
		var best, statements uint64
		for run := 0; run < 4; run++ {
			tag := nextTag()
			mark := serverNowMicros(t, ctx, direct)
			rows, err := direct.Query(ctx, "/* "+tag+" */ "+sql, args...)
			if err != nil {
				t.Fatalf("site7 %s: %v", pr.name, err)
			}
			for rows.Next() {
			}
			rows.Close()
			p, n := peak(mark, tag)
			if run == 0 {
				continue
			}
			statements = n
			if best == 0 || p < best {
				best = p
			}
		}
		if statements == 0 {
			t.Fatalf("site7 %s: no statement was logged", pr.name)
		}
		t.Logf("SITEMEM state=%s size=%s %-58s min-of-3 peak %6.2f MiB (mirror of the statement)", state, size, "site7 repo-less issue gate "+pr.name, mib(best))
	}
}
