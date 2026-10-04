package devhealthsource_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestIssuePullRequestLinkEdgesOnRealStores projects a small organization
// through the REAL producer (devhealthsource.ClickHouseProjectionSource) into
// a REAL FalkorDB from seeded production-typed ClickHouse rows, and reads the
// stored edges back through the real direct-edge query. It asserts exactly one
// LINKS_PULL_REQUEST edge per linked (issue, pull request) pair, carrying the
// link's tier and rank on the stored edge, for every issue id shape the
// writers produce and all three tiers, onto pull requests of a GitHub repo and
// a GitLab repo; and no edge for an unresolved work item, a pull-request-typed
// work item, a missing pull request node or an unknown provenance.
//
// Needs Docker (ClickHouse and FalkorDB containers). Written to be run by CI
// or by the lane owner; not run in the authoring sandbox.
func TestIssuePullRequestLinkEdgesOnRealStores(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	adapter := chaos7074FalkorAdapter(t, ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-90 * 24 * time.Hour)
	orgID := "11000000-0000-4000-8000-000000000001"
	repoGH, repoGL := o3UUID(orgID+"gh"), o3UUID(orgID+"gl")
	const zeroRepo = "00000000-0000-0000-0000-000000000000"
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	exec("repo gh", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoGH, orgID, "acme/widget", "github", now)
	exec("repo gl", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoGL, orgID, "group/proj", "gitlab", now)

	// The four issue id shapes the writers produce, plus the rows that must
	// not link.
	type issue struct{ id, repo, itemType, provider string }
	issues := []issue{
		{"linear:CHAOS-1", zeroRepo, "issue", "linear"},
		{"jira:PROJ-2", zeroRepo, "story", "jira"},
		{"gh:acme/widget#7", repoGH, "issue", "github"},
		{"gitlab:group/proj#9", repoGL, "issue", "gitlab"},
		{"gh:acme/widget#50", repoGH, "pr", "github"},
		{"gitlab:group/proj#51", repoGL, "merge_request", "gitlab"},
	}
	for _, i := range issues {
		exec("work item "+i.id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.id, i.repo, orgID, i.id, i.itemType, "open", i.provider, created, now, now)
	}
	type pull struct {
		repo   string
		number uint32
	}
	for _, p := range []pull{{repoGH, 42}, {repoGH, 44}, {repoGH, 47}, {repoGL, 43}, {repoGL, 45}, {repoGL, 46}} {
		exec(fmt.Sprintf("pull request %s#%d", p.repo, p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.repo, orgID, p.number, fmt.Sprintf("PR %d", p.number), "open", created, now)
	}

	type link struct {
		issue string
		pull  pull
		tier  string
	}
	linked := []link{
		{"linear:CHAOS-1", pull{repoGH, 42}, "native"},
		{"jira:PROJ-2", pull{repoGL, 43}, "explicit_text"},
		{"gh:acme/widget#7", pull{repoGH, 44}, "heuristic"},
		{"gitlab:group/proj#9", pull{repoGL, 45}, "native"},
		{"linear:CHAOS-1", pull{repoGL, 46}, "heuristic"},
		{"jira:PROJ-2", pull{repoGH, 47}, "explicit_text"},
	}
	skipped := []link{
		{"linear:GHOST-1", pull{repoGH, 42}, "native"},       // no work item
		{"gh:acme/widget#50", pull{repoGH, 42}, "native"},    // a pull request typed work item (pr)
		{"gitlab:group/proj#51", pull{repoGL, 43}, "native"}, // a pull request typed work item (merge_request)
		{"linear:CHAOS-1", pull{repoGH, 999}, "native"},      // no pull request node
		{"jira:PROJ-2", pull{repoGH, 42}, "guess"},           // a tier outside the closed vocabulary
		{"gh:acme/widget#7", pull{repoGL, 43}, "NOT_A_TIER"}, // a tier outside the closed vocabulary
	}
	for i, l := range append(append([]link{}, linked...), skipped...) {
		exec(fmt.Sprintf("link %d", i), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			l.pull.repo, l.issue, l.pull.number, float32(0.9), l.tier, "", now, orgID)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, source, adapter, orgID, devhealthsource.SourceName)

	principal := storage.Principal{OrgID: orgID, Subject: "link", CredentialID: "link"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("resolve graph binding: %v", err)
	}
	// Every pull request node, read in direction "in": the edges the graph
	// holds of this type, per pull request.
	var origins []contextfabric.SubjectRef
	for _, p := range []pull{{repoGH, 42}, {repoGH, 44}, {repoGH, 47}, {repoGL, 43}, {repoGL, 45}, {repoGL, 46}} {
		origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: fmt.Sprintf("pull_request:%s:%d", p.repo, p.number)})
	}
	page, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
		Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("direct edge page: %v", err)
	}
	if page.More {
		t.Fatal("more edges than the page held")
	}

	rank := map[string]string{"native": "3", "explicit_text": "2", "heuristic": "1"}
	got := map[string]string{}
	for _, edge := range page.Edges {
		key := edge.From.Subject.CanonicalID + " -> " + edge.To.Subject.CanonicalID
		if _, dup := got[key]; dup {
			t.Fatalf("edge %s projected twice", key)
		}
		got[key] = fmt.Sprint(edge.Attributes["property_link_provenance"]) + "/" + fmt.Sprint(edge.Attributes["property_link_provenance_rank"])
	}
	want := map[string]string{}
	for _, l := range linked {
		var issueRepo string
		for _, i := range issues {
			if i.id == l.issue {
				issueRepo = i.repo
			}
		}
		issueID, _, err := identity.Derive(identity.KindWorkItem, []string{issueRepo, l.issue}, nil)
		if err != nil {
			t.Fatal(err)
		}
		want[issueID+" -> "+fmt.Sprintf("pull_request:%s:%d", l.pull.repo, l.pull.number)] = l.tier + "/" + rank[l.tier]
	}
	if len(got) != len(want) {
		t.Errorf("LINKS_PULL_REQUEST edges = %d, want %d\n got  %v\n want %v", len(got), len(want), sortedPairs(got), sortedPairs(want))
	}
	for key, tier := range want {
		if got[key] != tier {
			t.Errorf("edge %s: tier/rank = %q, want %q", key, got[key], tier)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected edge %s (a skipped row projected)", key)
		}
	}
	// A skipped row's issue end must have no such edge: the pull request typed
	// work items are nodes (queryWorkItems projects them) but link to nothing.
	for _, id := range []string{"gh:acme/widget#50", "gitlab:group/proj#51"} {
		repo := repoGH
		if strings.HasPrefix(id, "gitlab") {
			repo = repoGL
		}
		canonical, _, _ := identity.Derive(identity.KindWorkItem, []string{repo, id}, nil)
		out, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
			Origins: []contextfabric.SubjectRef{{Kind: contextfabric.SubjectWorkItem, CanonicalID: canonical}},
			Types:   []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionOut, Limit: 10, ValidAt: now.Add(time.Minute),
		})
		if err != nil {
			t.Fatalf("direct edge page for %s: %v", id, err)
		}
		if len(out.Edges) != 0 {
			t.Errorf("pull request typed work item %s has %d LINKS_PULL_REQUEST edges", id, len(out.Edges))
		}
	}
}

