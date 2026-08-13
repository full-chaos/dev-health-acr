package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// codexRoundFalkordbImage mirrors adapter_live_integration_test.go's
// falkordbImage pin (that constant lives in the falkorgraph_test black-box
// package and is not visible from here -- this file needs white-box access
// to unexported helpers like graphKey/nodeByKindID/edgesOfNode/
// fulltextSearchNodes, so it cannot itself live in that package).
const codexRoundFalkordbImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

func newCodexRoundLiveAdapter(t *testing.T, ctx context.Context) (*Adapter, string) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: codexRoundFalkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err, "start FalkorDB container")
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	addr := host + ":" + port.Port()
	adapter, err := New(Config{
		Addr: addr, GraphPrefix: "acr-cf-codex-live", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err, "construct Adapter")
	return adapter, addr
}

// TestLiveOrgIDPredicateExcludesNodesPlantedInSameGraphKeyAcrossReadPaths is
// the Codex P2b probe. Two nodes and a connecting edge are planted DIRECTLY
// via a raw GRAPH.QUERY (bypassing the adapter's own write path entirely,
// which always sets org_id correctly), inside the EXACT SAME graph key this
// org's reads target, with org_id deliberately set to a different value.
// One-graph-per-org tenancy already isolates a truly different organization
// by graph key alone; this test is specifically about ADR 0009:95's claimed
// SECOND layer -- the org_id property predicate on every read query -- so it
// must prove the predicate itself actually runs, not rely on graph-key
// separation (which a different test already covers, and which would make
// this predicate look like it worked even if it silently didn't).
func TestLiveOrgIDPredicateExcludesNodesPlantedInSameGraphKeyAcrossReadPaths(t *testing.T) {
	ctx := context.Background()
	adapter, addr := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-org-predicate-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	key := graphKey(adapter.config.GraphPrefix, orgID)

	raw := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = raw.Close() })
	plant := "CREATE (a:Subject:Project {org_id:'wrong-org', subject_kind:'project', canonical_id:'project_planted', label:'Should Not Read', search_text:'plantedsearchtoken'}), " +
		"(b:Subject:WorkItem {org_id:'wrong-org', subject_kind:'work_item', canonical_id:'work_planted', label:'Planted Work'}), " +
		"(a)-[:Relates {relationship_id:'rel_planted', relation_type:'DEPENDS_ON', evidence_refs:['evidence_planted']}]->(b)"
	if err := raw.Do(ctx, "GRAPH.QUERY", key, plant).Err(); err != nil {
		t.Fatalf("plant cross-org nodes via raw GRAPH.QUERY error = %v", err)
	}
	if err := raw.Do(ctx, "GRAPH.QUERY", key, fmt.Sprintf("CALL db.idx.fulltext.createNodeIndex('%s', '%s')", labelSubject, propSearchText)).Err(); err != nil {
		t.Fatalf("create fulltext index for planted node error = %v", err)
	}

	if n, err := adapter.nodeByKindID(ctx, key, orgID, "project", "project_planted"); err != nil || n != nil {
		t.Fatalf("nodeByKindID() = (%#v, %v), want (nil, nil) -- a node planted with a different org_id must be excluded even from this exact graph key", n, err)
	}
	if edges, err := adapter.edgesOfNode(ctx, key, orgID, subjectUUID("project", "project_planted")); err != nil || len(edges) != 0 {
		t.Fatalf("edgesOfNode() = (%#v, %v), want (empty, nil) -- an edge whose endpoint has a different org_id must be excluded", edges, err)
	}
	nodes, _, err := adapter.fulltextSearchNodes(ctx, key, orgID, "plantedsearchtoken", 10)
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("fulltextSearchNodes() = %#v, want no results -- a node planted with a different org_id must be excluded even though its search_text matches", nodes)
	}
}

// TestLiveFulltextSearchOrdersByScoreServerSide is the Codex P2d probe.
// RediSearch does not pre-sort db.idx.fulltext.queryNodes results by score
// (verified independently via raw redis-cli against this same image), so the
// only way fulltextSearchNodes can guarantee the top-relevance match is the
// one actually kept under a tight limit is an explicit server-side
// `ORDER BY score DESC` in the Cypher itself -- a client-side "stop after N
// rows" over an unordered result stream keeps whichever N rows the engine
// happened to enumerate first, not the N most relevant ones.
func TestLiveFulltextSearchOrdersByScoreServerSide(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newCodexRoundLiveAdapter(t, ctx)
	orgID := "live-fulltext-order-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Now().UTC()
	strong := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_strong", Label: "Escalation Strong"}
	weak := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_weak", Label: "Escalation Weak"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_order_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{
				Subject: strong, Aliases: []string{"urgent urgent urgent urgent critical escalation now"},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}},
				EvidenceRefIDs: []string{"evidence_strong"}, ObservedAt: observed, SourceVersion: "v1",
			},
			{
				Subject: weak, Aliases: []string{"a brief unrelated mention of urgent topics elsewhere"},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}},
				EvidenceRefIDs: []string{"evidence_weak"}, ObservedAt: observed, SourceVersion: "v1",
			},
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	key := graphKey(adapter.config.GraphPrefix, orgID)
	results, _, err := adapter.fulltextSearchNodes(ctx, key, orgID, "urgent", 10)
	if err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("fulltextSearchNodes() returned %d results, want 2: %#v", len(results), results)
	}
	if results[0].Score == nil || results[1].Score == nil || *results[0].Score < *results[1].Score {
		t.Fatalf("fulltextSearchNodes() results not in descending score order: %#v", results)
	}
	if got, _ := graphrank.NodeSubject(results[0]); got != strong {
		t.Fatalf("top result = %#v, want the higher-relevance node (%q, term repeated many times) to rank first, not %q", got, strong.Label, weak.Label)
	}
}
