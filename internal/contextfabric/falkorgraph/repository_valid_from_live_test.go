package falkorgraph

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

func repositoryStartBatch(orgID, batchID, cursor, next string, at time.Time, subject contextfabric.SubjectRef, validFrom *time.Time) contextfabric.ProjectionBatch {
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: batchID, OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: cursor, NextCursor: next, GeneratedAt: at,
		Entities: []contextfabric.EntityProjection{{
			Subject: subject, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}},
			EvidenceRefIDs: []string{"evidence_start"}, ObservedAt: at, ValidFrom: validFrom, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

// TestLiveRepositoryValidityStartNeverMovesLater: a repository re-projected
// with a later start (a re-stamped sync) keeps the earlier one, so a read as of
// an instant between the first projection and the re-stamp still finds it; a
// write with no start keeps it; an earlier start lowers it. A work item keeps
// the overwrite and clear semantics of the canonical write.
func TestLiveRepositoryValidityStartNeverMovesLater(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	orgID := "live-repo-start-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, orgID)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	first := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	restamp := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	between := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	earlier := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:start", Label: "acme/start"}
	item := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_start", Label: "start item"}

	steps := 0
	apply := func(subject contextfabric.SubjectRef, at time.Time, validFrom *time.Time) {
		t.Helper()
		steps++
		cursor, next := fmt.Sprintf("cursor-%d", steps-1), fmt.Sprintf("cursor-%d", steps)
		if steps == 1 {
			cursor = ""
		}
		batch := repositoryStartBatch(orgID, "batch_start_"+next, cursor, next, at, subject, validFrom)
		if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
			t.Fatalf("ApplyProjectionBatch() step %d error = %v", steps, err)
		}
	}
	asOf := func(subject contextfabric.SubjectRef, instant time.Time) *node {
		t.Helper()
		filter := newTemporalFilter(contextfabric.TimeContext{Axis: contextfabric.TemporalValidTime, AsOf: &instant})
		found, err := adapter.nodeByKindID(ctx, key, orgID, string(subject.Kind), subject.CanonicalID, filter)
		if err != nil {
			t.Fatalf("nodeByKindID() error = %v", err)
		}
		return found
	}
	startNs := func(subject contextfabric.SubjectRef) interface{} {
		t.Helper()
		found, err := adapter.nodeByKindID(ctx, key, orgID, string(subject.Kind), subject.CanonicalID, temporalFilter{})
		if err != nil || found == nil {
			t.Fatalf("nodeByKindID() = %v, %v", found, err)
		}
		return found.Properties[propValidFromNs]
	}

	apply(repo, first, &first)
	apply(item, first, &first)
	apply(repo, restamp, &restamp)
	apply(item, restamp, &restamp)

	if asOf(repo, between) == nil {
		t.Fatalf("the repository was not found as of %s, between its first projection (%s) and the re-stamp (%s)", between, first, restamp)
	}
	if got := startNs(repo); fmt.Sprint(got) != fmt.Sprint(nsTimestamp(first)) {
		t.Fatalf("repository start after a later start = %v, want %v (the earlier start)", got, nsTimestamp(first))
	}
	if asOf(item, between) != nil {
		t.Fatal("a work item took the later start as before: it must keep the canonical overwrite, but was found before it")
	}

	apply(repo, restamp.Add(time.Hour), nil)
	if got := startNs(repo); fmt.Sprint(got) != fmt.Sprint(nsTimestamp(first)) {
		t.Fatalf("repository start after a write with no start = %v, want %v (kept)", got, nsTimestamp(first))
	}

	apply(repo, restamp.Add(2*time.Hour), &earlier)
	if got := startNs(repo); fmt.Sprint(got) != fmt.Sprint(nsTimestamp(earlier)) {
		t.Fatalf("repository start after an earlier start = %v, want %v (lowered)", got, nsTimestamp(earlier))
	}

	apply(item, restamp.Add(time.Hour), nil)
	if got := startNs(item); got != nil {
		t.Fatalf("work item start after a write with no start = %v, want it cleared", got)
	}
}
