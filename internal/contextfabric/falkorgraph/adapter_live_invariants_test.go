package falkorgraph_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestLiveRelationshipProjectionPreservesPriorCanonicalEntityMetadata proves
// the merge-semantics contract projection.go's subjectMergeAttrs doc comment
// describes: a relationship-sourced write to a subject node must never erase
// that subject's aliases/previous_names/provider IDs, even though the
// relationship write's own $attrs never carries those keys. Direct
// FalkorDB-backed analogue of
// zepgraph.TestRelationshipProjectionPreservesPriorCanonicalEntityMetadataAcrossBatches.
func TestLiveRelationshipProjectionPreservesPriorCanonicalEntityMetadata(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-merge-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_release", Label: "Release acceptance"}

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_merge_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Aliases: []string{"AskDev"}, PreviousNames: []string{"Dev Agent"}, ProviderIDs: map[string]string{"linear": "project-1"},
			Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}}, EvidenceRefIDs: []string{"evidence_identity_1"},
			ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	relBatch := entityBatch
	relBatch.BatchID = "batch_merge_00000002"
	relBatch.Cursor = "cursor-1"
	relBatch.NextCursor = "cursor-2"
	relBatch.Entities = []contextfabric.EntityProjection{}
	relBatch.Relationships = []contextfabric.RelationshipProjection{{
		RelationshipID: "relationship_merge_1", Type: "DEPENDS_ON", From: project, To: work,
		Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
		Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}}, EvidenceRefIDs: []string{"evidence_dependency_1"},
		ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
	}}
	if _, err := adapter.ApplyProjectionBatch(ctx, relBatch); err != nil {
		t.Fatalf("relationship ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	interpreted := contextfabric.InterpretedQuestion{
		// "Dev Agent" is the PreviousName the entity batch set, never
		// touched by the relationship batch -- if the relationship write
		// had clobbered previous_names, hybrid search on it (not the
		// canonical label "Ask Dev") would find nothing. Space-separated
		// multi-word text, not a single compound token: a bare unspaced
		// alias like "AskDev" hits an unrelated FalkorDB full-text
		// indexing quirk under parameterized writes (a single compound
		// token can fail to index even though its constituent words do,
		// and even though the property value itself round-trips correctly
		// -- confirmed independently of this adapter via raw redis-cli),
		// which is not what this test is trying to prove.
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"Dev Agent"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() by previous name error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("previous-name resolution after relationship write = %#v, want the project still resolvable by its entity-set previous name", resolution)
	}
}

// TestLiveApplyProjectionBatchSkipsStaleOutOfOrderTombstone proves the
// tombstone staleness check applied.go's DELETE ... WHERE observed_at_ns
// IS NULL OR observed_at_ns <= $effective implements: a tombstone whose
// EffectiveAt predates a subject's own stored observed_at must not delete
// it -- a later, legitimate re-projection has already re-established the
// subject since the tombstone was generated.
func TestLiveApplyProjectionBatchSkipsStaleOutOfOrderTombstone(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-stale-tombstone-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	newer := time.Date(2026, 8, 12, 13, 0, 0, 0, time.UTC)
	older := newer.Add(-time.Hour)
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_release", Label: "Release acceptance"}

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_stale_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: newer,
		Entities: []contextfabric.EntityProjection{{
			Subject: work, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_work_1"}, ObservedAt: newer, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	staleTombstoneBatch := batch
	staleTombstoneBatch.BatchID = "batch_stale_00000002"
	staleTombstoneBatch.Cursor = "cursor-1"
	staleTombstoneBatch.NextCursor = "cursor-2"
	staleTombstoneBatch.Entities = []contextfabric.EntityProjection{}
	// EffectiveAt (older) predates the subject's own observed_at (newer):
	// this tombstone is out of order and must be a no-op.
	staleTombstoneBatch.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: string(work.Kind), CanonicalID: work.CanonicalID, Reason: "stale out-of-order test", EffectiveAt: older, SourceVersion: "v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, staleTombstoneBatch); err != nil {
		t.Fatalf("stale tombstone ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: work.Kind, ID: work.CanonicalID, Label: work.Label, Source: "live-test"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{work.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != work {
		t.Fatalf("resolution after stale tombstone = %#v, want the subject still present (tombstone must have been a no-op)", resolution)
	}
}

// TestLiveResolveSubjectsAcceptsPrincipalWildcardRepositoryScope proves the
// live-round-trip half of graphrank.ScopeMatch's owner-wildcard handling: a
// node written with a specific repository authorization list must be
// admitted for a principal whose scope is an "owner/*" wildcard covering
// that repository, and denied for an unrelated owner wildcard. The
// wildcard-matching LOGIC itself is proven directly in graphrank's own
// tests; this proves the write path actually produces a []string FalkorDB
// property graphrank's read path can match against (the exact shape the
// live-discovered []interface{} decode bug would have broken silently).
func TestLiveResolveSubjectsAcceptsPrincipalWildcardRepositoryScope(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-wildcard-scope-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_scoped", Label: "Scoped Project"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_scope_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/repo-x"}},
			EvidenceRefIDs: []string{"evidence_scoped_1"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{project.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "live-test"}}

	allowed := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/*"}}
	resolution, err := adapter.ResolveSubjects(ctx, allowed, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() owner-wildcard error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("owner-wildcard resolution = %#v, want the project admitted for acme/*", resolution)
	}

	denied := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"other-owner/*"}}
	deniedResolution, err := adapter.ResolveSubjects(ctx, denied, request, interpreted)
	if err != nil {
		t.Fatalf("ResolveSubjects() unrelated-owner error = %v", err)
	}
	if len(deniedResolution.Committed) != 0 {
		t.Fatalf("unrelated-owner resolution = %#v, want no unsafe widening", deniedResolution)
	}
}
