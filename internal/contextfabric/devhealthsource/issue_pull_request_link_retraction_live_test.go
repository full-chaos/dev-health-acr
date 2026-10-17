package devhealthsource_test

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Explicit_text and heuristic links are a fallback only: a pull request or an
// issue with a native link gets no lower-tier link (the entity-tree rule). Ops
// deletes the outranked rows of work_graph_issue_pr, so no row names their
// already-projected edges again. These cases drive the real producer from
// seeded ClickHouse rows into a real FalkorDB through the held cursor and read
// the stored LINKS_PULL_REQUEST edges back, with their tier, the way the
// direct relationship read does.
//
// Needs Docker (ClickHouse and FalkorDB containers).

type linkWorld struct {
	*edgeWorld
	emitted []string
}

func (w *linkWorld) pull(repo string, number uint32) string {
	w.exec(fmt.Sprintf("pull request %d", number), `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, created_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		repo, w.orgID, number, fmt.Sprintf("PR %d", number), "open", w.now.Add(-30*24*time.Hour), w.now)
	return fmt.Sprintf("pull_request:%s:%d", repo, number)
}

func (w *linkWorld) link(repo, issue string, number uint32, tier string, synced time.Time) {
	w.exec(fmt.Sprintf("link %s %d %s", issue, number, tier), `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repo, issue, number, float32(0.9), tier, "", synced, w.orgID)
}

// retract deletes stored rows the way the ops build's fallback retraction does
// (ALTER TABLE ... DELETE, mutations_sync = 2).
func (w *linkWorld) retract(repo, issue string, number uint32) {
	w.exec("retract "+issue, `ALTER TABLE work_graph_issue_pr DELETE WHERE org_id = ? AND repo_id = ? AND work_item_id = ? AND pr_number = ? SETTINGS mutations_sync = 2`,
		w.orgID, repo, issue, number)
}

// drain is edgeWorld.drain that also records every LINKS_PULL_REQUEST edge the
// producer emits, as "issue -> pull request = tier".
func (w *linkWorld) drain() {
	w.t.Helper()
	replays := map[string]bool{}
	for page := 0; page < 200; page++ {
		batch, ok, err := w.source.NextProjectionBatch(w.ctx, contextfabric.ProjectionCheckpoint{OrgID: w.orgID, Source: devhealthsource.SourceName, Cursor: w.cursor})
		if err != nil {
			w.t.Fatalf("page %d: %v", page, err)
		}
		if !ok {
			return
		}
		for _, r := range batch.Relationships {
			if r.Type == "LINKS_PULL_REQUEST" {
				w.emitted = append(w.emitted, r.From.CanonicalID+" -> "+r.To.CanonicalID+" = "+*r.Properties[devhealthsource.IssuePullRequestLinkTierProperty].String)
			}
		}
		if _, err := w.adapter.ApplyProjectionBatch(w.ctx, batch); err != nil {
			w.t.Fatalf("apply page %d: %v", page, err)
		}
		requireCursorProgress(w.t, fmt.Sprintf("page %d", page), w.cursor, batch, replays)
		w.cursor = batch.NextCursor
	}
	w.t.Fatal("did not drain")
}

// stored is every LINKS_PULL_REQUEST edge into the pull requests, as
// "issue -> pull request = tier", read with the direct edge query.
func (w *linkWorld) stored(pulls ...string) []string {
	w.t.Helper()
	principal := storage.Principal{OrgID: w.orgID, Subject: "link-world", CredentialID: "link-world"}
	binding, err := w.adapter.ResolveInvestigationBinding(w.ctx, principal)
	if err != nil {
		w.t.Fatalf("resolve graph binding: %v", err)
	}
	var origins []contextfabric.SubjectRef
	for _, p := range pulls {
		origins = append(origins, contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: p})
	}
	page, err := w.adapter.DirectEdgePage(w.ctx, principal, binding, directread.EdgePageQuery{
		Origins: origins, Types: []string{"LINKS_PULL_REQUEST"}, Direction: directread.EdgeDirectionIn, Limit: 50, ValidAt: w.now.Add(time.Minute),
	})
	if err != nil {
		w.t.Fatalf("direct edge page: %v", err)
	}
	if page.More {
		w.t.Fatal("more edges than the page held")
	}
	var out []string
	for _, e := range page.Edges {
		out = append(out, e.From.Subject.CanonicalID+" -> "+e.To.Subject.CanonicalID+" = "+fmt.Sprint(e.Attributes["property_link_provenance"]))
	}
	sort.Strings(out)
	return out
}

