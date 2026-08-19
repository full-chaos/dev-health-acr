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
		RelationshipID: "relationship_merge_1", Type: "BLOCKS", From: project, To: work,
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
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() by previous name error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("previous-name resolution after relationship write = %#v, want the project still resolvable by its entity-set previous name", resolution)
	}
}

// TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization is
// CHAOS-3785 codex round-1 finding F1: projectRelationship writes BOTH
// endpoint stubs using the RELATIONSHIP's own Authorization, not either
// endpoint's own. Same-batch ordering (ApplyProjectionBatch's doc comment:
// relationships before entities) protects same-batch writes, but a
// paged/incremental batch can easily land a subject's real entity write and
// an edge that references it in two DIFFERENT batches -- entity/relationship
// candidates are sorted and capped by (observedAt, sortKey) across every
// producer table (devhealthsource's sortCandidates/truncateToCompleteRows),
// not kept together. When that happens, this proves the earlier batch's
// authoritative authorization must survive the later edge write, not be
// silently replaced by whatever (possibly narrower, possibly mismatched)
// scope the edge itself carries -- exactly the risk CHAOS-3785 introduced by
// giving repo-less work items a non-repository sentinel RepositorySlugs
// value distinct from any real repo scope: an edge from such a work item to
// a genuinely repo-backed subject must never erase that subject's real
// repository authorization.
func TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-authz-downgrade-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	repoBacked := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_repo_backed", Label: "Repo-backed work item"}
	repoLess := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_repo_less", Label: "Repo-less work item"}
	repoBackedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	// noRepositorySentinelValue is devhealthsource's exact
	// noRepositorySentinel constant, pinned here as a literal (codex round-1
	// finding F3) rather than this package importing devhealthsource's
	// unexported value: this proves the LITERAL string CHAOS-3785 actually
	// writes, not merely "some non-empty, non-matching scope" -- a drift
	// between the two would mean this test keeps passing while the real
	// producer's authorization value silently changed underneath it.
	noRepositorySentinelValue := "acr-context-fabric:no-repository"
	edgeScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{noRepositorySentinelValue}}

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_downgrade_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: repoBacked, Authorization: repoBackedScope, EvidenceRefIDs: []string{"evidence_repo_backed"}, ObservedAt: observed, SourceVersion: "v1"},
			// repoLess is projected with the SAME sentinel scope
			// workItemAuthorization would give a real Linear work item --
			// F3's second half: a repo-scoped principal must never see it,
			// while an org-wide principal must.
			{Subject: repoLess, Authorization: edgeScope, EvidenceRefIDs: []string{"evidence_repo_less"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	// A LATER batch (a later projection tick, exactly the paging scenario
	// the doc comment above describes) carries only the edge -- repoBacked's
	// own entity is not re-asserted in this batch, matching a real
	// incremental page where the edge's row sorts after the entity's row
	// from an earlier tick.
	relBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_downgrade_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(time.Minute),
		Entities: []contextfabric.EntityProjection{},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_downgrade_1", Type: "BLOCKS", From: repoLess, To: repoBacked,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: edgeScope, EvidenceRefIDs: []string{"evidence_downgrade_edge"}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, relBatch); err != nil {
		t.Fatalf("relationship ApplyProjectionBatch() error = %v", err)
	}

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{repoBacked.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: repoBacked.Kind, ID: repoBacked.CanonicalID, Label: repoBacked.Label, Source: "live-test"}}

	// The critical assertion: a principal scoped to repoBacked's OWN
	// repository must still see it after the edge write, even though the
	// edge itself carried a completely different scope.
	scopedPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}
	resolution, err := adapter.ResolveSubjects(ctx, scopedPrincipal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != repoBacked {
		t.Fatalf("resolution for a principal scoped to the work item's OWN repository = %#v, want it still admitted -- the later edge write must not have downgraded its authorization from %q to the edge's own scope",
			resolution, repoBackedScope.RepositorySlugs)
	}

	// F3 (codex round-1): the SAME repo-scoped principal must never see the
	// sentinel-scoped repoLess work item -- a real Linear item, unrestricted
	// only for an org-wide principal, must not leak to someone scoped to a
	// specific repository merely because it shares an edge with something
	// that repository DOES own.
	repoLessInterpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{repoLess.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	repoLessRequest := liveInvestigationRequest()
	repoLessRequest.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: repoLess.Kind, ID: repoLess.CanonicalID, Label: repoLess.Label, Source: "live-test"}}

	deniedResolution, err := adapter.ResolveSubjects(ctx, scopedPrincipal, repoLessRequest, repoLessInterpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() for repoLess with a repo-scoped principal error = %v", err)
	}
	if len(deniedResolution.Committed) != 0 {
		t.Fatalf("repo-scoped principal resolution for the sentinel-scoped work item = %#v, want it denied", deniedResolution)
	}

	orgWidePrincipal := storage.Principal{OrgID: orgID}
	admittedResolution, err := adapter.ResolveSubjects(ctx, orgWidePrincipal, repoLessRequest, repoLessInterpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() for repoLess with an org-wide principal error = %v", err)
	}
	if len(admittedResolution.Committed) != 1 || admittedResolution.Committed[0] != repoLess {
		t.Fatalf("org-wide principal resolution for the sentinel-scoped work item = %#v, want it admitted", admittedResolution)
	}
}

