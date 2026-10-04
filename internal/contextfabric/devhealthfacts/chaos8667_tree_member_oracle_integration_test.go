package devhealthfacts

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
)

// Differential oracle, CHAOS-8667: which work items of a repository may a
// caller see, decided by two implementations that must agree.
//
//	Path G (graph): the REAL producer (devhealthsource.ClickHouseProjectionSource)
//	  projects seeded ClickHouse rows into a REAL FalkorDB; the REAL
//	  falkorgraph Adapter.TreeWorkItemMembers walks repository -> pull request
//	  -> issue over LINKS_PULL_REQUEST and applies admitted() per row.
//	Path L (library): the issues linked through work_graph_issue_pr (FINAL, any
//	  tier) to a pull request of the anchor repository, filtered by the
//	  authorization relation of readers.WorkItemScopeSQL, built from this
//	  package's workItemRepositoryAuthorization (the translation every
//	  work-item reader uses). The library's own JoinSQL, AuthorizationExpr and
//	  Bindings are used as returned; no part of its SQL is re-typed here.
//
// Compared: the set of work item identities (the issue canonical id the graph
// returns; the same id derived with identity.Derive from the stored
// (repo_id, work_item_id) on the library side), per principal.
//
// Where the two rules differ BY DESIGN the difference is a named, documented
// exception in oracleExceptions below, asserted EXACTLY (the observed
// difference must equal the declared one: a missing and an extra difference
// both fail). An undeclared difference fails the test.
//
// Needs Docker (ClickHouse and FalkorDB containers). Written to be run by CI
// or by the lane owner; not run in the authoring sandbox.

const (
	oracleOrg       = "86670000-0000-4000-8000-000000000001"
	oracleFalkorImg = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"
	oracleZeroRepo  = "00000000-0000-0000-0000-000000000000"
	oracleAnchor    = "acme/svc"
)

// oracleSchemaTables are the tables the projection source reads plus the
// ownership tables the library rule reads. Naming a subset picks WHICH
// declared tables to render; every column type still comes from
// devhealthschema.DDL.
// devhealthschema:not-a-production-replica
var oracleSchemaTables = []string{
	"repos", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"ci_pipeline_runs", "deployments", "operational_incidents",
	"operational_service_repository_mappings", "work_item_dependencies",
	"work_graph_deployment_incident_edges", "work_graph_issue_pr",
	"teams", "projects", "work_item_team_attributions", "team_project_ownership",
	"project_membership_transitions", "team_repo_ownership",
}

type oracleItem struct {
	id, repo, provider, itemType, project string
}

type oracleLink struct {
	issue string
	// pullRepo is the pull request's repository key ("svc" or "other"); the
	// link row's repo_id is the PULL REQUEST's repository.
	pullRepo string
	number   uint32
	tier     string
}

type oraclePrincipal struct {
	name      string
	scopes    []string
	requested []string
}

type oracleException struct {
	// graphOnly: work item ids the graph returns and the library does not.
	graphOnly []string
	// libraryOnly: work item ids the library admits and the graph does not.
	libraryOnly []string
	reason      string
}

const oracleReasonPresenceVsColumn = "PV presence vs own project column: the graph reads an issue's project from project_membership_presence, the library from work_items.project_id"