func newLinkWorld(t *testing.T, adapter *falkorgraph.Adapter, orgID, repo string) *linkWorld {
	w := &linkWorld{edgeWorld: newEdgeWorld(t, adapter, orgID)}
	w.repo(repo, "acme/links")
	return w
}

// (a) A lower-tier link is projected; a native link of its pull request (and,
// separately, of its issue) arrives later and ops deletes the outranked row.
// After the pass only the native edges are stored; a pull request with only
// lower-tier links keeps them (c), and every stored edge names its tier (d).
func nativeLinkArrivingLaterRetractsTheOutrankedEdges(t *testing.T, adapter *falkorgraph.Adapter) {
	const org, repo = "98780000-0000-4000-8000-000000000001", "98780000-0000-4000-8000-0000000000a1"
	w := newLinkWorld(t, adapter, org, repo)
	first := w.now.Add(-5 * time.Hour)
	for _, id := range []string{"jira:A-1", "jira:B-1", "jira:C-1", "jira:D-1"} {
		w.item(repo, id, "", first)
	}
	p, q, r := w.pull(repo, 1), w.pull(repo, 2), w.pull(repo, 3)
	w.link(repo, "jira:B-1", 1, "heuristic", first)
	w.link(repo, "jira:A-1", 2, "explicit_text", first)
	w.link(repo, "jira:C-1", 3, "explicit_text", first)
	w.link(repo, "jira:D-1", 3, "heuristic", first)
	w.drain()
	a, b, c, d := w.workItemID(repo, "jira:A-1"), w.workItemID(repo, "jira:B-1"), w.workItemID(repo, "jira:C-1"), w.workItemID(repo, "jira:D-1")
	if got, want := w.stored(p, q, r), sortedStrings(b+" -> "+p+" = heuristic", a+" -> "+q+" = explicit_text", c+" -> "+r+" = explicit_text", d+" -> "+r+" = heuristic"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("before the native link: %v, want %v", got, want)
	}

	later := time.Now().UTC().Add(time.Second)
	w.link(repo, "jira:A-1", 1, "native", later)
	w.retract(repo, "jira:B-1", 1)
	w.retract(repo, "jira:A-1", 2)
	w.drain()
	if got, want := w.stored(p, q, r), sortedStrings(a+" -> "+p+" = native", c+" -> "+r+" = explicit_text", d+" -> "+r+" = heuristic"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after the native link A -> P: %v, want %v: B -> P shares the pull request and A -> Q the issue, both outranked; R has no native link and keeps its lower-tier links", got, want)
	}
}

// (b) A native and a lower-tier link of the same pull request (and of the same
// issue) in one pass: the producer writes only the native edge.
func nativeAndLowerTierInOnePassWriteOnlyTheNative(t *testing.T, adapter *falkorgraph.Adapter) {
	const org, repo = "98780000-0000-4000-8000-000000000002", "98780000-0000-4000-8000-0000000000b1"
	w := newLinkWorld(t, adapter, org, repo)
	synced := w.now.Add(-time.Hour)
	for _, id := range []string{"linear:A-2", "linear:B-2", "linear:C-2"} {
		w.item(repo, id, "", synced)
	}
	p, q, r := w.pull(repo, 11), w.pull(repo, 12), w.pull(repo, 13)
	w.link(repo, "linear:A-2", 11, "native", synced)
	w.link(repo, "linear:B-2", 11, "heuristic", synced.Add(time.Minute))      // same pull request, written after the native row
	w.link(repo, "linear:A-2", 12, "explicit_text", synced.Add(-time.Minute)) // same issue, written before it
	w.link(repo, "linear:C-2", 13, "heuristic", synced)
	w.drain()
	a, c := w.workItemID(repo, "linear:A-2"), w.workItemID(repo, "linear:C-2")
	want := sortedStrings(a+" -> "+p+" = native", c+" -> "+r+" = heuristic")
	if got := sortedStrings(w.emitted...); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("the producer emitted %v, want only %v", got, want)
	}
	if got := w.stored(p, q, r); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("stored %v, want %v", got, want)
	}
}

