package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const fulltextFirstReadRepeats = 200

// TestLiveFulltextFirstReadAfterApplySeesEveryProjectedNode repeats, on fresh organization graphs of one FalkorDB, the
// shape that the kind-filter and score-order live tests share: apply a batch whose two nodes carry one search term,
// then read that term through the fulltext index at once. Every iteration records the first read's hit count, the
// fulltext index status right after it, and, when the first read came up short, how long the index took to answer
// fully and the status it showed while it did.
func TestLiveFulltextFirstReadAfterApplySeesEveryProjectedNode(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")

	short := 0
	var report []string
	for i := 0; i < fulltextFirstReadRepeats; i++ {
		orgID := fmt.Sprintf("live-fulltext-first-read-%s-%02d", stamp, i)
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
		observed := time.Now().UTC()
		project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_shared_term", Label: "Escalation Project"}
		team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_shared_term", Label: "Escalation Team"}
		scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
		batch := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_first_read", OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", NextCursor: "cursor-1", GeneratedAt: observed,
			Entities: []contextfabric.EntityProjection{
				{Subject: project, Aliases: []string{"shared escalation search term"}, Authorization: scope, EvidenceRefIDs: []string{"evidence_project"}, ObservedAt: observed, SourceVersion: "v1"},
				{Subject: team, Aliases: []string{"shared escalation search term"}, Authorization: scope, EvidenceRefIDs: []string{"evidence_team"}, ObservedAt: observed, SourceVersion: "v1"},
			},
			Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
			Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("iteration %d: ApplyProjectionBatch() error = %v", i, err)
		}
		key := graphKey(adapter.config.GraphPrefix, orgID)
		first, _, err := adapter.fulltextSearchNodes(ctx, key, orgID, "escalation", 10, temporalFilter{})
		if err != nil {
			t.Fatalf("iteration %d: fulltextSearchNodes() error = %v", i, err)
		}
		line := fmt.Sprintf("iteration %02d: first read hits=%d index status after=%s", i, len(first), fulltextIndexStatus(ctx, adapter, key))
		if len(first) != 2 {
			short++
			began := time.Now()
			ready := false
			for time.Since(began) < 30*time.Second {
				hits, _, queryErr := adapter.fulltextSearchNodes(ctx, key, orgID, "escalation", 10, temporalFilter{})
				if queryErr == nil && len(hits) == 2 {
					ready = true
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			line += fmt.Sprintf("; full answer after %dms ready=%v status then=%s", time.Since(began).Milliseconds(), ready, fulltextIndexStatus(ctx, adapter, key))
		}
		report = append(report, line)
	}
	t.Logf("fulltext first read after apply, %d iterations, %d short:\n%s", fulltextFirstReadRepeats, short, strings.Join(report, "\n"))
	if short > 0 {
		t.Fatalf("%d of %d first reads returned fewer than the 2 projected nodes", short, fulltextFirstReadRepeats)
	}
}

func fulltextIndexStatus(ctx context.Context, adapter *Adapter, key string) string {
	statuses, err := adapter.api.indexes(ctx, key)
	if err != nil {
		return "indexes-error"
	}
	var found []string
	for _, status := range statuses {
		for _, kinds := range status.Types {
			for _, kind := range kinds {
				if kind == "FULLTEXT" {
					found = append(found, status.Label+":"+status.Status)
				}
			}
		}
	}
	if len(found) == 0 {
		return "no-fulltext-index"
	}
	return strings.Join(found, ",")
}
