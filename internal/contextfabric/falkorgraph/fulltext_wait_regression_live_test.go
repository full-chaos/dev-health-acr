package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	fulltextHoldBatchSize      = 1000
	fulltextHoldBatches        = 10
	fulltextHoldAliasesPerNode = 20
	fulltextHoldNodes          = fulltextHoldBatchSize * fulltextHoldBatches
	fulltextHoldRepeats        = 3
)

func fulltextHoldBatch(orgID string, b int) contextfabric.ProjectionBatch {
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	observed := time.Now().UTC()
	entities := make([]contextfabric.EntityProjection, 0, fulltextHoldBatchSize)
	for i := 0; i < fulltextHoldBatchSize; i++ {
		n := b*fulltextHoldBatchSize + i
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project_hold_%05d", n), Label: fmt.Sprintf("Hold Project %05d", n)}
		aliases := []string{"shared escalation search term"}
		for k := 0; k < fulltextHoldAliasesPerNode; k++ {
			aliases = append(aliases, fmt.Sprintf("hold filler %05d %02d alpha beta gamma delta epsilon zeta eta theta", n, k))
		}
		entities = append(entities, contextfabric.EntityProjection{
			Subject: subject, Aliases: aliases, Authorization: scope,
			EvidenceRefIDs: []string{"evidence_hold"}, ObservedAt: observed, SourceVersion: "v1",
		})
	}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_hold_%02d", b), OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", NextCursor: fmt.Sprintf("cursor-hold-%02d", b), GeneratedAt: observed, Entities: entities,
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

func TestFulltextHoldBatchesAreValidProjectionBatches(t *testing.T) {
	for b := 0; b < fulltextHoldBatches; b++ {
		if err := fulltextHoldBatch("org-hold", b).Validate(); err != nil {
			t.Fatalf("hold batch %d is not a valid projection batch: %v", b, err)
		}
	}
}

func applyFulltextHoldBatches(t *testing.T, ctx context.Context, adapter *Adapter, orgID string) {
	t.Helper()
	for b := 0; b < fulltextHoldBatches; b++ {
		if _, err := adapter.ApplyProjectionBatch(ctx, fulltextHoldBatch(orgID, b)); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s, batch %d) error = %v", orgID, b, err)
		}
	}
}

func rawFulltextHitCount(t *testing.T, ctx context.Context, adapter *Adapter, key string) int64 {
	t.Helper()
	rows, err := adapter.api.query(ctx, key, "CALL db.idx.fulltext.queryNodes('"+labelSubject+"', 'escalation') YIELD node RETURN count(node) AS hits", nil, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("raw fulltext count error = %v rows = %d", err, len(rows))
	}
	switch hits := rows[0]["hits"].(type) {
	case int64:
		return hits
	case int:
		return int64(hits)
	default:
		t.Fatalf("raw fulltext count has type %T", hits)
		return 0
	}
}

func rebuildFulltextIndex(t *testing.T, ctx context.Context, adapter *Adapter, key string) {
	t.Helper()
	if _, err := adapter.api.query(ctx, key, "CALL db.idx.fulltext.drop('"+labelSubject+"')", nil, false); err != nil {
		t.Fatalf("drop the fulltext index: %v", err)
	}
	if err := adapter.createFulltextIndex(ctx, key); err != nil {
		t.Fatalf("recreate the fulltext index: %v", err)
	}
}

func awaitFulltextIndexStatus(t *testing.T, ctx context.Context, adapter *Adapter, key string) {
	t.Helper()
	began := time.Now()
	for fulltextIndexStatus(ctx, adapter, key) != labelSubject+":OPERATIONAL" {
		if time.Since(began) > time.Minute {
			t.Fatalf("the fulltext index is not operational after a minute")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestLiveFulltextReadWaitsForARebuildingIndex projects fulltextHoldNodes nodes that share a term, then drops and
// recreates the Subject fulltext index, which FalkorDB rebuilds in the background over every stored node: the index is
// UNDER CONSTRUCTION for longer than a first read takes. A control read that goes straight to the index must come back
// short on every repeat, which shows the hold is real; the adapter's read must then leave the index operational and
// see every node. The test fails when the readiness wait is removed from the fulltext query, on every repeat and not by
// chance.
func TestLiveFulltextReadWaitsForARebuildingIndex(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := fmt.Sprintf("live-fulltext-hold-%s", time.Now().UTC().Format("20060102T150405.000000000"))
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	began := time.Now()
	applyFulltextHoldBatches(t, ctx, adapter, orgID)
	key := graphKey(adapter.config.GraphPrefix, orgID)
	awaitFulltextIndexStatus(t, ctx, adapter, key)
	t.Logf("applied %d nodes in %v", fulltextHoldNodes, time.Since(began))

	for i := 0; i < fulltextHoldRepeats; i++ {
		rebuildFulltextIndex(t, ctx, adapter, key)
		controlHits := rawFulltextHitCount(t, ctx, adapter, key)
		controlStatus := fulltextIndexStatus(ctx, adapter, key)
		awaitFulltextIndexStatus(t, ctx, adapter, key)

		rebuildFulltextIndex(t, ctx, adapter, key)
		readBegan := time.Now()
		got, truncated, err := adapter.fulltextSearchNodes(ctx, key, orgID, "escalation", 10, temporalFilter{})
		if err != nil {
			t.Fatalf("repeat %d: fulltextSearchNodes() error = %v", i, err)
		}
		readTook := time.Since(readBegan)
		statusAfter := fulltextIndexStatus(ctx, adapter, key)
		hitsAfter := rawFulltextHitCount(t, ctx, adapter, key)
		t.Logf("repeat %d: control hits=%d of %d status=%s; real read returned %d truncated=%v in %v, status after=%s, raw hits after=%d",
			i, controlHits, fulltextHoldNodes, controlStatus, len(got), truncated, readTook, statusAfter, hitsAfter)

		if controlHits >= fulltextHoldNodes {
			t.Fatalf("repeat %d: the control read saw all %d nodes, so the index was not held under construction", i, fulltextHoldNodes)
		}
		if statusAfter != labelSubject+":OPERATIONAL" {
			t.Fatalf("repeat %d: the index status after the read is %s, want %s:OPERATIONAL", i, statusAfter, labelSubject)
		}
		if hitsAfter != fulltextHoldNodes {
			t.Fatalf("repeat %d: %d of %d nodes are searchable right after the read", i, hitsAfter, fulltextHoldNodes)
		}
	}
}