// oracleExceptions are the DESIGNED differences between the two rules, by
// principal name. Each is asserted exactly.
var oracleExceptions = map[string]oracleException{
	// E1 (project ownership) is no longer a difference: the graph walk admits
	// a repository-less issue through a project owned by a team that owns a
	// granted repository, as the library does, so G equals L on linear:CHAOS-14.
	//
	// PV presence vs own project column. The graph reaches an issue's project
	// over BELONGS_TO_PROJECT, which the producer reads from
	// project_membership_presence (a transition history wins over the column);
	// the library reads the item's own work_items.project_id. When the two
	// name different projects the rules differ, in both directions:
	//   linear:CHAOS-16: presence project is owned, own project_id is not (graph admits);
	//   linear:CHAOS-17: own project_id is owned, presence project is not (library admits).
	"[acme/svc]":   {graphOnly: []string{"linear:CHAOS-16"}, libraryOnly: []string{"linear:CHAOS-17"}, reason: oracleReasonPresenceVsColumn},
	"[ACME/Svc]":   {graphOnly: []string{"linear:CHAOS-16"}, libraryOnly: []string{"linear:CHAOS-17"}, reason: oracleReasonPresenceVsColumn},
	"[acme/*]":     {graphOnly: []string{"linear:CHAOS-16"}, libraryOnly: []string{"linear:CHAOS-17"}, reason: oracleReasonPresenceVsColumn},
	"[acme/other]": {libraryOnly: []string{"gh:acme/other#4"}, reason: "E2 anchor gate: the graph walk requires the anchor repository and its pull request nodes to be authorized for the caller, so a caller without acme/svc gets nothing; the library has no anchor and authorizes the issue by its own repository acme/other"},
	// E3 requested scope. The library ANDs the requested selector into every
	// authorization path, and the requested selector needs the item's OWN
	// repository to match (renderRequestedRepositorySelectorSet requires a
	// present, named repository), so a repository-less item never passes under
	// a requested scope, native link or not. The graph admits a repository-less
	// issue through a native link to a pull request of the requested repository.
	// STATIC READING: confirm on the first live run.
	"unrestricted+requested[acme/svc]": {graphOnly: []string{"linear:CHAOS-10", "jira:PROJ-11"}, reason: "E3 requested scope (removed by CHAOS-8694, which carries the requested scope into the walk): library requires the item's own repository to match the requested selector; the graph admits repository-less issues through a native link"},
}

func oracleCanonical(t *testing.T, repoID, workItemID string) string {
	t.Helper()
	id, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		t.Fatalf("derive work item id for %s %s: omitted=%t err=%v", repoID, workItemID, omitted, err)
	}
	return id
}

func oracleUUID(label string) string {
	// Deterministic, UUID-shaped, distinct per label.
	h := fmt.Sprintf("%032x", []byte(label))
	for len(h) < 32 {
		h += "0"
	}
	h = h[len(h)-32:]
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

func oracleClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	address := net.JoinHostPort(host, port.Port())
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{address}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := direct.Ping(ctx); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("ping ClickHouse: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: "clickhouse://acr:acr@" + address + "/default", DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	for _, statement := range devhealthschema.DDL(oracleSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create membership view: %v", err)
	}
	return query, direct
}

func oracleFalkor(t *testing.T, ctx context.Context) *falkorgraph.Adapter {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: oracleFalkorImg, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-8667", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 100, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatalf("falkorgraph.New: %v", err)
	}
	return adapter
}

func oracleDrain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, adapter *falkorgraph.Adapter) {
	t.Helper()
	cursor := ""
	for page := 0; page < 200; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: oracleOrg, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("projection page %d: %v", page, err)
		}
		if !available {
			return
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("apply page %d: %v", page, err)
		}
		if batch.NextCursor == cursor {
			t.Fatalf("projection page %d made no cursor progress", page)
		}
		cursor = batch.NextCursor
	}
	t.Fatal("projection did not drain")
}

