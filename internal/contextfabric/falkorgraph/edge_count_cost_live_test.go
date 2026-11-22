//go:build sizeproof

package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/FalkorDB/falkordb-go/v2"
	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The edge count of the projection run's count check on an organization whose
// graph holds many edges of OTHER relations. The image's server TIMEOUT
// (1,000 ms, not overridden in production) bounds every read. Edges of the
// counted relations are written through the real ApplyProjectionBatch; the
// other-relation population is planted with raw Cypher in the same graph key,
// in chunks, until a read that walks every edge of the organization stops at
// the server timeout, and then by as many again so the walk is well past it.
// Every failed count must be that server timeout: the read ends near 1 s with
// its own context still live, the server answers "Query timed out" to the same
// statement, and the run's failure class is canceled. It also records that a
// per-query TIMEOUT above the server's lets the same walk finish, and how long
// a projector write waits while such a walk runs. It plants millions of edges,
// so it runs only with ACR_FALKOR_TIMEOUT_REPRO=1. Needs Docker.
const edgeCountCostEnv = "ACR_FALKOR_TIMEOUT_REPRO"

const edgeCountCostForced = false

// edgeCountCostProdScale is the production graph's edge count in the sizing
// table of docs/operations.md ("Sizing FalkorDB": 346k edges), rounded up.
const edgeCountCostProdScale = 400000

const edgeCountCostQueryTimeoutMs = 60000

const (
	edgeCountCostChunk     = 100000
	edgeCountCostPlant     = 20000
	edgeCountCostMaxNoise  = 2000000
	edgeCountCostRelated   = 400
	edgeCountCostProjected = 200
)

// edgeCountCostWholeOrgWalk reads every edge of the organization: the
// statement shape whose cost grows with the whole graph, not with one
// relation.
var edgeCountCostWholeOrgWalk = fmt.Sprintf("MATCH (a:%s {%s:$org})-[r:%s]->() WHERE r.%s = $rel RETURN count(DISTINCT r.%s) AS total",
	labelSubject, propOrgID, labelRelation, propRelationType, propRelationshipID)

var edgeCountCostRecords = regexp.MustCompile(`Records produced: (\d+)`)