// A native link the producer cannot project still covers its ends: one whose
// pull request node is missing covers its issue, and one whose issue is not a
// work item (or is a pull request item) covers its pull request. The
// lower-tier edges of those ends projected before it are retracted.
func nativeLinkThatIsNotProjectedStillRetracts(t *testing.T, adapter *falkorgraph.Adapter) {
	const org, repo = "98780000-0000-4000-8000-000000000003", "98780000-0000-4000-8000-0000000000c1"
	w := newLinkWorld(t, adapter, org, repo)
	first := w.now.Add(-5 * time.Hour)
	for _, id := range []string{"jira:A-3", "jira:B-3", "jira:C-3", "jira:D-3"} {
		w.item(repo, id, "", first)
	}
	w.exec("pull request work item", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, parent_id, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"gh:acme/links#9", repo, org, "a pull request item", "pr", "open", "", "", "github", first, first, first)
	p, q, r := w.pull(repo, 21), w.pull(repo, 22), w.pull(repo, 23)
	w.link(repo, "jira:A-3", 21, "heuristic", first)
	w.link(repo, "jira:B-3", 22, "explicit_text", first)
	w.link(repo, "jira:C-3", 23, "heuristic", first)
	w.link(repo, "jira:D-3", 23, "heuristic", first)
	w.drain()
	a, b, c, d := w.workItemID(repo, "jira:A-3"), w.workItemID(repo, "jira:B-3"), w.workItemID(repo, "jira:C-3"), w.workItemID(repo, "jira:D-3")
	if got, want := w.stored(p, q, r), sortedStrings(a+" -> "+p+" = heuristic", b+" -> "+q+" = explicit_text", c+" -> "+r+" = heuristic", d+" -> "+r+" = heuristic"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("before the native links: %v, want %v", got, want)
	}

	later := time.Now().UTC().Add(time.Second)
	w.link(repo, "jira:A-3", 99, "native", later)        // no pull request #99: covers A-3
	w.link(repo, "jira:GHOST-3", 22, "native", later)    // no such work item: covers #22
	w.link(repo, "gh:acme/links#9", 23, "native", later) // a pull request item: covers #23
	w.retract(repo, "jira:A-3", 21)
	w.retract(repo, "jira:B-3", 22)
	w.retract(repo, "jira:C-3", 23)
	w.retract(repo, "jira:D-3", 23)
	w.drain()
	if got := w.stored(p, q, r); len(got) != 0 {
		t.Fatalf("after native links the producer cannot project: %v, want none (each lower-tier edge has an end a native link covers)", got)
	}
}

func sortedStrings(values ...string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

// The cases share one FalkorDB container and the package's shared ClickHouse
// (each case has its own organization).
func TestOutrankedIssuePullRequestLinksOnRealStores(t *testing.T) {
	adapter := chaos7074FalkorAdapter(t, t.Context())
	t.Run("a native link arriving later", func(t *testing.T) { nativeLinkArrivingLaterRetractsTheOutrankedEdges(t, adapter) })
	t.Run("native and lower tier in one pass", func(t *testing.T) { nativeAndLowerTierInOnePassWriteOnlyTheNative(t, adapter) })
	t.Run("a native link that is not projected", func(t *testing.T) { nativeLinkThatIsNotProjectedStillRetracts(t, adapter) })
}