// TestIssuePullRequestLinkProjectsOneEdgePerWorkItemNodeOfTheLinkedIssue pins
// the lead ruling on an issue with rows in several repositories. The
// work-item node identity of main is (repo_id, work_item_id): an issue with
// rows in several repositories has several nodes (queryWorkItems), so a link
// row, which names its issue by work_item_id alone, projects one edge per
// node; the older edge producers (dependencies, parent/child) behave the
// same, and the node identity is a separate ticket. Asserted: exactly one
// edge per (link row x work-item node of its issue), each carrying its link
// row's tier, and both ends of every edge resolve to a stored node (no
// dangling end). Distinct issues in an answer are the walks' job (#896/#899).
//
// Needs Docker (ClickHouse and FalkorDB containers). Written to be run by CI
// or by the lane owner; not run in the authoring sandbox.
func TestIssuePullRequestLinkProjectsOneEdgePerWorkItemNodeOfTheLinkedIssue(t *testing.T) {
	ctx := context.Background()
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("apply rendered schema statement: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)
	adapter := chaos7074FalkorAdapter(t, ctx)

	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-90 * 24 * time.Hour)
	orgID := "11000000-0000-4000-8000-000000000002"
	repoA, repoB := o3UUID(orgID+"a"), o3UUID(orgID+"b")
	const zeroRepo = "00000000-0000-0000-0000-000000000000"
	const issueID = "jira:DUP-1"
	exec := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}
	exec("repo a", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoA, orgID, "acme/alpha", "github", now)
	exec("repo b", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoB, orgID, "group/beta", "gitlab", now)
	// One issue id with rows under two repositories: two work-item nodes.
	issueRepos := []string{zeroRepo, repoA}
	for _, repo := range issueRepos {
		exec("work item under "+repo, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			issueID, repo, orgID, issueID, "story", "open", "jira", created, now, now)
	}
	type pull struct {
		repo   string
		number uint32
		tier   string
	}
	pulls := []pull{{repoA, 1, "native"}, {repoB, 2, "heuristic"}}
	for _, p := range pulls {
		exec(fmt.Sprintf("pull request %s#%d", p.repo, p.number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			p.repo, orgID, p.number, fmt.Sprintf("PR %d", p.number), "open", created, now)
		exec(fmt.Sprintf("link to %s#%d", p.repo, p.number), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.repo, issueID, p.number, float32(0.9), p.tier, "", now, orgID)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	drainSource(t, ctx, source, adapter, orgID, devhealthsource.SourceName)

	principal := storage.Principal{OrgID: orgID, Subject: "link", CredentialID: "link"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatalf("resolve graph binding: %v", err)
	}
	var origins []contextfabric.SubjectRef
	want := map[string]string{}
	for _, p := range pulls {
		pullID := fmt.Sprintf("pull_request:%s:%d", p.repo, p.number)
		origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: pullID})
		for _, repo := range issueRepos {
			node, _, err := identity.Derive(identity.KindWorkItem, []string{repo, issueID}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want[node+" -> "+pullID] = p.tier
		}
	}
	page, err := adapter.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{
		Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 100, ValidAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("direct edge page: %v", err)
	}
	if page.More {
		t.Fatal("more edges than the page held")
	}
	got := map[string]string{}
	var ends []contextfabric.SubjectRef
	for _, edge := range page.Edges {
		key := edge.From.Subject.CanonicalID + " -> " + edge.To.Subject.CanonicalID
		if _, dup := got[key]; dup {
			t.Fatalf("edge %s projected twice", key)
		}
		got[key] = fmt.Sprint(edge.Attributes["property_link_provenance"])
		ends = append(ends, edge.From.Subject, edge.To.Subject)
	}
	if len(got) != len(want) {
		t.Errorf("LINKS_PULL_REQUEST edges = %d, want %d (one per link row x work-item node)\n got  %v\n want %v", len(got), len(want), sortedPairs(got), sortedPairs(want))
	}
	for key, tier := range want {
		if got[key] != tier {
			t.Errorf("edge %s: tier = %q, want %q (the link row's tier on every node's edge)", key, got[key], tier)
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("unexpected edge %s", key)
		}
	}
	// No dangling end: every end of every edge is a node the node producers
	// made from the same store.
	nodes, err := adapter.ReadSubjectNodes(ctx, principal, binding, ends)
	if err != nil {
		t.Fatalf("read subject nodes: %v", err)
	}
	stored := map[string]bool{}
	for _, n := range nodes {
		stored[n.Kind+"|"+n.CanonicalID] = true
	}
	for _, end := range ends {
		if !stored[string(end.Kind)+"|"+end.CanonicalID] {
			t.Errorf("edge end %s %s has no stored node", end.Kind, end.CanonicalID)
		}
	}
	if len(ends) == 0 {
		t.Fatal("no edge ends to resolve: the dangling-end check measured nothing")
	}
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+" = "+v)
	}
	sort.Strings(out)
	return out
}
