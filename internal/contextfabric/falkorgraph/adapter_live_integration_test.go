package falkorgraph_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// falkordbImage is pinned by digest to the exact image/module version this
// adapter was developed and verified against (FalkorDB 4.20.2, module ver
// 42002, Redis 8.6.3) -- see docs/design/context-fabric-falkordb-adapter.md
// §1.1. Update deliberately, not silently, if the pin ever moves.
const falkordbImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// newLiveAdapter starts a real FalkorDB container (no env gate, no skip --
// this repository's established testcontainers convention, matching
// pgprojection and devhealthsource's ClickHouse integration tests) and
// returns a falkorgraph.Adapter connected to it. CHAOS-3752 was blocked on a
// live-service proof for its entire history solely because Zep Cloud needed
// an external credential nothing in this repository's CI or local dev could
// supply; FalkorDB needs none, so this test simply always runs.
func newLiveAdapter(t *testing.T, ctx context.Context) *falkorgraph.Adapter {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: falkordbImage, ExposedPorts: []string{"6379/tcp"},
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
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-test", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
	})
	require.NoError(t, err, "construct falkorgraph.Adapter")
	return adapter
}

func liveBatch(orgID string) contextfabric.ProjectionBatch {
	observed := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	validFrom := observed.Add(-time.Hour)
	validTo := observed.Add(time.Hour)
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	work := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_release", Label: "Release acceptance"}
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_live_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{{
			Subject: project, Aliases: []string{"AskDev"}, ProviderIDs: map[string]string{"linear": "project-1"},
			Properties:     map[string]contextfabric.ScalarValue{"lifecycle": {String: ptrString("active")}},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_project_1234"}, ObservedAt: observed, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{{
			RelationshipID: "relationship_00000001", Type: "BLOCKS", From: project, To: work,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"full-chaos/dev-health-acr"}},
			EvidenceRefIDs: []string{"evidence_dependency_1234"}, ObservedAt: observed, ValidFrom: &validFrom, ValidTo: &validTo, SourceVersion: "v1",
		}},
		Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
}

func liveInvestigationRequest() contextfabric.InvestigationRequest {
	return contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_00000001",
		Question: "What is Ask Dev blocked by?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
}

