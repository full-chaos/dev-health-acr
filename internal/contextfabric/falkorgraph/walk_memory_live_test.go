package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
)

// The memory one repository walk takes, on a link population several times the
// largest seen in production (about 1200 pull requests and 750 linked issues on
// one repository), with production-wide nodes: rows written to ClickHouse,
// projected by the REAL producer into a REAL FalkorDB, every issue and pull
// request carrying a long title and an embedding of the production width (3072
// numbers, the property the vector projection writes). Needs Docker; run by CI.

// devhealthschema:not-a-production-replica -- this list only picks which declared tables to render through devhealthschema.DDL; it declares no column.
var walkMemoryTables = []string{
	"repos", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"ci_pipeline_runs", "deployments", "operational_incidents",
	"operational_service_repository_mappings", "work_item_dependencies",
	"work_graph_deployment_incident_edges", "work_graph_issue_pr",
}

const (
	// walkMemoryLinks: issues, each natively linked to its own pull request of
	// the one repository.
	walkMemoryLinks = 3000
	// walkMemoryDimension is the production embedding width
	// (calibratedIdentityText3Large, #d3072).
	walkMemoryDimension = 3072
	// walkMemoryBound is the allocation one walk request may make on this
	// population: an eighth of the 512 MiB the API pod is given.
	walkMemoryBound = 64 << 20
)

func walkMemoryUUID(label string) string {
	sum := sha256.Sum256([]byte("walk-memory:" + label))
	h := hex.EncodeToString(sum[:16])
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-8" + h[17:20] + "-" + h[20:32]
}

func walkMemoryClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
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
	direct, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{address}, Auth: clickhouse.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
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
	for _, statement := range devhealthschema.DDL(walkMemoryTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	return query, direct
}

