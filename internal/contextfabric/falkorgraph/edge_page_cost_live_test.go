package falkorgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FalkorDB/falkordb-go/v2"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The cost of the read_relationships edge page on a hub: two teams, each
// owning half of edgePageCostItems work items (80% ended); the first also
// owns edgePageCostRepos repositories. Two thirds of the edges never touch
// a team. Written through the real ApplyProjectionBatch into a real
// FalkorDB. Needs Docker; run by CI.
const edgePageCostRepos = 200

// edgePageCostItems: under the race detector the seed is a tenth and nothing
// is timed (the read-work bound and the page equality still run); the race
// shard has a wall budget and its times say nothing about production.
var edgePageCostItems = func() int {
	if raceDetectorEnabled {
		return 1200
	}
	return 12000
}()

type edgePageCostVenue struct {
	adapter *Adapter
	addr    string
	raw     *redis.Client
	key     string
	orgID   string
	now     time.Time
	team    contextfabric.SubjectRef
	repos   []contextfabric.SubjectRef
}

func edgePageCostStart(t *testing.T, ctx context.Context) *edgePageCostVenue {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: codexRoundFalkordbImage, ExposedPorts: []string{"6379/tcp"},
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
	v := &edgePageCostVenue{addr: host + ":" + port.Port()}
	v.adapter = v.newAdapter(t, 30*time.Second)
	v.raw = redis.NewClient(&redis.Options{Addr: v.addr, ReadTimeout: 60 * time.Second})
	t.Cleanup(func() { _ = v.raw.Close() })
	return v
}