// TestLiveContentProjectionNeverDowngradesTheAttachedSubjectsOwnAuthorization
// is CHAOS-3785 codex round-2 finding R2-1: the same endpoint-authorization
// clobber TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization
// proves for relationship endpoints applies equally to projectContent's
// attachment subject -- projectContent does not own whatever real entity a
// content record attaches to, so a later content write carrying a
// different scope must not replace that entity's own authorization.
func TestLiveContentProjectionNeverDowngradesTheAttachedSubjectsOwnAuthorization(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-content-downgrade-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	repoBacked := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_content_target", Label: "Repo-backed work item (content)"}
	repoBackedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	mismatchedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acr-context-fabric:no-repository"}}

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_content_downgrade_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: repoBacked, Authorization: repoBackedScope, EvidenceRefIDs: []string{"evidence_content_target"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	// A LATER batch attaches a content record to repoBacked with a
	// DIFFERENT scope -- projectContent does not own repoBacked.
	contentBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_content_downgrade_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(time.Minute),
		Entities: []contextfabric.EntityProjection{},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "content_downgrade_00000001", Subject: repoBacked, Title: "Attached note", Body: "some body",
			ContentDigest: "digest_downgrade_00000001", Authorization: mismatchedScope, EvidenceRefIDs: []string{"evidence_content_note"},
			ObservedAt: observed.Add(time.Minute), SourceVersion: "v1", Untrusted: true,
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, contentBatch); err != nil {
		t.Fatalf("content ApplyProjectionBatch() error = %v", err)
	}

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{repoBacked.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: repoBacked.Kind, ID: repoBacked.CanonicalID, Label: repoBacked.Label, Source: "live-test"}}

	scopedPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}
	resolution, err := adapter.ResolveSubjects(ctx, scopedPrincipal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != repoBacked {
		t.Fatalf("resolution for a principal scoped to the work item's OWN repository = %#v, want it still admitted -- a later content write must not have downgraded its authorization", resolution)
	}
}

