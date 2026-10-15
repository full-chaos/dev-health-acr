package devhealthsource_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// A work item has one parent, and a blocks link the provider stopped
// reporting is not a link. These tests drive the real producer from seeded
// ClickHouse rows into a real FalkorDB and count the edges the graph still
// holds, by a caller that sees everything. A row whose parent changes mints a
// new edge id, and nothing retracts the edge of the old parent.
//
// Needs Docker (ClickHouse and FalkorDB containers).

type edgeWorld struct {
	t       *testing.T
	ctx     context.Context
	direct  clickhousedriver.Conn
	adapter *falkorgraph.Adapter
	source  contextfabric.ProjectionSource
	orgID   string
	cursor  string
	now     time.Time
}

func newEdgeWorld(t *testing.T, adapter *falkorgraph.Adapter, orgID string) *edgeWorld {
	t.Helper()
	query, direct := orgIsolationClickHouseFixture(t)
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	return &edgeWorld{t: t, ctx: context.Background(), direct: direct, adapter: adapter, source: source, orgID: orgID, now: time.Now().UTC().Truncate(time.Second)}
}

func (w *edgeWorld) exec(label, statement string, args ...any) {
	w.t.Helper()
	if err := w.direct.Exec(w.ctx, statement, args...); err != nil {
		w.t.Fatalf("seed %s: %v", label, err)
	}
}

func (w *edgeWorld) repo(id, name string) {
	w.exec("repo "+name, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, w.orgID, name, "jira", w.now)
}

// item writes one version of a work item; work_items is replaced by last_synced.
func (w *edgeWorld) item(repo, id, parent string, synced time.Time) {
	w.exec("work item "+id, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, url, parent_id, provider, created_at, updated_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, repo, w.orgID, "issue "+id, "issue", "open", "", parent, "jira", w.now.Add(-30*24*time.Hour), synced, synced)
}

// drain projects everything past the held cursor into the graph and keeps the cursor.
func (w *edgeWorld) drain() {
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
		if _, err := w.adapter.ApplyProjectionBatch(w.ctx, batch); err != nil {
			w.t.Fatalf("apply page %d: %v", page, err)
		}
		requireCursorProgress(w.t, fmt.Sprintf("page %d", page), w.cursor, batch, replays)
		w.cursor = batch.NextCursor
	}
	w.t.Fatal("did not drain")
}