func (v *edgePageCostVenue) newAdapter(t *testing.T, timeout time.Duration) *Adapter {
	t.Helper()
	adapter, err := New(Config{
		Addr: v.addr, GraphPrefix: "acr-cf-edge-page-cost", RequestTimeout: timeout,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return adapter
}

func edgePageCostRID(prefix string, i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", prefix, i)))
	return "rel_" + hex.EncodeToString(sum[:8])
}

// seed: team T owns edgePageCostRepos repositories and every even work item,
// team U every odd one (a work item's OWNED_BY_TEAM edge carries no window,
// as the producer writes it); 80% of the work items ended in the past; every
// work item BELONGS_TO_REPOSITORY one repository with the item's own window
// and RELATES_TO the next work item (no window). Only OWNED_BY_TEAM edges
// touch a team.
func (v *edgePageCostVenue) seed(t *testing.T, ctx context.Context) {
	t.Helper()
	v.orgID = "live-edge-page-cost-" + time.Now().UTC().Format("20060102T150405.000000000")
	v.now = time.Now().UTC()
	start := v.now.Add(-600 * 24 * time.Hour)
	v.team = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}, TeamIDs: []string{"T"}}
	entity := func(subject contextfabric.SubjectRef, from, to *time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{
			Subject: subject, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + strings.NewReplacer(":", "_", ".", "_").Replace(subject.CanonicalID)},
			ObservedAt: v.now, ValidFrom: from, ValidTo: to, SourceVersion: "v1",
		}
	}
	relation := func(id, kind string, from, to contextfabric.SubjectRef, validFrom, validTo *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: contextfabric.RelationshipType(kind), From: from, To: to,
			Derivation: contextfabric.DerivationRuleInferred, EpistemicStatus: contextfabric.EpistemicSourceAsserted,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: v.now,
			ValidFrom: validFrom, ValidTo: validTo, SourceVersion: "v1",
		}
	}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:U", Label: "U"}
	entities := []contextfabric.EntityProjection{entity(v.team, &start, nil), entity(other, &start, nil)}
	var relationships []contextfabric.RelationshipProjection
	for i := 0; i < edgePageCostRepos; i++ {
		repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: fmt.Sprintf("repository:r%04d", i), Label: fmt.Sprintf("acme/r%04d", i)}
		v.repos = append(v.repos, repo)
		entities = append(entities, entity(repo, &start, nil))
		relationships = append(relationships, relation(edgePageCostRID("own-repo", i), "OWNED_BY_TEAM", repo, v.team, &start, nil))
	}
	item := func(i int) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item.v2:w%05d", i), Label: fmt.Sprintf("Work item %d", i)}
	}
	for i := 0; i < edgePageCostItems; i++ {
		created := v.now.Add(-time.Duration(i%500+5) * 24 * time.Hour)
		var ended *time.Time
		if i%5 != 0 {
			e := created.Add(time.Duration(i%30+1) * 24 * time.Hour)
			if e.After(v.now) {
				e = v.now.Add(-time.Hour)
			}
			ended = &e
		}
		owner := v.team
		if i%2 == 1 {
			owner = other
		}
		entities = append(entities, entity(item(i), &created, ended))
		relationships = append(relationships,
			relation(edgePageCostRID("own-item", i), "OWNED_BY_TEAM", item(i), owner, nil, nil),
			relation(edgePageCostRID("item-repo", i), "BELONGS_TO_REPOSITORY", item(i), v.repos[i%len(v.repos)], &created, ended),
			relation(edgePageCostRID("item-item", i), "RELATES_TO", item(i), item((i+1)%edgePageCostItems), nil, nil))
	}
	apply := func(index int, entities []contextfabric.EntityProjection, relationships []contextfabric.RelationshipProjection) {
		b := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_edge_page_cost_%08d", index), OrgID: v.orgID, Source: "live-test",
			SourceVersion: "v1", Cursor: fmt.Sprintf("cursor-%d", index), NextCursor: fmt.Sprintf("cursor-%d", index+1), GeneratedAt: v.now,
			Entities: entities, Relationships: relationships,
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if _, err := v.adapter.ApplyProjectionBatch(ctx, b); err != nil {
			t.Fatalf("ApplyProjectionBatch(%d): %v", index, err)
		}
	}
	const chunk = 1000
	index := 1
	for lo := 0; lo < len(entities); lo += chunk {
		apply(index, entities[lo:min(lo+chunk, len(entities))], []contextfabric.RelationshipProjection{})
		index++
	}
	for lo := 0; lo < len(relationships); lo += chunk {
		apply(index, []contextfabric.EntityProjection{}, relationships[lo:min(lo+chunk, len(relationships))])
		index++
	}
	binding, err := v.adapter.ResolveInvestigationBinding(ctx, storage.Principal{OrgID: v.orgID, Subject: "u", CredentialID: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if v.key, err = v.adapter.effectiveKey(ctx, v.orgID, binding); err != nil {
		t.Fatal(err)
	}
}

func (v *edgePageCostVenue) text(t *testing.T, cypher string, params map[string]interface{}) string {
	t.Helper()
	safe, err := safeParams(params)
	if err != nil {
		t.Fatal(err)
	}
	return falkordb.BuildParamsHeader(safe) + cypher
}

func edgePageCostLines(reply interface{}) []string {
	var out []string
	var walk func(interface{})
	walk = func(x interface{}) {
		switch value := x.(type) {
		case []interface{}:
			for _, item := range value {
				walk(item)
			}
		case string:
			out = append(out, value)
		default:
			out = append(out, fmt.Sprint(value))
		}
	}
	walk(reply)
	return out
}

// internalMs runs one read under a server-side timeout and returns the
// server's internal execution time.
func (v *edgePageCostVenue) internalMs(t *testing.T, ctx context.Context, text string) (float64, error) {
	t.Helper()
	reply, err := v.raw.Do(ctx, "GRAPH.RO_QUERY", v.key, text, "TIMEOUT", edgePageCostServerTimeoutMs).Result()
	if err != nil {
		return 0, err
	}
	parts, _ := reply.([]interface{})
	for _, line := range edgePageCostLines(parts[len(parts)-1]) {
		if rest, ok := strings.CutPrefix(line, "Query internal execution time:"); ok {
			return strconv.ParseFloat(strings.Fields(rest)[0], 64)
		}
	}
	return 0, fmt.Errorf("no internal execution time in %v", reply)
}

const edgePageCostServerTimeoutMs = 10000

// medianMs: one warm-up read, then five measured reads. A read the server
// stops at edgePageCostServerTimeoutMs returns the error.
func (v *edgePageCostVenue) medianMs(t *testing.T, ctx context.Context, text string) (float64, []float64, error) {
	t.Helper()
	if _, err := v.internalMs(t, ctx, text); err != nil {
		return 0, nil, err
	}
	runs := make([]float64, 0, 5)
	for i := 0; i < 5; i++ {
		ms, err := v.internalMs(t, ctx, text)
		if err != nil {
			return 0, nil, err
		}
		runs = append(runs, ms)
	}
	sorted := append([]float64(nil), runs...)
	sort.Float64s(sorted)
	return sorted[2], runs, nil
}

func (v *edgePageCostVenue) profile(ctx context.Context, text string) ([]string, error) {
	reply, err := v.raw.Do(ctx, "GRAPH.PROFILE", v.key, text).Result()
	if err != nil {
		return nil, fmt.Errorf("GRAPH.PROFILE: %w", err)
	}
	return edgePageCostLines(reply), nil
}

var edgePageCostRecords = regexp.MustCompile(`^\s*(Node By Index Scan|Conditional Traverse|Node By Label Scan|All Node Scan)\b.*Records produced: (\d+)`)

// readWork sums the records the scans and the traversals of a profile
// produced: what the plan read before any filter could drop a row.
func edgePageCostReadWork(lines []string) (scanned, traversed int) {
	for _, line := range lines {
		m := edgePageCostRecords.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if m[1] == "Conditional Traverse" {
			traversed += n
		} else {
			scanned += n
		}
	}
	return scanned, traversed
}

// edgePageCostLegacyInArm rewrites the in arm to its single-pattern form
// (a:other)-[r]->(b:origin): same aliases, same direction, same predicates.
// A statement already in that form is returned as it is.
func edgePageCostLegacyInArm(cypher string) string {
	origin := fmt.Sprintf("(b:%s {%s:$org, %s:o.k, %s:o.i})", labelSubject, propOrgID, propKind, propCanonicalID)
	other := fmt.Sprintf("(a:%s {%s:$org})", labelSubject, propOrgID)
	return strings.Replace(cypher, "MATCH "+origin+" WITH b MATCH "+other+"-[r:"+labelRelation+"]->(b)", "MATCH "+other+"-[r:"+labelRelation+"]->"+origin, 1)
}

// edgePageCostReference is the page the single-pattern statement serves,
// built one origin at a time (a one-origin statement stays fast in either
// form) and merged in Go: union by relationship id, ordered by it, cut at
// Limit+1.
func (v *edgePageCostVenue) edgePageCostReference(t *testing.T, ctx context.Context, query directread.EdgePageQuery) []edgePageCostRow {
	t.Helper()
	seen := map[string]bool{}
	var all []edgePageCostRow
	for _, origin := range query.Origins {
		one := query
		one.Origins = []contextfabric.SubjectRef{origin}
		cypher, params := directEdgePageCypher(v.orgID, one)
		for _, row := range v.page(t, ctx, edgePageCostLegacyInArm(cypher), params) {
			if !seen[row.rid] {
				seen[row.rid] = true
				all = append(all, row)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].rid < all[j].rid })
	return all[:min(len(all), query.Limit+1)]
}

type edgePageCostRow struct {
	rid, from, to string
}

func (v *edgePageCostVenue) page(t *testing.T, ctx context.Context, cypher string, params map[string]interface{}) []edgePageCostRow {
	t.Helper()
	rows, err := v.adapter.api.query(ctx, v.key, cypher, params, true)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	out := make([]edgePageCostRow, 0, len(rows))
	for _, r := range rows {
		e, eok := r["r"].(*edge)
		a, aok := r["a"].(*node)
		b, bok := r["b"].(*node)
		if !eok || !aok || !bok {
			t.Fatalf("row without an edge and two nodes: %v", r)
		}
		out = append(out, edgePageCostRow{propStringValue(e.Properties[propRelationshipID]), propStringValue(a.Properties[propCanonicalID]), propStringValue(b.Properties[propCanonicalID])})
	}
	return out
}

func TestEdgePageReadsOnlyTheOriginsEdges(t *testing.T) {
	ctx := context.Background()
	v := edgePageCostStart(t, ctx)
	began := time.Now()
	v.seed(t, ctx)
	t.Logf("seeded %d work items, %d repositories, %d edges in %s", edgePageCostItems, edgePageCostRepos, 3*edgePageCostItems+edgePageCostRepos, time.Since(began))
	asOf := v.now.Add(-250 * 24 * time.Hour)
	hub := []contextfabric.SubjectRef{v.team}
	frontier := append([]contextfabric.SubjectRef(nil), v.repos[:20]...)
	after := &directread.EdgeKey{RelationshipID: edgePageCostRID("own-item", 20)}
	hubDegree := edgePageCostItems/2 + edgePageCostRepos
	frontierDegree := 20 + 20*(edgePageCostItems/edgePageCostRepos)
	cases := []struct {
		name   string
		query  directread.EdgePageQuery
		degree int
		// kindIndexed: the statement keeps its single pattern (EndKinds), so
		// its read work is bounded by the nodes of those kinds, not by the
		// origins.
		kindIndexed bool
	}{
		{"current", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: v.now, Current: true}, hubDegree, false},
		{"as_of", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: asOf}, hubDegree, false},
		{"strict_now", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: v.now}, hubDegree, false},
		{"current_in_owned", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: v.now, Current: true, Direction: directread.EdgeDirectionIn, Types: []string{"OWNED_BY_TEAM"}}, hubDegree, false},
		{"as_of_in_owned", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: asOf, Direction: directread.EdgeDirectionIn, Types: []string{"OWNED_BY_TEAM"}}, hubDegree, false},
		{"current_after_limit100", directread.EdgePageQuery{Origins: hub, Limit: 100, ValidAt: v.now, Current: true, After: after}, hubDegree, false},
		{"as_of_after_limit100", directread.EdgePageQuery{Origins: hub, Limit: 100, ValidAt: asOf, After: after}, hubDegree, false},
		{"current_frontier", directread.EdgePageQuery{Origins: frontier, Limit: directread.MaxEdgePageLimit, ValidAt: v.now, Current: true, Exclude: &v.team}, frontierDegree, false},
		{"as_of_frontier", directread.EdgePageQuery{Origins: frontier, Limit: directread.MaxEdgePageLimit, ValidAt: asOf, Exclude: &v.team}, frontierDegree, false},
		{"owned_by_end_kinds", directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: v.now, Direction: directread.EdgeDirectionIn, EndKinds: []string{string(contextfabric.SubjectRepository)}}, hubDegree, true},
	}
	medians := map[string][2]float64{}
	for _, c := range cases {
		cypher, params := directEdgePageCypher(v.orgID, c.query)
		legacy := edgePageCostLegacyInArm(cypher)
		got, want := v.page(t, ctx, cypher, params), v.edgePageCostReference(t, ctx, c.query)
		if len(got) == 0 || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: page %v, single-pattern reference %v", c.name, got, want)
		}
		plan, err := v.profile(ctx, v.text(t, cypher, params))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		scanned, traversed := edgePageCostReadWork(plan)
		t.Logf("CASE %s rows=%d read work scanned=%d traversed=%d (origins %d, origin degree %d)\nPROFILE after %s\n%s",
			c.name, len(got), scanned, traversed, len(c.query.Origins), c.degree, c.name, strings.Join(plan, "\n"))
		if !raceDetectorEnabled {
			afterMs, afterRuns, err := v.medianMs(t, ctx, v.text(t, cypher, params))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			before, beforeRuns, legacyErr := v.medianMs(t, ctx, v.text(t, legacy, params))
			legacyScanned, legacyTraversed := -1, -1
			if legacyErr == nil {
				var legacyPlan []string
				if legacyPlan, legacyErr = v.profile(ctx, v.text(t, legacy, params)); legacyErr == nil {
					legacyScanned, legacyTraversed = edgePageCostReadWork(legacyPlan)
					t.Logf("PROFILE before %s\n%s", c.name, strings.Join(legacyPlan, "\n"))
				}
			}
			medians[c.name] = [2]float64{before, afterMs}
			t.Logf("TIMES %s before_ms=%.2f %v (single-pattern error %v, read work scanned=%d traversed=%d) after_ms=%.2f %v",
				c.name, before, beforeRuns, legacyErr, legacyScanned, legacyTraversed, afterMs, afterRuns)
		}
		// Each arm scans the origins only and traverses only their own edges:
		// at most two origin lookups per origin (one per arm) and at most two
		// passes over the origins' edges.
		if c.kindIndexed {
			if legacy != cypher || scanned > edgePageCostRepos || traversed > edgePageCostRepos {
				t.Errorf("%s: the end-kind page changed its statement or read beyond the %d nodes of its kind: scanned %d, traversed %d", c.name, edgePageCostRepos, scanned, traversed)
			}
			continue
		}
		if scanned > 2*len(c.query.Origins) || traversed > 2*c.degree {
			t.Errorf("%s: the page read beyond the origins: scanned %d nodes for %d origins, traversed %d edges for origin degree %d", c.name, scanned, len(c.query.Origins), traversed, c.degree)
		}
	}
	if !raceDetectorEnabled {
		for _, pair := range [][2]string{{"current", "as_of"}, {"current", "strict_now"}, {"current_in_owned", "as_of_in_owned"}, {"current_after_limit100", "as_of_after_limit100"}, {"current_frontier", "as_of_frontier"}} {
			c, a := medians[pair[0]], medians[pair[1]]
			t.Logf("RATIO %s/%s before=%.2f after=%.2f (%s before %.2f ms, after %.2f ms)", pair[0], pair[1], c[0]/a[0], c[1]/a[1], pair[1], a[0], a[1])
		}
	}

	deadline := v.newAdapter(t, time.Second)
	principal := storage.Principal{OrgID: v.orgID, Subject: "u", CredentialID: "c"}
	binding, err := deadline.ResolveInvestigationBinding(ctx, principal)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 4)
	walls := make([]time.Duration, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			began := time.Now()
			_, errs[i] = deadline.DirectEdgePage(ctx, principal, binding, directread.EdgePageQuery{Origins: hub, Limit: 5, ValidAt: time.Now().UTC(), Current: true})
			walls[i] = time.Since(began)
		}(i)
	}
	wg.Wait()
	t.Logf("PARALLEL 4 current-axis pages under a 1 s request deadline: walls=%v", walls)
	for i, err := range errs {
		if err != nil {
			t.Errorf("parallel page %d: %v", i, err)
		}
	}
}