// TestLiveEpisodeProjectionNeverDowngradesTheAttachedSubjectsOwnAuthorization
// is projectEpisode's counterpart to the content test above -- same R2-1
// finding, same reasoning: projectEpisode does not own the subject an
// episode attaches to.
func TestLiveEpisodeProjectionNeverDowngradesTheAttachedSubjectsOwnAuthorization(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-episode-downgrade-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	repoBacked := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_episode_target", Label: "Repo-backed work item (episode)"}
	repoBackedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	mismatchedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acr-context-fabric:no-repository"}}

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_episode_downgrade_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: repoBacked, Authorization: repoBackedScope, EvidenceRefIDs: []string{"evidence_episode_target"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	episodeBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_episode_downgrade_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(time.Minute),
		Entities: []contextfabric.EntityProjection{},
		Episodes: []contextfabric.EpisodeProjection{{
			EpisodeID: "episode_downgrade_00000001", Subject: repoBacked, Goal: "Investigate", Outcome: "resolved", Summary: "did the thing",
			Authorization: mismatchedScope, EvidenceRefIDs: []string{"evidence_episode_note"},
			StartedAt: observed.Add(time.Minute), EndedAt: observed.Add(2 * time.Minute), SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, episodeBatch); err != nil {
		t.Fatalf("episode ApplyProjectionBatch() error = %v", err)
	}

	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{repoBacked.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: repoBacked.Kind, ID: repoBacked.CanonicalID, Label: repoBacked.Label, Source: "live-test"}}

	scopedPrincipal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}
	resolution, err := adapter.ResolveSubjects(ctx, scopedPrincipal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != repoBacked {
		t.Fatalf("resolution for a principal scoped to the work item's OWN repository = %#v, want it still admitted -- a later episode write must not have downgraded its authorization", resolution)
	}
}

// TestLiveRelationshipProjectionNeverOverwritesAnEndpointsOwnLabel is
// CHAOS-3785 codex round-2 finding R2-2: devhealthsource's
// queryWorkItemDependencies/queryWorkItemHierarchy set a relationship
// endpoint's Label to the bare work-item ID, not its title
// (tables.go From/To Label: sourceID/targetID/childID/parentID). A
// relationship-only incremental batch landing after the subject's own
// entity write must not replace that subject's real, human-readable title
// with the ID a dependency/hierarchy edge happens to carry as its Label.
func TestLiveRelationshipProjectionNeverOverwritesAnEndpointsOwnLabel(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-label-downgrade-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	canonicalTitle := "Investigate flaky checkout test"
	titled := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_titled", Label: canonicalTitle}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_other", Label: "WI-999"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}

	entityBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_label_downgrade_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: titled, Authorization: scope, EvidenceRefIDs: []string{"evidence_titled"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, entityBatch); err != nil {
		t.Fatalf("entity ApplyProjectionBatch() error = %v", err)
	}

	// A LATER batch references "titled" by a bare ID Label -- "work_titled"
	// itself, matching devhealthsource's own From/To convention of using
	// the raw work_item_id as Label, never the title.
	idOnlyRef := contextfabric.SubjectRef{Kind: titled.Kind, CanonicalID: titled.CanonicalID, Label: "work_titled"}
	relBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_label_downgrade_2", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed.Add(time.Minute),
		Entities: []contextfabric.EntityProjection{},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_label_downgrade_1", Type: "BLOCKS", From: idOnlyRef, To: other,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_label_edge"}, ObservedAt: observed.Add(time.Minute), SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, relBatch); err != nil {
		t.Fatalf("relationship ApplyProjectionBatch() error = %v", err)
	}

	// Resolve by the CANONICAL title text -- if the edge's bare-ID Label had
	// overwritten it, this hybrid-search term would no longer match anything.
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{canonicalTitle},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := liveInvestigationRequest()
	resolution, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: orgID}, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() by canonical title error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != titled {
		t.Fatalf("resolution by canonical title after a bare-ID-labeled edge write = %#v, want the subject still resolvable by its real title (label must not have been overwritten with the edge's ID label)", resolution)
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
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
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
	resolution, err := adapter.ResolveSubjects(ctx, allowed, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() owner-wildcard error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("owner-wildcard resolution = %#v, want the project admitted for acme/*", resolution)
	}

	denied := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"other-owner/*"}}
	deniedResolution, err := adapter.ResolveSubjects(ctx, denied, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() unrelated-owner error = %v", err)
	}
	if len(deniedResolution.Committed) != 0 {
		t.Fatalf("unrelated-owner resolution = %#v, want no unsafe widening", deniedResolution)
	}
}

// TestLiveDiscoverContextEnforcesAuthorizationOnPathsAndAttributionEdges is
// the Codex P1 probe: a restricted principal with an authorized origin
// subject must never receive (a) a relationship path whose OWN authorization
// attributes it cannot see, even when both the path's endpoints are
// independently visible to it, or (b) a DOCUMENTED_BY attribution edge whose
// underlying content is scoped to a repository the principal cannot see.
// Before this fix, DiscoverContext applied no Go-side authorization check at
// all to traversed edges or their endpoints (reader.go's old doc comment
// even asserted the opposite: "falkorgraph never needs a second-hop verify
// step"), so both leaks were live.
func TestLiveDiscoverContextEnforcesAuthorizationOnPathsAndAttributionEdges(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-authz-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_authz", Label: "Authz Project"}
	visibleWork := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_visible", Label: "Visible Work"}
	restrictedWork := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_restricted", Label: "Restricted Work"}
	allowedScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/allowed"}}
	privateScope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/private"}}

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_authz_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: project, Authorization: allowedScope, EvidenceRefIDs: []string{"evidence_project"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: visibleWork, Authorization: allowedScope, EvidenceRefIDs: []string{"evidence_visible_work"}, ObservedAt: observed, SourceVersion: "v1"},
			// restrictedWork itself is authorized to "acme/allowed" (visible
			// on its own) -- only the RELATIONSHIP to it is scoped private,
			// so this proves edge-level authorization is checked
			// independently of both endpoints' own authorization.
			{Subject: restrictedWork, Authorization: allowedScope, EvidenceRefIDs: []string{"evidence_restricted_work"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{
			{
				RelationshipID: "rel_visible", Type: "BLOCKS", From: project, To: visibleWork,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization: allowedScope, EvidenceRefIDs: []string{"evidence_dep_visible"}, ObservedAt: observed, SourceVersion: "v1",
			},
			{
				RelationshipID: "rel_restricted", Type: "BLOCKS", From: project, To: restrictedWork,
				Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
				Authorization: privateScope, EvidenceRefIDs: []string{"evidence_dep_restricted"}, ObservedAt: observed, SourceVersion: "v1",
			},
		},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "content_private_1", Subject: project, Title: "Private design note", Body: "sensitive body text",
			ContentDigest: "digest_content_private_1_sha256", Authorization: privateScope, EvidenceRefIDs: []string{"evidence_content_private"},
			ObservedAt: observed, SourceVersion: "v1", Untrusted: true,
		}},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/allowed"}}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "live-test"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "dependencies", SubjectTerms: []string{project.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
	}
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("origin resolution = %#v, want the project resolvable (its own scope IS visible to the restricted principal)", resolution)
	}

	discovery := contextfabric.GraphDiscoveryRequest{Request: request, Interpretation: interpreted, Resolution: resolution}
	result, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	sawVisibleDependency := false
	for pathIndex := range result.Paths {
		for edgeIndex := range result.Paths[pathIndex].Edges {
			edge := result.Paths[pathIndex].Edges[edgeIndex]
			if edge.Type == "BLOCKS" && edge.To == restrictedWork {
				t.Fatalf("unauthorized relationship leaked into the result despite both endpoints being individually visible: %#v", edge)
			}
			if edge.Type == "BLOCKS" && edge.To == visibleWork {
				sawVisibleDependency = true
			}
			if edge.Type == "DOCUMENTED_BY" {
				t.Fatalf("unauthorized attribution edge leaked into the result: %#v", edge)
			}
		}
	}
	if !sawVisibleDependency {
		t.Fatalf("authorized BLOCKS relationship missing -- authorization filtering must not exclude paths the principal IS scoped to see: %#v", result.Paths)
	}
}