func TestCountEdgesStaysWithinTheServerTimeoutOnALargeGraph(t *testing.T) {
	if os.Getenv(edgeCountCostEnv) != "1" && !edgeCountCostForced {
		t.Skipf("set %s=1 to run the large-graph timeout record", edgeCountCostEnv)
	}
	ctx := context.Background()
	adapter, addr := newLiveFalkorAdapter(t, ctx)
	raw := redis.NewClient(&redis.Options{Addr: addr, ReadTimeout: 60 * time.Second})
	t.Cleanup(func() { _ = raw.Close() })
	orgID := "live-edge-count-cost-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	for _, name := range []string{"TIMEOUT", "TIMEOUT_DEFAULT", "TIMEOUT_MAX"} {
		reply, err := raw.Do(ctx, "GRAPH.CONFIG", "GET", name).Result()
		t.Logf("server config %s = %v (err %v)", name, reply, err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/api"}}
	workItem := func(i int) contextfabric.SubjectRef {
		return contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: fmt.Sprintf("work_item:w%05d", i), Label: fmt.Sprintf("w%d", i)}
	}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:p1", Label: "p1"}
	relation := func(id string, rel contractsv1.ContextFabricRelationshipType, from, to contextfabric.SubjectRef, validFrom *time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: rel, From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: now, ValidFrom: validFrom, SourceVersion: "v1",
		}
	}
	var rels []contextfabric.RelationshipProjection
	for i := 0; i < edgeCountCostRelated; i++ {
		rels = append(rels, relation(fmt.Sprintf("relationship_rel_%05d", i), contractsv1.ContextFabricRelationshipRelatedTo, workItem(i), workItem(i+1), nil))
	}
	for i := 0; i < edgeCountCostProjected; i++ {
		rels = append(rels, relation(fmt.Sprintf("relationship_prj_%05d", i), contractsv1.ContextFabricRelationshipBelongsToProject, workItem(i), project, &now))
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_edge_count_cost_01", OrgID: orgID, Source: "live-test", SourceVersion: "v1",
		Cursor: "", NextCursor: "c1", GeneratedAt: now, Relationships: rels, Entities: []contextfabric.EntityProjection{},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}); err != nil {
		t.Fatalf("ApplyProjectionBatch: %v", err)
	}
	key, err := adapter.resolveReadKey(ctx, orgID, contextfabric.GraphKeyRoleProjectionWrite)
	if err != nil {
		t.Fatal(err)
	}
	text := func(cypher string, params map[string]interface{}) string {
		safe, err := safeParams(params)
		if err != nil {
			t.Fatal(err)
		}
		return falkordb.BuildParamsHeader(safe) + cypher
	}
	ciRun := string(contractsv1.ContextFabricSubjectCIRun)
	if _, err := raw.Do(ctx, "GRAPH.QUERY", key, text(fmt.Sprintf("CREATE (:%s {%s:$org, %s:'repository', %s:'repository:noise'})",
		labelSubject, propOrgID, propKind, propCanonicalID), map[string]interface{}{"org": orgID})).Result(); err != nil {
		t.Fatalf("plant repository: %v", err)
	}
	plant := fmt.Sprintf("MATCH (repo:%s {%s:$org, %s:'repository', %s:'repository:noise'}) UNWIND range($lo, $hi) AS i "+
		"CREATE (:%s {%s:$org, %s:$kind, %s:'ci_pipeline_run:' + toString(i)})-[:%s {%s:'BELONGS_TO_REPOSITORY', %s:'relationship_noise_' + toString(i)}]->(repo)",
		labelSubject, propOrgID, propKind, propCanonicalID,
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, propRelationType, propRelationshipID)
	probe := text(edgeCountCostWholeOrgWalk, map[string]interface{}{"org": orgID, "rel": graphrank.NormalizeRelation(string(contractsv1.ContextFabricRelationshipRelatedTo))})
	noise, firstTimeout := 0, 0
	began := time.Now()
	plantTo := func(end int) {
		for ; noise < end; noise += edgeCountCostPlant {
			if _, err := raw.Do(ctx, "GRAPH.QUERY", key, text(plant, map[string]interface{}{"org": orgID, "kind": ciRun, "lo": noise, "hi": noise + edgeCountCostPlant - 1})).Result(); err != nil {
				t.Fatalf("plant noise at %d: %v", noise, err)
			}
		}
	}
	walk := func() bool {
		probeBegan := time.Now()
		_, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, probe).Result()
		timedOut := err != nil && strings.Contains(err.Error(), "Query timed out")
		t.Logf("noise %d edges: whole-organization walk took %s (err %v)", noise, time.Since(probeBegan), err)
		if err != nil && !timedOut {
			t.Fatalf("whole-organization walk: %v", err)
		}
		return timedOut
	}
	// stall times projector writes (a 50-edge batch through the real
	// ApplyProjectionBatch) and a served-style one-edge read before, during and
	// after a whole-organization walk that runs under a per-query TIMEOUT above
	// the server's. The walk must answer, not time out.
	batchSeq := 0
	write := func() time.Duration {
		batchSeq++
		var batchRels []contextfabric.RelationshipProjection
		for i := 0; i < 50; i++ {
			batchRels = append(batchRels, relation(fmt.Sprintf("relationship_stall_%03d_%02d", batchSeq, i), contractsv1.ContextFabricRelationshipBlocks,
				workItem(10000+batchSeq*100+i), workItem(10000+batchSeq*100+i+1), nil))
		}
		writeBegan := time.Now()
		if _, err := adapter.ApplyProjectionBatch(ctx, contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_edge_count_stall_%03d", batchSeq), OrgID: orgID, Source: "live-test", SourceVersion: "v1",
			Cursor: "c1", NextCursor: "c2", GeneratedAt: now, Relationships: batchRels, Entities: []contextfabric.EntityProjection{},
			Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
		}); err != nil {
			t.Fatalf("stall write %d: %v", batchSeq, err)
		}
		return time.Since(writeBegan)
	}
	lookup := text(fmt.Sprintf("MATCH ()-[r:%s {%s:$id}]->() RETURN count(r) AS total", labelRelation, propRelationshipID), map[string]interface{}{"id": "relationship_rel_00001"})
	served := func() time.Duration {
		readBegan := time.Now()
		if _, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, lookup).Result(); err != nil {
			t.Fatalf("served read: %v", err)
		}
		return time.Since(readBegan)
	}
	stall := func(label string) {
		before := []time.Duration{write(), write(), write()}
		servedBefore := served()
		type walkResult struct {
			took  time.Duration
			total int64
			err   error
		}
		done := make(chan walkResult, 1)
		go func() {
			walkBegan := time.Now()
			reply, err := raw.Do(ctx, "GRAPH.RO_QUERY", key, probe, "TIMEOUT", edgeCountCostQueryTimeoutMs).Result()
			done <- walkResult{took: time.Since(walkBegan), total: edgeCountCostTotal(reply), err: err}
		}()
		time.Sleep(100 * time.Millisecond)
		during := write()
		servedDuring := served()
		w := <-done
		after := []time.Duration{write(), write()}
		servedAfter := served()
		t.Logf("STALL %s noise %d: walk under TIMEOUT %d ms took %s, total %d, err %v; write before %v during %s after %v; served read before %s during %s after %s",
			label, noise, edgeCountCostQueryTimeoutMs, w.took, w.total, w.err, before, during, after, servedBefore, servedDuring, servedAfter)
		if w.err != nil || w.total != edgeCountCostRelated {
			t.Errorf("STALL %s: the walk under a per-query TIMEOUT of %d ms = %d, %v; want %d", label, edgeCountCostQueryTimeoutMs, w.total, w.err, edgeCountCostRelated)
		}
	}
	prodScaleDone := false
	for firstTimeout == 0 && noise < edgeCountCostMaxNoise {
		plantTo(noise + edgeCountCostChunk)
		if !prodScaleDone && noise >= edgeCountCostProdScale {
			stall("prod scale")
			prodScaleDone = true
		}
		if walk() {
			firstTimeout = noise
		}
	}
	if firstTimeout == 0 {
		t.Fatalf("the venue is too small: a whole-organization walk over %d edges finished within the server timeout", noise)
	}
	plantTo(2 * firstTimeout)
	if !walk() {
		t.Fatalf("the venue is too small: a whole-organization walk over %d edges finished within the server timeout", noise)
	}
	t.Logf("planted %d other-relation edges in %s (first walk timeout at %d)", noise, time.Since(began), firstTimeout)
	// The proof that the per-query TIMEOUT wins over the server's 1,000 ms: the
	// walk the server stops at its own timeout answers under the per-query one,
	// past one second.
	proofBegan := time.Now()
	proof, proofErr := raw.Do(ctx, "GRAPH.RO_QUERY", key, probe, "TIMEOUT", edgeCountCostQueryTimeoutMs).Result()
	proofTook := time.Since(proofBegan)
	t.Logf("PROOF noise %d: the walk under a per-query TIMEOUT of %d ms took %s, total %d, err %v", noise, edgeCountCostQueryTimeoutMs, proofTook, edgeCountCostTotal(proof), proofErr)
	if proofErr != nil || edgeCountCostTotal(proof) != edgeCountCostRelated || proofTook <= time.Second {
		t.Errorf("PROOF: the per-query TIMEOUT did not let the walk run past 1 s and answer %d: took %s, %v", edgeCountCostRelated, proofTook, proofErr)
	}
	stall("past the server timeout")

	for _, c := range []struct {
		key  contextfabric.EdgeCountKey
		want int64
	}{
		{contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipRelatedTo}, edgeCountCostRelated},
		{contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition}, edgeCountCostProjected},
	} {
		cypher, err := edgeCountCypher(c.key)
		if err != nil {
			t.Fatal(err)
		}
		params := map[string]interface{}{"org": orgID, "rel": graphrank.NormalizeRelation(string(c.key.Relation))}
		if c.key.Arm != "" {
			params["arm"] = c.key.Arm
		}
		plan, err := raw.Do(ctx, "GRAPH.EXPLAIN", key, text(cypher, params)).Result()
		t.Logf("EXPLAIN %+v (err %v)\n%s", c.key, err, strings.Join(edgePageCostLines(plan), "\n"))
		// Every operation of the read produces at most twice the counted
		// edges: the read is bounded by the relation, not by the graph.
		profile, err := raw.Do(ctx, "GRAPH.PROFILE", key, text(cypher, params)).Result()
		if err != nil {
			t.Errorf("PROFILE %+v: %v", c.key, err)
		} else {
			lines := edgePageCostLines(profile)
			for _, line := range lines {
				if m := edgeCountCostRecords.FindStringSubmatch(line); m != nil {
					if n, _ := strconv.ParseInt(m[1], 10, 64); n > 2*c.want {
						t.Errorf("PROFILE %+v: an operation produced %d records for %d counted edges: %s", c.key, n, c.want, strings.TrimSpace(line))
					}
				}
			}
			t.Logf("PROFILE %+v\n%s", c.key, strings.Join(lines, "\n"))
		}
		got, err := countEdgesCause(t, ctx, adapter, raw, key, orgID, c.key, text(cypher, params))
		if err != nil || got != c.want {
			t.Errorf("CountEdges(%+v) = %d, %v; want %d within the server timeout", c.key, got, err, c.want)
		}
	}
	// The counted population of this key is the planted edges: it is the
	// server timeout, whatever the plan of the other keys.
	ciKey := contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToRepository, Arm: ciRun}
	cypher, err := edgeCountCypher(ciKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := countEdgesCause(t, ctx, adapter, raw, key, orgID, ciKey, text(cypher, map[string]interface{}{"org": orgID, "rel": "BELONGS_TO_REPOSITORY", "arm": ciRun})); err == nil {
		t.Errorf("CountEdges(%+v) over %d edges of its own finished within the server timeout: the cause cell observed no failure", ciKey, noise)
	}
}