// TestLiveFalkorDBContextFabricLifecycle proves the full CHAOS-3752 adapter
// contract against a real FalkorDB server: (1) project canonical
// entities/relationships in a single batch, (2) replay the same batch to
// prove idempotency, (3) resolve the projected subject by exact hint and by
// hybrid full-text search, (4) discover context and verify the projected
// relationship's temporal/evidence metadata survived the round trip, (5)
// tombstone the relationship and confirm it no longer surfaces, (6) read the
// projection watermark, (7) verify organization isolation against a second
// live org, (8) purge the organization graph (idempotent re-purge), (9)
// prove purging one org never affects another, and (10) prove a write after
// purge re-bootstraps the schema correctly (only read paths use
// GRAPH.RO_QUERY, which does not auto-create -- see TestLiveReadOnlyPath
// AfterPurgeReturnsNotFoundWithoutAutoCreating for that half; writes must
// still succeed via GRAPH.QUERY's auto-create).
//
// Unlike zepgraph.TestLiveZepContextFabricLifecycle, this test is not
// env-gated: FalkorDB needs no external credential, so CHAOS-3752's
// live-service proof runs in ordinary CI, not only on demand.
func TestLiveFalkorDBContextFabricLifecycle(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-contract-a-" + stamp
	otherOrgID := "live-contract-b-" + stamp
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), otherOrgID) })

	batch := liveBatch(orgID)
	project := batch.Entities[0].Subject
	principal := storage.Principal{OrgID: orgID}

	// (1)/(2)
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("(1) ApplyProjectionBatch() error = %v", err)
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("(2) idempotent replay ApplyProjectionBatch() error = %v", err)
	}

	// (3)
	request := liveInvestigationRequest()
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: project.Kind, ID: project.CanonicalID, Label: project.Label, Source: "live-test"}}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{project.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("(3) exact-hint ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != project {
		t.Fatalf("(3) exact-hint resolution = %#v", resolution)
	}
	hybridRequest := request
	hybridRequest.RequestedScope.SubjectHints = nil
	hybridResolution, _, err := adapter.ResolveSubjects(ctx, principal, hybridRequest, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("(3) hybrid ResolveSubjects() error = %v", err)
	}
	if len(hybridResolution.Committed) != 1 || hybridResolution.Committed[0] != project {
		t.Fatalf("(3) hybrid resolution = %#v", hybridResolution)
	}

	// (4)
	discoveryRequest := request
	discoveryRequest.Question = "What is Ask Dev blocked by?"
	discovery := contextfabric.GraphDiscoveryRequest{
		Request: discoveryRequest,
		Interpretation: contextfabric.InterpretedQuestion{
			Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "dependencies", SubjectTerms: []string{project.Label},
			TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactBlockers}},
		},
		Resolution: resolution,
	}
	graphContext, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("(4) DiscoverContext() error = %v", err)
	}
	dependencyEdge := findLiveEdge(graphContext.Paths, "BLOCKS")
	if dependencyEdge == nil {
		t.Fatalf("(4) DiscoverContext() did not surface the projected BLOCKS relationship: %#v", graphContext.Paths)
	}
	if dependencyEdge.ValidFrom == nil || dependencyEdge.ValidTo == nil || len(dependencyEdge.EvidenceRefIDs) == 0 || dependencyEdge.EvidenceRefIDs[0] != "evidence_dependency_1234" {
		t.Fatalf("(4) relationship temporal/evidence metadata = %#v", dependencyEdge)
	}

	// (5)
	tombstoneBatch := batch
	tombstoneBatch.BatchID = "batch_live_tombstone_00000001"
	tombstoneBatch.Cursor = batch.NextCursor
	tombstoneBatch.NextCursor = "cursor-3"
	tombstoneBatch.Entities = []contextfabric.EntityProjection{}
	tombstoneBatch.Relationships = []contextfabric.RelationshipProjection{}
	tombstoneBatch.Tombstones = []contextfabric.ProjectionTombstone{
		{Kind: "relationship", CanonicalID: batch.Relationships[0].RelationshipID, Reason: "live contract test cleanup", EffectiveAt: time.Now().UTC(), SourceVersion: "v1"},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, tombstoneBatch); err != nil {
		t.Fatalf("(5) tombstone ApplyProjectionBatch() error = %v", err)
	}
	afterTombstone, err := adapter.DiscoverContext(ctx, principal, discovery)
	if err != nil {
		t.Fatalf("(5) DiscoverContext() after tombstone error = %v", err)
	}
	if edge := findLiveEdge(afterTombstone.Paths, "BLOCKS"); edge != nil {
		t.Fatalf("(5) tombstoned relationship still surfaced: %#v", edge)
	}

	// (6)
	watermark, err := adapter.ProjectionWatermark(ctx, orgID, batch.Source)
	if err != nil {
		t.Fatalf("(6) ProjectionWatermark() error = %v", err)
	}
	if watermark.Cursor != tombstoneBatch.NextCursor || watermark.SourceVersion != tombstoneBatch.SourceVersion {
		t.Fatalf("(6) watermark = %#v", watermark)
	}

	// (7)
	otherBatch := liveBatch(otherOrgID)
	if _, err := adapter.ApplyProjectionBatch(ctx, otherBatch); err != nil {
		t.Fatalf("(7) cross-org ApplyProjectionBatch() error = %v", err)
	}
	crossOrgResolution, _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: otherOrgID}, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("(7) cross-org ResolveSubjects() error = %v", err)
	}
	if len(crossOrgResolution.Committed) != 1 || crossOrgResolution.Committed[0] != project {
		t.Fatalf("(7) cross-org resolution = %#v", crossOrgResolution)
	}

	// (8)
	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("(8) PurgeOrganization() error = %v", err)
	}
	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("(8) repeat PurgeOrganization() on an absent graph error = %v", err)
	}

	// (9)
	survivingResolution, _, err := adapter.ResolveSubjects(ctx, storage.Principal{OrgID: otherOrgID}, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("(9) surviving-org ResolveSubjects() error = %v", err)
	}
	if len(survivingResolution.Committed) != 1 {
		t.Fatalf("(9) purging one organization affected another: %#v", survivingResolution)
	}

	// (10): a write after purge must re-bootstrap the schema and succeed --
	// writes go through GRAPH.QUERY (auto-creates), never GRAPH.RO_QUERY.
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("(10) ApplyProjectionBatch() after purge did not re-bootstrap: %v", err)
	}
	rebootstrapped, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{})
	if err != nil {
		t.Fatalf("(10) ResolveSubjects() after re-bootstrap error = %v", err)
	}
	if len(rebootstrapped.Committed) != 1 || rebootstrapped.Committed[0] != project {
		t.Fatalf("(10) resolution after re-bootstrap = %#v", rebootstrapped)
	}

	if err := adapter.PurgeOrganization(ctx, otherOrgID); err != nil {
		t.Fatalf("cleanup PurgeOrganization() error = %v", err)
	}
}