// walkMemorySeed writes one repository with walkMemoryLinks pull requests and
// as many issues, each issue natively linked to its pull request, and two
// smaller repositories with all three tiers and repository-less issues for the
// differential rows; projects them with the real producer; then writes an
// embedding of the production width on every issue and pull request node.
func walkMemorySeed(t *testing.T, ctx context.Context) (*Adapter, string, string) {
	t.Helper()
	query, direct := walkMemoryClickHouse(t, ctx)
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := walkMemoryUUID("org")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC().Truncate(time.Millisecond)
	created := now.Add(-40 * 24 * time.Hour)
	longText := strings.Repeat("A sentence of the kind an issue title or a pull request description carries in production. ", 6)
	repos := map[string]string{"acme/big": walkMemoryUUID("acme/big"), "acme/svc": walkMemoryUUID("acme/svc"), "acme/other": walkMemoryUUID("acme/other")}
	for slug, id := range repos {
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, id, orgID, slug, "github", now); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
	}
	pulls, err := direct.PrepareBatch(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, body, state, created_at, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	items, err := direct.PrepareBatch(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, type, status, provider, project_id, created_at, updated_at, last_synced)`)
	if err != nil {
		t.Fatal(err)
	}
	links, err := direct.PrepareBatch(ctx, `INSERT INTO work_graph_issue_pr (repo_id, work_item_id, pr_number, confidence, provenance, evidence, last_synced, org_id)`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= walkMemoryLinks; i++ {
		title, body := fmt.Sprintf("Change %d: %s", i, longText), longText+longText
		if err := pulls.Append(repos["acme/big"], orgID, uint32(i), &title, &body, ptr("merged"), created, now); err != nil {
			t.Fatal(err)
		}
		issue := fmt.Sprintf("gh:acme/big#%d", 100000+i)
		if err := items.Append(issue, repos["acme/big"], orgID, fmt.Sprintf("Issue %d: %s", i, longText), "issue", "open", "github", "", created, now, now); err != nil {
			t.Fatal(err)
		}
		if err := links.Append(repos["acme/big"], issue, uint32(i), float32(0.9), "native", "", now, orgID); err != nil {
			t.Fatal(err)
		}
	}
	// The differential rows: every tier, repository-less issues, an issue of
	// another repository, links into two repositories.
	small := []struct {
		issue, issueRepo, provider, slug string
		number                           uint32
		tier                             string
	}{
		{"linear:ENG-1", "", "linear", "acme/svc", 1, "native"},
		{"linear:ENG-2", "", "linear", "acme/svc", 1, "explicit_text"},
		{"jira:OPS-3", "", "jira", "acme/svc", 2, "heuristic"},
		{"gh:acme/svc#4", "acme/svc", "github", "acme/svc", 2, "heuristic"},
		{"gh:acme/other#5", "acme/other", "github", "acme/svc", 1, "explicit_text"},
		{"gh:acme/other#6", "acme/other", "github", "acme/other", 1, "native"},
		{"linear:ENG-7", "", "linear", "acme/other", 1, "native"},
	}
	for _, p := range []struct {
		slug   string
		number uint32
	}{{"acme/svc", 1}, {"acme/svc", 2}, {"acme/other", 1}} {
		title := "Small change"
		if err := pulls.Append(repos[p.slug], orgID, p.number, &title, &title, ptr("open"), created, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range small {
		repoID := "00000000-0000-0000-0000-000000000000"
		if l.issueRepo != "" {
			repoID = repos[l.issueRepo]
		}
		if err := items.Append(l.issue, repoID, orgID, "Small "+l.issue, "issue", "open", l.provider, "", created, now, now); err != nil {
			t.Fatal(err)
		}
		if err := links.Append(repos[l.slug], l.issue, l.number, float32(0.9), l.tier, "", now, orgID); err != nil {
			t.Fatal(err)
		}
	}
	for name, batch := range map[string]clickhousedriver.Batch{"pull requests": pulls, "work items": items, "links": links} {
		if err := batch.Send(); err != nil {
			t.Fatalf("send %s: %v", name, err)
		}
	}
	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	cursor := ""
	for page := 0; ; page++ {
		if page > 2000 {
			t.Fatal("projection did not drain")
		}
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName, Cursor: cursor})
		if err != nil {
			t.Fatalf("projection page %d: %v", page, err)
		}
		if !available {
			break
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("apply page %d: %v", page, err)
		}
		if batch.NextCursor == cursor {
			t.Fatalf("projection page %d made no cursor progress", page)
		}
		cursor = batch.NextCursor
	}
	binding, err := adapter.ResolveInvestigationBinding(ctx, storage.Principal{OrgID: orgID})
	if err != nil {
		t.Fatal(err)
	}
	key, err := adapter.effectiveKey(ctx, orgID, binding)
	if err != nil {
		t.Fatal(err)
	}
	// The embedding the vector projection writes (writeNodeVector:
	// SET n.embedding = vecf32($vec)), on every issue and pull request.
	vector := make([]interface{}, walkMemoryDimension)
	for i := range vector {
		vector[i] = float64(i%97) / 97
	}
	for _, kind := range []string{"work_item", "pull_request"} {
		rows, err := adapter.api.query(ctx, key, fmt.Sprintf("MATCH (n:%s {%s:$org, %s:$kind}) RETURN n.%s AS id", labelSubject, propOrgID, propKind, propCanonicalID), map[string]interface{}{"org": orgID, "kind": kind}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) < walkMemoryLinks {
			t.Fatalf("%d %s nodes projected, want at least %d: the graph is not the seed", len(rows), kind, walkMemoryLinks)
		}
		for start := 0; start < len(rows); start += 50 {
			ids := []interface{}{}
			for _, r := range rows[start:min(start+50, len(rows))] {
				ids = append(ids, r["id"])
			}
			cypher := fmt.Sprintf("UNWIND $ids AS id MATCH (n:%s {%s:$org, %s:$kind, %s:id}) SET n.%s = vecf32($vec)", labelSubject, propOrgID, propKind, propCanonicalID, propEmbedding)
			if _, err := adapter.api.query(ctx, key, cypher, map[string]interface{}{"org": orgID, "kind": kind, "ids": ids, "vec": vector}, false); err != nil {
				t.Fatalf("write embeddings: %v", err)
			}
		}
	}
	return adapter, key, orgID
}

func ptr(s string) *string { return &s }

// walkAllocation runs fn alone and reports the bytes it allocated and the
// highest live heap seen while it ran, above the heap before it.
func walkAllocation(fn func()) (allocated, peak uint64) {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var high atomic.Uint64
	high.Store(before.HeapAlloc)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var m runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-time.After(2 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > high.Load() {
					high.Store(m.HeapAlloc)
				}
			}
		}
	}()
	fn()
	close(done)
	wg.Wait()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if after.HeapAlloc > high.Load() {
		high.Store(after.HeapAlloc)
	}
	return after.TotalAlloc - before.TotalAlloc, high.Load() - before.HeapAlloc
}

// TestOneRepositoryWalkStaysSmallOnAWideLinkPopulation: one walk of the
// repository with the largest link population allocates a bounded amount,
// far under the API pod's memory, and returns the same members and census as
// the whole-node read. On the code before the change the walk read every
// property of every linked issue and pull request (embedding included), and
// the bound fails.
func TestOneRepositoryWalkStaysSmallOnAWideLinkPopulation(t *testing.T) {
	ctx := context.Background()
	adapter, key, orgID := walkMemorySeed(t, ctx)
	t.Run("the walk reads decide as the whole-node reads", func(t *testing.T) {
		walkReadsDecideAsWholeNodeReads(t, ctx, adapter, key, orgID)
	})
	principal := storage.Principal{OrgID: orgID, Subject: "u", CredentialID: "c"}
	binding, err := adapter.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + walkMemoryUUID("acme/big"), Label: "acme/big"}
	var walk contextfabric.TreeWorkItemWalk
	var walkErr error
	allocated, peak := walkAllocation(func() {
		walk, walkErr = adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{}, anchor, contextfabric.WorkItemMembershipCensusLimit+1)
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	t.Logf("one repository walk over %d links: allocated %d MiB, peak live heap +%d MiB, members %d, truncated %t", walkMemoryLinks, allocated>>20, peak>>20, len(walk.Members), walk.Truncated)
	if len(walk.Members) != contextfabric.WorkItemMembershipCensusLimit+1 || !walk.Truncated {
		t.Fatalf("members %d truncated %t, want the census bound plus one and a cut: the walk did not read the population", len(walk.Members), walk.Truncated)
	}
	t.Run("the same walk in series does not grow the live heap", func(t *testing.T) {
		liveHeap := func() uint64 {
			runtime.GC()
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return m.HeapAlloc
		}
		const runs = 25
		var first, last uint64
		for i := 1; i <= runs; i++ {
			if _, err := adapter.TreeWorkItemMembers(ctx, principal, binding, contextfabric.RequestedScope{}, anchor, contextfabric.WorkItemMembershipCensusLimit+1); err != nil {
				t.Fatal(err)
			}
			switch i {
			case 1:
				first = liveHeap()
			case runs:
				last = liveHeap()
			}
		}
		t.Logf("live heap after GC: %d KiB after walk 1, %d KiB after walk %d", first>>10, last>>10, runs)
		if last > first+4<<20 {
			t.Fatalf("the live heap grew from %d KiB to %d KiB over %d identical walks: something keeps each walk's memory", first>>10, last>>10, runs)
		}
	})
	if allocated > walkMemoryBound || peak > walkMemoryBound {
		t.Fatalf("one walk allocated %d MiB (peak live heap +%d MiB), want at most %d MiB: the walk holds whole nodes", allocated>>20, peak>>20, walkMemoryBound>>20)
	}
}

// walkReadsDecideAsWholeNodeReads: on the same store, the projected
// link read and the projected walk step return the same rows, in the same
// order, with the same values of every property the walk decides with, and the
// walk's admission decides each row the same, as the whole-node reads of the
// code before the change; for an unrestricted caller, a restricted one and a
// requested scope.
func walkReadsDecideAsWholeNodeReads(t *testing.T, ctx context.Context, adapter *Adapter, key, orgID string) {
	path, ok := treePath(treeRepository, treeIssue)
	if !ok || len(path) != 2 {
		t.Fatal("no repository to issue path")
	}
	feed, link := path[0], path[1]
	for _, c := range []struct {
		name      string
		principal storage.Principal
		scope     contextfabric.RequestedScope
		slug      string
	}{
		{"unrestricted, acme/svc", storage.Principal{OrgID: orgID}, contextfabric.RequestedScope{}, "acme/svc"},
		{"restricted to acme/svc, acme/svc", storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/svc"}}, contextfabric.RequestedScope{}, "acme/svc"},
		{"requested scope acme/svc, acme/svc", storage.Principal{OrgID: orgID}, contextfabric.RequestedScope{RepositorySlugs: []string{"acme/svc"}}, "acme/svc"},
		{"unrestricted, acme/big", storage.Principal{OrgID: orgID}, contextfabric.RequestedScope{}, "acme/big"},
	} {
		anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:" + walkMemoryUUID(c.slug), Label: c.slug}
		restricted := needsProjectReach(c.principal)
		projected := linkSegmentCypher(feed, link, temporalFilter{}, restricted)
		whole := strings.Replace(projected, " RETURN "+walkProjection("m", walkNodeProperties)+", "+walkProjection("b", walkNodeProperties)+", "+walkProjection("rl", walkLinkProperties), " RETURN m, b, rl", 1)
		if whole == projected {
			t.Fatalf("%s: the link read has no projected return clause to compare", c.name)
		}
		state := treeWalkState{out: &treeWalk{}, principal: c.principal, scope: c.scope}
		rowsOf := func(cypher string) []row {
			params := linkSegmentParams(orgID, anchor, feed, link, 0, 5000, temporalFilter{})
			if restricted {
				params = linkSegmentGrants(params, c.principal)
			}
			rows, err := adapter.api.query(ctx, key, cypher, params, true)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			return rows
		}
		got, want := rowsOf(projected), rowsOf(whole)
		if len(got) != len(want) || len(want) == 0 {
			t.Fatalf("%s: projected read %d rows, whole read %d rows", c.name, len(got), len(want))
		}
		for i := range want {
			for _, column := range []string{"m", "b"} {
				g, w := walkNode(got[i][column]), walkNode(want[i][column])
				for _, property := range walkNodeProperties {
					if !reflect.DeepEqual(g.Properties[property], w.Properties[property]) {
						t.Fatalf("%s: row %d %s.%s projected %#v, whole %#v", c.name, i, column, property, g.Properties[property], w.Properties[property])
					}
				}
			}
			gt, gok := linkTierOf(walkEdge(got[i]["rl"]))
			wt, wok := linkTierOf(walkEdge(want[i]["rl"]))
			if gt != wt || gok != wok {
				t.Fatalf("%s: row %d tier projected %v/%t, whole %v/%t", c.name, i, gt, gok, wt, wok)
			}
			for _, position := range []struct {
				column string
				at     treePosition
			}{{"m", feed.to}, {"b", link.to}} {
				if state.admitted(position.at, walkNode(got[i][position.column]), gt) != state.admitted(position.at, walkNode(want[i][position.column]), wt) {
					t.Fatalf("%s: row %d %s admitted differently on the projection", c.name, i, position.column)
				}
			}
		}
		// The intermediate walk step (pull request -> repository, the hop the
		// project deployment walk reads after the link) returns the same rows.
		step := walkStep{fromKind: treeNodes[treePullRequest].kind, toKind: treeNodes[treeRepository].kind, relation: contractsv1.ContextFabricRelationshipBelongsToRepository, direction: walkOut}
		var prIDs []string
		for _, r := range want {
			prIDs = append(prIDs, canonicalIDOf(walkNode(r["m"])))
		}
		wholeHits, _, err := adapter.walkStepHits(ctx, key, orgID, prIDs, step, temporalFilter{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		step.projected = true
		projectedHits, _, err := adapter.walkStepHits(ctx, key, orgID, prIDs, step, temporalFilter{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(projectedHits) != len(wholeHits) || len(wholeHits) == 0 {
			t.Fatalf("%s: projected step %d hits, whole step %d hits", c.name, len(projectedHits), len(wholeHits))
		}
		for i := range wholeHits {
			if projectedHits[i].from != wholeHits[i].from || !reflect.DeepEqual(projectedHits[i].rel.Properties, wholeHits[i].rel.Properties) {
				t.Fatalf("%s: step hit %d differs: %v / %v", c.name, i, projectedHits[i].from, wholeHits[i].from)
			}
			for _, property := range walkNodeProperties {
				if !reflect.DeepEqual(projectedHits[i].to.Properties[property], wholeHits[i].to.Properties[property]) {
					t.Fatalf("%s: step hit %d %s projected %#v, whole %#v", c.name, i, property, projectedHits[i].to.Properties[property], wholeHits[i].to.Properties[property])
				}
			}
			if state.authorized(projectedHits[i].to) != state.authorized(wholeHits[i].to) {
				t.Fatalf("%s: step hit %d authorized differently on the projection", c.name, i)
			}
		}
	}
}