// countEdgesCause runs CountEdges under the count check's own 30 s edge
// context and, when it fails, requires the failure to be the server TIMEOUT:
// the context is still live, the read ended within 2 s, it unwraps to
// context.DeadlineExceeded, the projection run classes it canceled, and the
// server answers the same statement with "Query timed out".
func countEdgesCause(t *testing.T, parent context.Context, adapter *Adapter, raw *redis.Client, graphKey, orgID string, key contextfabric.EdgeCountKey, statement string) (int64, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	began := time.Now()
	got, err := adapter.CountEdges(ctx, orgID, key)
	elapsed := time.Since(began)
	class := projectionrun.ClassifyFailure(err)
	t.Logf("CountEdges(%+v) = %d, %v in %s (failure_class %q, context err %v)", key, got, err, elapsed, class, ctx.Err())
	if err == nil {
		return got, nil
	}
	_, rawErr := raw.Do(parent, "GRAPH.RO_QUERY", graphKey, statement).Result()
	t.Logf("the same statement sent raw: %v", rawErr)
	if ctx.Err() != nil || elapsed > 2*time.Second || !errors.Is(err, context.DeadlineExceeded) || class != "canceled" ||
		rawErr == nil || !strings.Contains(rawErr.Error(), "Query timed out") {
		t.Errorf("CountEdges(%+v) failed but not by the server timeout: err %v, elapsed %s, context err %v, class %q, raw %v", key, err, elapsed, ctx.Err(), class, rawErr)
	}
	return got, err
}

// edgeCountCostTotal reads the single count of a non-compact GRAPH.RO_QUERY
// reply: [header, [[total]], stats]. -1 when the reply has another shape.
func edgeCountCostTotal(reply interface{}) int64 {
	parts, ok := reply.([]interface{})
	if !ok || len(parts) < 2 {
		return -1
	}
	rows, ok := parts[1].([]interface{})
	if !ok || len(rows) != 1 {
		return -1
	}
	cells, ok := rows[0].([]interface{})
	if !ok || len(cells) != 1 {
		return -1
	}
	total, ok := cells[0].(int64)
	if !ok {
		return -1
	}
	return total
}