func oracleSetDiff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func oracleSorted(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

func TestChaos8667RepositoryWorkItemMembersGraphAgreesWithLibraryRule(t *testing.T) {
	ctx := context.Background()
	query, direct := oracleClickHouse(t, ctx)
	adapter := oracleFalkor(t, ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-90 * 24 * time.Hour)
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	repoIDs := map[string]string{"svc": oracleUUID("svc"), "other": oracleUUID("other"), "gl": oracleUUID("gl")}
	for _, r := range []struct{ key, slug, provider string }{
		{"svc", oracleAnchor, "github"}, {"other", "acme/other", "github"}, {"gl", "acme/gl-proj", "gitlab"},
	} {
		exec("repo "+r.key, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoIDs[r.key], oracleOrg, r.slug, r.provider, now)
	}
	// Pull requests: two of acme/svc (the anchor), one of acme/other.
	type pull struct {
		repo   string
		number uint32
	}
	pulls := []pull{{"svc", 1}, {"svc", 2}, {"other", 1}}
	for _, p := range pulls {
		exec(fmt.Sprintf("pull request %s#%d", p.repo, p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			repoIDs[p.repo], oracleOrg, p.number, fmt.Sprintf("PR %d", p.number), "open", created, now)
	}

	// Project rows the library's project-ownership path reads: project
	// proj-p1 (Linear) is owned by team-svc, which owns acme/svc.
	exec("project", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'linear', NULL, 'P1', 1, 'started', '', ?)`, "proj-p1", oracleOrg, now)
	exec("team project ownership", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, 'linear', 'team-svc', ?, NULL, 'native', ?, NULL, ?)`, oracleOrg, "proj-p1", created, now)
	exec("project unowned", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, 'linear', NULL, 'Unowned', 1, 'started', '', ?)`, "proj-unowned", oracleOrg, now)
	exec("team", `INSERT INTO teams (id, name, updated_at, org_id, provider, is_active) VALUES ('team-svc', 'Team Svc', ?, ?, 'linear', 1)`, now, oracleOrg)
	exec("team repo ownership", `INSERT INTO team_repo_ownership (org_id, provider, team_id, repo_id, repo_full_name, match_type, source, is_primary, specificity, priority, valid_from, valid_to, updated_at) VALUES (?, 'github', 'team-svc', ?, ?, 'exact', 'inferred', 0, 0, 0, ?, NULL, ?)`, oracleOrg, repoIDs["svc"], oracleAnchor, created, now)

	items := []oracleItem{
		{"gh:acme/svc#1", "svc", "github", "issue", ""},            // own repo, native
		{"gh:acme/svc#2", "svc", "github", "issue", ""},            // own repo, heuristic
		{"gh:acme/svc#8", "svc", "github", "issue", ""},            // own repo, explicit_text
		{"gitlab:acme/gl-proj#3", "gl", "gitlab", "issue", ""},     // gitlab own repo, native
		{"linear:CHAOS-10", "", "linear", "issue", ""},             // repository-less, native
		{"jira:PROJ-11", "", "jira", "story", ""},                  // repository-less, native
		{"linear:CHAOS-12", "", "linear", "issue", ""},             // repository-less, explicit_text ONLY
		{"jira:PROJ-13", "", "jira", "story", ""},                  // repository-less, heuristic ONLY
		{"gh:acme/other#4", "other", "github", "issue", ""},        // another repository's issue, native to an acme/svc PR
		{"linear:CHAOS-14", "", "linear", "issue", "proj-p1"},      // repository-less, project-owned, explicit_text ONLY
		{"linear:CHAOS-16", "", "linear", "issue", "proj-unowned"}, // PV (i): own project unowned, presence project owned, explicit_text ONLY
		{"linear:CHAOS-17", "", "linear", "issue", "proj-p1"},      // PV (ii): own project owned, presence project unowned, explicit_text ONLY
		{"gh:acme/svc#6", "svc", "github", "pr", ""},               // a pull request typed work item: never a member
		{"gh:acme/svc#7", "svc", "github", "issue", ""},            // no link at all
		{"gh:acme/other#9", "other", "github", "issue", ""},        // linked to a pull request of acme/other only
		{"linear:CHAOS-15", "", "linear", "issue", ""},             // repository-less, native to a pull request of acme/other only
	}
	itemRepo := map[string]string{}
	for _, i := range items {
		repoID := oracleZeroRepo
		if i.repo != "" {
			repoID = repoIDs[i.repo]
		}
		itemRepo[i.id] = repoID
		exec("work item "+i.id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.id, repoID, oracleOrg, i.id, i.itemType, "open", i.provider, i.project, created, now, now)
	}
	links := []oracleLink{
		{"gh:acme/svc#1", "svc", 1, "native"},
		{"gh:acme/svc#2", "svc", 2, "heuristic"},
		{"gh:acme/svc#8", "svc", 2, "explicit_text"},
		{"gitlab:acme/gl-proj#3", "svc", 1, "native"},
		{"linear:CHAOS-10", "svc", 1, "native"},
		{"jira:PROJ-11", "svc", 2, "native"},
		{"linear:CHAOS-12", "svc", 1, "explicit_text"},
		{"jira:PROJ-13", "svc", 2, "heuristic"},
		{"gh:acme/other#4", "svc", 1, "native"},
		{"linear:CHAOS-14", "svc", 1, "explicit_text"},
		{"linear:CHAOS-16", "svc", 1, "explicit_text"},
		{"linear:CHAOS-17", "svc", 2, "explicit_text"},
		{"gh:acme/svc#6", "svc", 1, "native"},
		{"linear:GHOST-1", "svc", 1, "native"}, // no work item row
		{"gh:acme/other#9", "other", 1, "native"},
		{"linear:CHAOS-15", "other", 1, "native"},
	}
	// Presence history that disagrees with the item's own project column
	// (the presence view prefers the transition history for a subject that
	// has one): CHAOS-16 joined the owned project, CHAOS-17 the unowned one.
	for i, m := range []struct{ item, project string }{{"linear:CHAOS-16", "proj-p1"}, {"linear:CHAOS-17", "proj-unowned"}} {
		exec(fmt.Sprintf("membership transition %d", i), `INSERT INTO project_membership_transitions (org_id, source_id, repo_id, subject_kind, subject_id, provider, from_project_id, to_project_id, from_project_key, to_project_key, actor, occurred_at, last_synced, event_id, ingested_at) VALUES (?, NULL, ?, 'work_item', ?, 'linear', '', ?, '', '', '', ?, ?, ?, ?)`,
			oracleOrg, oracleZeroRepo, m.item, m.project, created, now, fmt.Sprintf("pv-%d", i), now)
	}
	for i, l := range links {
		exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoIDs[l.pullRepo], l.issue, l.number, float32(0.9), l.tier, "", now, oracleOrg)
	}

	// The seed must have landed: row counts read back from ClickHouse.
	count := func(statement string) uint64 {
		t.Helper()
		rows, err := direct.Query(ctx, statement, oracleOrg)
		if err != nil {
			t.Fatalf("count: %v\n%s", err, statement)
		}
		defer rows.Close()
		var n uint64
		if !rows.Next() {
			t.Fatalf("count returned no row: %s", statement)
		}
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct {
		table string
		want  int
	}{{"repos", 3}, {"git_pull_requests", len(pulls)}, {"work_items", len(items)}, {"work_graph_issue_pr", len(links)}, {"team_repo_ownership", 1}, {"team_project_ownership", 1}, {"projects", 2}, {"teams", 1}, {"project_membership_transitions", 2}} {
		if got := count("SELECT count() FROM " + c.table + " FINAL WHERE org_id = ?"); got != uint64(c.want) {
			t.Fatalf("seed: %s has %d rows, want %d", c.table, got, c.want)
		}
	}

	// Project through the REAL producer into the REAL graph.
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	oracleDrain(t, ctx, source, adapter)

	// The projection must have produced the edges: one LINKS_PULL_REQUEST
	// edge per link row whose issue exists, is not pull-request typed and
	// whose pull request exists (the producer's own skip rules, queryIssuePullRequestLinks).
	skippedIssues := map[string]bool{"linear:GHOST-1": true, "gh:acme/svc#6": true}
	wantEdges := 0
	for _, l := range links {
		if !skippedIssues[l.issue] {
			wantEdges++
		}
	}
	probe := storage.Principal{OrgID: oracleOrg, Subject: "oracle", CredentialID: "oracle"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, probe)
	if err != nil {
		t.Fatalf("resolve graph binding: %v", err)
	}
	var origins []contextfabric.SubjectRef
	for _, p := range pulls {
		origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:%s:%d", repoIDs[p.repo], p.number)})
	}
	page, err := adapter.DirectEdgePage(ctx, probe, binding, directread.EdgePageQuery{
		Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("direct edge page: %v", err)
	}
	if page.More || len(page.Edges) != wantEdges {
		t.Fatalf("projection produced %d LINKS_PULL_REQUEST edges (more=%t), want %d: the graph is not the seed", len(page.Edges), page.More, wantEdges)
	}

	canonicalToID := map[string]string{}
	for _, i := range items {
		canonicalToID[oracleCanonical(t, itemRepo[i.id], i.id)] = i.id
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + repoIDs["svc"], Label: oracleAnchor}

	// G: the graph rule.
	graphSet := func(p oraclePrincipal) map[string]bool {
		t.Helper()
		principal := storage.Principal{OrgID: oracleOrg, Subject: "oracle", CredentialID: "oracle", RepositoryScopes: p.scopes}
		b, err := adapter.ResolveInvestigationBinding(ctx, principal)
		if err != nil {
			t.Fatalf("%s: resolve binding: %v", p.name, err)
		}
		walk, err := adapter.TreeWorkItemMembers(ctx, principal, b, contextfabric.RequestedScope{RepositorySlugs: p.requested}, anchor, 100)
		if err != nil {
			t.Fatalf("%s: TreeWorkItemMembers: %v", p.name, err)
		}
		if walk.Truncated {
			t.Fatalf("%s: graph walk truncated; the comparison would be partial", p.name)
		}
		out := map[string]bool{}
		for _, m := range walk.Members {
			id, ok := canonicalToID[m.Subject.CanonicalID]
			if !ok {
				t.Fatalf("%s: graph member %s is not a seeded work item", p.name, m.Subject.CanonicalID)
			}
			out[id] = true
		}
		return out
	}

	// L: the library rule over the same relation.
	librarySet := func(p oraclePrincipal) map[string]bool {
		t.Helper()
		principal := storage.Principal{OrgID: oracleOrg, RepositoryScopes: p.scopes}
		rendered := readers.WorkItemScopeSQL(workItemRepositoryAuthorization(principal, p.requested))
		statement := `SELECT DISTINCT toString(w.repo_id), w.work_item_id
FROM work_items AS w FINAL
` + rendered.JoinSQL + `
WHERE w.org_id = {org_id:String}
  AND lower(w.type) NOT IN ('pr', 'merge_request')
  AND (` + rendered.AuthorizationExpr + `)
  AND w.work_item_id IN (
    SELECT oracle_l.work_item_id
    FROM work_graph_issue_pr AS oracle_l FINAL
    INNER JOIN (SELECT id FROM repos FINAL WHERE org_id = {org_id:String} AND repo = '` + oracleAnchor + `') AS oracle_a ON oracle_a.id = oracle_l.repo_id
    INNER JOIN git_pull_requests AS oracle_p FINAL ON oracle_p.org_id = oracle_l.org_id AND oracle_p.repo_id = oracle_l.repo_id AND oracle_p.number = oracle_l.pr_number
    WHERE oracle_l.org_id = {org_id:String})`
		bindings := append([]contextpacket.ClickHouseBinding{{Name: "org_id", Value: oracleOrg}}, rendered.Bindings...)
		rows, err := query.Query(ctx, statement, bindings)
		if err != nil {
			t.Fatalf("%s: library read: %v\n%s", p.name, err, statement)
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var repoID, workItemID string
			if err := rows.Scan(&repoID, &workItemID); err != nil {
				t.Fatalf("%s: scan: %v", p.name, err)
			}
			out[workItemID] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("%s: library rows: %v", p.name, err)
		}
		return out
	}

	principals := []oraclePrincipal{
		{name: "unrestricted"},
		{name: "[acme/svc]", scopes: []string{"acme/svc"}},
		{name: "[ACME/Svc]", scopes: []string{"ACME/Svc"}},
		{name: "[acme/*]", scopes: []string{"acme/*"}},
		{name: "[acme/other]", scopes: []string{"acme/other"}},
		{name: "unrestricted+requested[acme/svc]", requested: []string{"acme/svc"}},
	}
	results := map[string][2]map[string]bool{}
	for _, p := range principals {
		g, l := graphSet(p), librarySet(p)
		results[p.name] = [2]map[string]bool{g, l}
		graphOnly, libraryOnly := oracleSetDiff(g, l), oracleSetDiff(l, g)
		want := oracleExceptions[p.name]
		if strings.Join(graphOnly, ",") != strings.Join(oracleSorted(want.graphOnly), ",") ||
			strings.Join(libraryOnly, ",") != strings.Join(oracleSorted(want.libraryOnly), ",") {
			t.Errorf("principal %s: graph and library rules disagree beyond the declared exception.\n  graph only   = %v (declared %v)\n  library only = %v (declared %v)\n  G = %v\n  L = %v\n  declared reason: %q",
				p.name, graphOnly, want.graphOnly, libraryOnly, want.libraryOnly, oracleKeys(g), oracleKeys(l), want.reason)
		}
	}

	// The relation must be non-trivial: the unrestricted caller sees every
	// linked issue on both paths (the pull request typed item, the missing
	// work item and the issues linked to a pull request of acme/other are
	// never members).
	open := results["unrestricted"]
	if len(open[0]) == 0 || len(open[1]) == 0 {
		t.Fatalf("unrestricted caller got G=%d L=%d members; the seed or the projection produced nothing", len(open[0]), len(open[1]))
	}
	for _, never := range []string{"gh:acme/svc#6", "linear:GHOST-1", "gh:acme/svc#7", "gh:acme/other#9", "linear:CHAOS-15"} {
		if open[0][never] || open[1][never] {
			t.Errorf("unrestricted: %s is a member (G=%t L=%t), but it has no link of record to an acme/svc pull request", never, open[0][never], open[1][never])
		}
	}
	// The explicit_text-only and heuristic-only repository-less issues are
	// members for the unrestricted caller and for NO narrowed caller, on both
	// paths: a link grants authority only when native.
	for _, id := range []string{"linear:CHAOS-12", "jira:PROJ-13"} {
		if !open[0][id] || !open[1][id] {
			t.Errorf("unrestricted: %s must be a member on both paths (G=%t L=%t)", id, open[0][id], open[1][id])
		}
		for _, name := range []string{"[acme/svc]", "[ACME/Svc]", "[acme/*]", "[acme/other]", "unrestricted+requested[acme/svc]"} {
			if results[name][0][id] || results[name][1][id] {
				t.Errorf("%s: repository-less %s admitted without a native link (G=%t L=%t)", name, id, results[name][0][id], results[name][1][id])
			}
		}
	}
	// The native repository-less issues are members for the narrowed callers
	// that are granted acme/svc, on both paths.
	for _, name := range []string{"[acme/svc]", "[ACME/Svc]", "[acme/*]"} {
		for _, id := range []string{"linear:CHAOS-10", "jira:PROJ-11"} {
			if !results[name][0][id] || !results[name][1][id] {
				t.Errorf("%s: native repository-less %s must be a member on both paths (G=%t L=%t)", name, id, results[name][0][id], results[name][1][id])
			}
		}
	}
	// E1 is gone: the project-owned, text-linked repository-less issue is a
	// member on BOTH paths for every caller granted acme/svc.
	for _, name := range []string{"[acme/svc]", "[ACME/Svc]", "[acme/*]"} {
		id := "linear:CHAOS-14"
		if !results[name][0][id] || !results[name][1][id] {
			t.Errorf("%s: project-owned %s must be a member on both paths (G=%t L=%t)", name, id, results[name][0][id], results[name][1][id])
		}
		if !results[name][0]["linear:CHAOS-16"] || results[name][1]["linear:CHAOS-16"] {
			t.Errorf("%s: PV (i) linear:CHAOS-16 must be graph-only (G=%t L=%t)", name, results[name][0]["linear:CHAOS-16"], results[name][1]["linear:CHAOS-16"])
		}
		if results[name][0]["linear:CHAOS-17"] || !results[name][1]["linear:CHAOS-17"] {
			t.Errorf("%s: PV (ii) linear:CHAOS-17 must be library-only (G=%t L=%t)", name, results[name][0]["linear:CHAOS-17"], results[name][1]["linear:CHAOS-17"])
		}
	}
}

func oracleKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
