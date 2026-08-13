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
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
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
	resolution, err := adapter.ResolveSubjects(ctx, principal, request, interpreted)
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