// TestLiveReadOnlyPathAfterPurgeReturnsNotFoundWithoutAutoCreating proves the
// design decision this adapter relies on for the CHAOS-3752 auto-create-on-
// read hazard (docs/design/context-fabric-falkordb-adapter.md §1.3(3)):
// every read this adapter issues goes through GRAPH.RO_QUERY, which --
// unlike GRAPH.QUERY -- does NOT silently create an absent graph key. A read
// against an organization that was purged (or never projected at all) must
// report ErrNotFound, never fabricate an empty graph.
func TestLiveReadOnlyPathAfterPurgeReturnsNotFoundWithoutAutoCreating(t *testing.T) {
	adapter := newLiveAdapter(t, context.Background())
	ctx := context.Background()
	orgID := "live-ro-path-" + time.Now().UTC().Format("20060102T150405.000000000")

	// Never projected at all: a read-only lookup must not fabricate a graph.
	_, err := adapter.ProjectionWatermark(ctx, orgID, "live-test")
	if !errors.Is(err, falkorgraph.ErrNotFound) {
		t.Fatalf("watermark read for a never-projected org = %v, want ErrNotFound", err)
	}
	// CHAOS-3882: the same error must ALSO satisfy the backend-neutral
	// classification projectionrun.Coordinator's checkpoint-vs-store
	// divergence check relies on, so a backend-agnostic caller can recognize
	// a confirmed absence without importing this package directly.
	if !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) {
		t.Fatalf("watermark read for a never-projected org = %v, want it to also satisfy contextfabric.ErrProjectionWatermarkNotFound", err)
	}

	batch := liveBatch(orgID)
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}
	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("PurgeOrganization() error = %v", err)
	}

	// Purged: a read-only lookup must report not-found, not a generic
	// dependency error and not a silently-empty result from an
	// auto-created graph. This is the exact shape of the CHAOS-3882
	// incident's second half: a batch WAS durably applied here (so a
	// Postgres checkpoint recording that would carry a real
	// BackendWatermark), and the backend's own state is now confirmed gone.
	_, err = adapter.ProjectionWatermark(ctx, orgID, "live-test")
	if !errors.Is(err, falkorgraph.ErrNotFound) {
		t.Fatalf("watermark read after purge = %v, want ErrNotFound", err)
	}
	if !errors.Is(err, contextfabric.ErrProjectionWatermarkNotFound) {
		t.Fatalf("watermark read after purge = %v, want it to also satisfy contextfabric.ErrProjectionWatermarkNotFound", err)
	}
}

func findLiveEdge(paths []contextfabric.RelationshipPath, relationType contextfabric.RelationshipType) *contextfabric.RelationshipEdge {
	for pathIndex := range paths {
		for edgeIndex := range paths[pathIndex].Edges {
			if paths[pathIndex].Edges[edgeIndex].Type == relationType {
				return &paths[pathIndex].Edges[edgeIndex]
			}
		}
	}
	return nil
}

func ptrString(value string) *string { return &value }
