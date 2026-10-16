package falkorgraph

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

const fulltextSecondProjectionRepeats = 10

// TestLiveFulltextSecondProjectionBecomesSearchable projects a first batch, reads until the fulltext index answers,
// then projects a second batch that adds a subject carrying a new term and reads that term at once. Every iteration
// records the index status right after the second apply, the first read's hit count, and how long the new subject took
// to become searchable with the status seen while waiting: it shows whether an existing index goes back under
// construction after new writes, and whether a subject written after the index was operational is found at all.
func TestLiveFulltextSecondProjectionBecomesSearchable(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}

	apply := func(orgID, batchID string, subject contextfabric.SubjectRef, alias string) {
		t.Helper()
		observed := time.Now().UTC()
		batch := contextfabric.ProjectionBatch{
			SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: "live-test",
			SourceVersion: "v1", NextCursor: "cursor-" + batchID, GeneratedAt: observed,
			Entities: []contextfabric.EntityProjection{
				{Subject: subject, Aliases: []string{alias}, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + batchID}, ObservedAt: observed, SourceVersion: "v1"},
			},
			Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
			Tombstones: []contextfabric.ProjectionTombstone{},
		}
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch(%s) error = %v", batchID, err)
		}
	}

	var report []string
	notFound := 0
	for i := 0; i < fulltextSecondProjectionRepeats; i++ {
		orgID := fmt.Sprintf("live-fulltext-second-%s-%02d", stamp, i)
		t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
		key := graphKey(adapter.config.GraphPrefix, orgID)

		apply(orgID, "batch_first", contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_first", Label: "First Project"}, "firstneedle alpha")
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			hits, _, err := adapter.fulltextSearchNodes(ctx, key, orgID, "firstneedle", 10, temporalFilter{})
			if err == nil && len(hits) == 1 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}

		apply(orgID, "batch_second", contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_second", Label: "Second Project"}, "secondneedle beta")
		statusAfter := fulltextIndexStatus(ctx, adapter, key)
		first, _, err := adapter.fulltextSearchNodes(ctx, key, orgID, "secondneedle", 10, temporalFilter{})
		if err != nil {
			t.Fatalf("iteration %d: fulltextSearchNodes() error = %v", i, err)
		}
		line := fmt.Sprintf("iteration %02d: status right after the second apply=%s, first read hits=%d", i, statusAfter, len(first))
		if len(first) != 1 {
			began := time.Now()
			found := false
			var seen []string
			for time.Since(began) < 15*time.Second {
				hits, _, queryErr := adapter.fulltextSearchNodes(ctx, key, orgID, "secondneedle", 10, temporalFilter{})
				if queryErr == nil && len(hits) == 1 {
					found = true
					break
				}
				if status := fulltextIndexStatus(ctx, adapter, key); len(seen) == 0 || seen[len(seen)-1] != status {
					seen = append(seen, status)
				}
				time.Sleep(25 * time.Millisecond)
			}
			if !found {
				notFound++
			}
			line += fmt.Sprintf("; searchable after %dms found=%v statuses seen while waiting=%s", time.Since(began).Milliseconds(), found, strings.Join(seen, " | "))
		}
		report = append(report, line)
	}
	t.Logf("fulltext second projection, %d iterations, %d never found:\n%s", fulltextSecondProjectionRepeats, notFound, strings.Join(report, "\n"))
	if notFound > 0 {
		t.Fatalf("%d of %d subjects written after the index was operational never became searchable", notFound, fulltextSecondProjectionRepeats)
	}
}