func (w *edgeWorld) workItemID(repo, id string) string {
	w.t.Helper()
	out, _, err := identity.Derive(identity.KindWorkItem, []string{repo, id}, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// edgesOut is the edges of one type that leave a work item, as "from -> to" in
// the graph, every one the graph holds and the window admits at the instant.
func (w *edgeWorld) edgesOut(origin string, relation contractsv1.ContextFabricRelationshipType) []string {
	w.t.Helper()
	principal := storage.Principal{OrgID: w.orgID, Subject: "edge-world", CredentialID: "edge-world"}
	binding, err := w.adapter.ResolveInvestigationBinding(w.ctx, principal)
	if err != nil {
		w.t.Fatalf("resolve graph binding: %v", err)
	}
	page, err := w.adapter.DirectEdgePage(w.ctx, principal, binding, directread.EdgePageQuery{
		Origins:   []contextfabric.SubjectRef{{Kind: contextfabric.SubjectWorkItem, CanonicalID: origin}},
		Types:     []string{graphrank.NormalizeRelation(string(relation))},
		Direction: directread.EdgeDirectionOut, Limit: 50, ValidAt: w.now.Add(time.Minute), Current: true,
	})
	if err != nil {
		w.t.Fatalf("direct edge page: %v", err)
	}
	var out []string
	for _, e := range page.Edges {
		out = append(out, e.From.Subject.CanonicalID+" -> "+e.To.Subject.CanonicalID)
	}
	sort.Strings(out)
	return out
}

func partOfReparentLeavesOneOpenEdge(t *testing.T, adapter *falkorgraph.Adapter) {
	const org = "97950000-0000-4000-8000-000000000001"
	w := newEdgeWorld(t, adapter, org)
	const repo = "97950000-0000-4000-8000-0000000000a1"
	w.repo(repo, "acme/tree")
	first, second := w.now.Add(-5*time.Hour), time.Now().UTC().Add(time.Second)
	w.item(repo, "jira:PAR-1", "", first)
	w.item(repo, "jira:PAR-2", "", first)
	w.item(repo, "jira:CHD-1", "jira:PAR-1", first)
	w.drain()
	child := w.workItemID(repo, "jira:CHD-1")
	oldParent, newParent := w.workItemID(repo, "jira:PAR-1"), w.workItemID(repo, "jira:PAR-2")
	if got := w.edgesOut(child, contractsv1.ContextFabricRelationshipPartOf); fmt.Sprint(got) != fmt.Sprint([]string{child + " -> " + oldParent}) {
		t.Fatalf("before the change: PART_OF edges %v, want the edge to the first parent", got)
	}

	t.Run("the parent changes", func(t *testing.T) {
		w.item(repo, "jira:CHD-1", "jira:PAR-2", second)
		w.drain()
		got := w.edgesOut(child, contractsv1.ContextFabricRelationshipPartOf)
		if fmt.Sprint(got) != fmt.Sprint([]string{child + " -> " + newParent}) {
			t.Fatalf("after the re-parent the graph holds %d open PART_OF edges from the child: %v, want only the edge to the new parent", len(got), got)
		}
	})

	t.Run("the parent is cleared", func(t *testing.T) {
		w.item(repo, "jira:CHD-1", "", time.Now().UTC().Add(2*time.Second))
		w.drain()
		if got := w.edgesOut(child, contractsv1.ContextFabricRelationshipPartOf); len(got) != 0 {
			t.Fatalf("the parent was cleared and the graph holds %d open PART_OF edges from the child: %v, want none", len(got), got)
		}
	})
}

// A qualified parent id (a known integration prefix and a key, parent_key.go)
// names its parent org-wide: one holder is the parent, several are no parent,
// and an empty key is no parent. Any other id is a per-container number: its
// parent is the work item of that id in the child's own repository, and
// nothing else.
func partOfParentResolution(t *testing.T, adapter *falkorgraph.Adapter) {
	const org = "97950000-0000-4000-8000-000000000002"
	w := newEdgeWorld(t, adapter, org)
	var logs bytes.Buffer
	w.source.(*devhealthsource.ClickHouseProjectionSource).WithLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	const repoA, repoB, repoC = "97950000-0000-4000-8000-0000000000b1", "97950000-0000-4000-8000-0000000000b2", "97950000-0000-4000-8000-0000000000b3"
	w.repo(repoA, "acme/a")
	w.repo(repoB, "acme/b")
	w.repo(repoC, "acme/c")
	synced := w.now.Add(-time.Hour)
	// Qualified: SOLO-1 is held once (repository B), SHARED-1 under A and B.
	w.item(repoB, "jira:SOLO-1", "", synced)
	w.item(repoA, "jira:SHARED-1", "", synced)
	w.item(repoB, "jira:SHARED-1", "", synced)
	w.item(repoC, "jira:QONCE", "jira:SOLO-1", synced)
	w.item(repoC, "jira:QMANY", "jira:SHARED-1", synced)
	w.item(repoC, "jira:QEMPTY", "jira:", synced)
	w.item(repoA, "jira:QEMPTY2", "jira:", synced)
	w.item(repoC, "jira:QNONE", "jira:MISSING-1", synced)
	// Per-container numbers: 7 is held in B only, 8 in A and B.
	w.item(repoB, "7", "", synced)
	w.item(repoA, "8", "", synced)
	w.item(repoB, "8", "", synced)
	w.item(repoC, "NFOREIGN", "7", synced)
	w.item(repoA, "NOWN", "8", synced)
	w.item(repoC, "NLOST", "8", synced)
	// An integration prefix the map does not know takes the own-repository rule.
	w.item(repoA, "ghx:1", "", synced)
	w.item(repoC, "UNMAPPED", "ghx:1", synced)
	w.drain()

	edges := func(repo, id string) []string {
		return w.edgesOut(w.workItemID(repo, id), contractsv1.ContextFabricRelationshipPartOf)
	}
	edgeTo := func(childRepo, child, parentRepo, parent string) []string {
		return []string{w.workItemID(childRepo, child) + " -> " + w.workItemID(parentRepo, parent)}
	}
	for _, c := range []struct {
		name             string
		childRepo, child string
		want             []string
	}{
		{"a qualified key held once, in another repository, is the parent", repoC, "jira:QONCE", edgeTo(repoC, "jira:QONCE", repoB, "jira:SOLO-1")},
		{"a qualified key held under two repositories names no parent", repoC, "jira:QMANY", nil},
		{"a qualified prefix with an empty key names no parent", repoC, "jira:QEMPTY", nil},
		{"a second child with the same empty key names no parent either", repoA, "jira:QEMPTY2", nil},
		{"a qualified key held nowhere names no parent", repoC, "jira:QNONE", nil},
		{"a number held under two repositories, one the child's own, is the own one", repoA, "NOWN", edgeTo(repoA, "NOWN", repoA, "8")},
		{"a number held only in another repository names no parent", repoC, "NFOREIGN", nil},
		{"a number held under two repositories, neither the child's own, names no parent", repoC, "NLOST", nil},
		{"an unmapped prefix held only in another repository names no parent", repoC, "UNMAPPED", nil},
	} {
		if got := edges(c.childRepo, c.child); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: PART_OF edges %v, want %v", c.name, got, c.want)
		}
	}

	line := logs.String()
	for _, want := range []string{
		"part_of_parent_key_not_unique", "part_of_parent_empty_key", "part_of_parent_not_in_own_repository",
		"part_of parent id prefix is not mapped", "prefix=ghx:",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the log does not say %q: %q", want, line)
		}
	}
	for _, secret := range []string{repoA, repoB, repoC, "acme/a", "acme/b", "acme/c", "SOLO-1", "SHARED-1", "MISSING-1"} {
		if strings.Contains(line, secret) {
			t.Errorf("the log names %q, which belongs to a repository the child cannot read: %q", secret, line)
		}
	}
}

