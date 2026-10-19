package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const (
	fulltextHoldBatchSize = 1000
	fulltextHoldBatches   = 10
	fulltextHoldNodes     = fulltextHoldBatchSize * fulltextHoldBatches
	fulltextHoldRepeats   = 3
)

func applyFulltextHoldBatches(t *testing.T, ctx context.Context, adapter *Adapter, orgID string) {
	t.Helper()
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	filler := strings.Repeat("alpha beta gamma delta epsilon zeta eta theta iota kappa ", 20)
	for b := 0; b < fulltextHoldBatches; b++ {
		observed := time.Now().UTC()
		entities := make([]contextfabric.EntityProjection, 0, fulltextHoldBatchSize)
		for i := 0; i < fulltextHoldBatchSize; i++ {
			n := b*fulltextHoldBatchSize + i
			subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: fmt.Sprintf("project_hold_%05d", n), Label: fmt.Sprintf("Hold Project %05d", n)}
			entities = append(entities, contextfabric.EntityProjection{
				Subject: subject, Aliases: []string{"shared escalation search term " + filler}, Authorization: scope,
				EvidenceRefIDs: []string{"evidence_hold"}, ObservedAt: observed, SourceVersion: "v1",
			})
		}
		batch := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: fmt.Sprintf("batch_hold_%02d", b), OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", NextCursor: fmt.Sprintf("cursor-hold-%02d", b), GeneratedAt: observed, Entities: entities,
			Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
			Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
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

// TestLiveFulltextReadWaitsForALargeBatchToBeIndexed applies fulltextHoldNodes nodes that share a term, in the largest batches the projection contract allows,
// which keeps the Subject fulltext index under construction for longer than a first read takes. A control read that
// goes straight to the index must come back short on every fresh graph, which shows the hold is real; the adapter's
// read must then leave the index operational and see every node. The test fails when the readiness wait is removed
// from the fulltext query, on every iteration and not by chance.
func TestLiveFulltextReadWaitsForALargeBatchToBeIndexed(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")

	for i := 0; i < fulltextHoldRepeats; i++ {
		controlOrg := fmt.Sprintf("live-fulltext-hold-control-%s-%02d", stamp, i)
		realOrg := fmt.Sprintf("live-fulltext-hold-real-%s-%02d", stamp, i)
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), controlOrg) })
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), realOrg) })

		applyFulltextHoldBatches(t, ctx, adapter, controlOrg)
		controlKey := graphKey(adapter.config.GraphPrefix, controlOrg)
		controlHits := rawFulltextHitCount(t, ctx, adapter, controlKey)
		controlStatus := fulltextIndexStatus(ctx, adapter, controlKey)

		applyFulltextHoldBatches(t, ctx, adapter, realOrg)
		realKey := graphKey(adapter.config.GraphPrefix, realOrg)
		began := time.Now()
		got, truncated, err := adapter.fulltextSearchNodes(ctx, realKey, realOrg, "escalation", 10, temporalFilter{})
		if err != nil {
			t.Fatalf("iteration %d: fulltextSearchNodes() error = %v", i, err)
		}
		readTook := time.Since(began)
		statusAfter := fulltextIndexStatus(ctx, adapter, realKey)
		hitsAfter := rawFulltextHitCount(t, ctx, adapter, realKey)
		t.Logf("iteration %d: control hits=%d of %d status=%s; real read returned %d truncated=%v in %v, status after=%s, raw hits after=%d",
			i, controlHits, fulltextHoldNodes, controlStatus, len(got), truncated, readTook, statusAfter, hitsAfter)

		if controlHits >= fulltextHoldNodes {
			t.Fatalf("iteration %d: the control read saw all %d nodes, so the index was not held under construction", i, fulltextHoldNodes)
		}
		if statusAfter != labelSubject+":OPERATIONAL" {
			t.Fatalf("iteration %d: the index status after the read is %s, want %s:OPERATIONAL", i, statusAfter, labelSubject)
		}
		if hitsAfter != fulltextHoldNodes {
			t.Fatalf("iteration %d: %d of %d nodes are searchable right after the read", i, hitsAfter, fulltextHoldNodes)
		}
	}
}