// TestLivePartOfEdgeRoundTripsFromProjectionThroughDiscoverContext is
// CHAOS-3779 codex round-1 finding L6's integration-shaped proof for
// PART_OF: a projected relationship -> ApplyProjectionBatch (real
// FalkorDB) -> DiscoverContext (real FalkorDB read), end to end, against
// an actual backend rather than the fakeConn unit tests already covering
// PART_OF's admission/direction logic in isolation
// (TestAdmitEdgesAnswersBlocksAndPartOfInOneHopWithEvidence, graphrank
// package -- pure, no I/O) and its deterministic source-row mapping in
// isolation (TestClickHouseProjectionSourceProjectsEveryClosedVocabularyRelationshipType,
// devhealthsource package -- fakeClient, no real ClickHouse or FalkorDB).
// This is the missing middle: does a PART_OF edge, once actually written
// to a real graph, actually come back out through the real read path with
// its evidence intact? A fakeConn-based ApplyProjectionBatch harness was
// considered impractical to build with confidence here: unlike
// DiscoverContext's read-only queries (already fake-tested extensively
// elsewhere in this package), ApplyProjectionBatch's write path also
// exercises bootstrapSchema's constraint/index polling, which no existing
// fake test drives end to end -- a live container is the honest way to
// prove this round trip rather than a first-time, unexecuted fake
// harness for the write path.
func TestLivePartOfEdgeRoundTripsFromProjectionThroughDiscoverContext(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-part-of-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	child := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_child", Label: "Child Work"}
	parent := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_parent", Label: "Parent Work"}
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}}

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_part_of_1", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{
			{Subject: child, Authorization: scope, EvidenceRefIDs: []string{"evidence_child"}, ObservedAt: observed, SourceVersion: "v1"},
			{Subject: parent, Authorization: scope, EvidenceRefIDs: []string{"evidence_parent"}, ObservedAt: observed, SourceVersion: "v1"},
		},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "rel_part_of_1", Type: "PART_OF", From: child, To: parent,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_hierarchy_child_parent"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: child.Kind, ID: child.CanonicalID, Label: child.Label, Source: "live-test"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "hierarchy", SubjectTerms: []string{child.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactRequiredChildren}},
	}
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != child {
		t.Fatalf("origin resolution = %#v, want the child work item resolvable", resolution)
	}
	discovery := contextfabric.GraphDiscoveryRequest{Request: request, Interpretation: interpreted, Resolution: resolution}
	result, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}

	edge := findLiveEdge(result.Paths, "PART_OF")
	if edge == nil {
		t.Fatalf("PART_OF edge did not round-trip through ApplyProjectionBatch -> DiscoverContext: %#v", result.Paths)
	}
	if edge.From != child || edge.To != parent {
		t.Fatalf("PART_OF edge = %s -> %s, want work_child -> work_parent", edge.From.CanonicalID, edge.To.CanonicalID)
	}
	if len(edge.EvidenceRefIDs) == 0 || edge.EvidenceRefIDs[0] != "evidence_hierarchy_child_parent" {
		t.Fatalf("PART_OF edge evidence = %#v, want [evidence_hierarchy_child_parent] to have survived the round trip", edge.EvidenceRefIDs)
	}
}