// A blocks link the provider stopped reporting is retracted by tombstone, by
// the id the asserting row minted; the graph must hold no BLOCKS edge after.
func removedBlockerLinkLeavesNoOpenEdgeInTheGraph(t *testing.T, adapter *falkorgraph.Adapter) {
	const org = "97950000-0000-4000-8000-000000000003"
	w := newEdgeWorld(t, adapter, org)
	const repo = "97950000-0000-4000-8000-0000000000c1"
	w.repo(repo, "acme/blocks")
	linked, resynced := w.now.Add(-5*time.Hour), time.Now().UTC().Add(time.Second)
	w.item(repo, "jira:BLK-1", "", linked)
	w.item(repo, "jira:BLK-2", "", linked)
	w.exec("blocks row", `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, org_id, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
		"jira:BLK-1", "jira:BLK-2", "blocks", "blocks", org, linked)
	w.drain()
	blocker := w.workItemID(repo, "jira:BLK-1")
	if got := w.edgesOut(blocker, contractsv1.ContextFabricRelationshipBlocks); len(got) != 1 {
		t.Fatalf("before the removal: %d BLOCKS edges, want 1: %v", len(got), got)
	}
	w.item(repo, "jira:BLK-1", "", resynced)
	w.item(repo, "jira:BLK-2", "", resynced)
	w.drain()
	if got := w.edgesOut(blocker, contractsv1.ContextFabricRelationshipBlocks); len(got) != 0 {
		t.Fatalf("the provider stopped reporting the link and the graph holds %d open BLOCKS edges: %v", len(got), got)
	}
}

// The three cases share one FalkorDB container and the package's shared
// ClickHouse (each case has its own organization), to stay inside the package
// time budget.
func TestEdgeRetractionOnRealStores(t *testing.T) {
	adapter := chaos7074FalkorAdapter(t, context.Background())
	t.Run("a re-parent and a cleared parent", func(t *testing.T) { partOfReparentLeavesOneOpenEdge(t, adapter) })
	t.Run("parent id resolution", func(t *testing.T) { partOfParentResolution(t, adapter) })
	t.Run("a removed blocks link", func(t *testing.T) { removedBlockerLinkLeavesNoOpenEdgeInTheGraph(t, adapter) })
}
