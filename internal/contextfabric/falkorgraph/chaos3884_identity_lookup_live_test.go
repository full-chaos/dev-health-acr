package falkorgraph_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// newLiveAdapterWithIdentityUniverse mirrors newLiveAdapter (adapter_live_integration_test.go)
// exactly, adding the CHAOS-3884 IdentityUniverse/Telemetry config a plain
// newLiveAdapter leaves unset -- a separate constructor rather than
// widening newLiveAdapter's own signature, so every pre-CHAOS-3884 live
// test stays untouched.
func newLiveAdapterWithIdentityUniverse(t *testing.T, ctx context.Context, identityUniverse func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error), telemetry falkorgraph.GraphTelemetry) *falkorgraph.Adapter {
	t.Helper()
	return newLiveAdapterWithIdentityUniverseAndTracer(t, ctx, identityUniverse, telemetry, nil)
}

// newLiveAdapterWithIdentityUniverseAndTracer is
// newLiveAdapterWithIdentityUniverse plus an optional ResolutionTracer --
// a separate constructor (rather than widening the 3-arg one and updating
// its existing call sites) so every pre-existing test in this file keeps
// its own call site untouched. tracer may be nil (byte-identical to the
// 3-arg constructor in that case).
func newLiveAdapterWithIdentityUniverseAndTracer(t *testing.T, ctx context.Context, identityUniverse func(ctx context.Context, orgID string) ([]graphrank.IdentityRow, time.Time, bool, error), telemetry falkorgraph.GraphTelemetry, tracer graphrank.ResolutionTracer) *falkorgraph.Adapter {
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
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-live-identity-test", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false,
		IdentityUniverse: identityUniverse, Telemetry: telemetry, ResolutionTracer: tracer,
	})
	require.NoError(t, err, "construct falkorgraph.Adapter")
	return adapter
}

// fakeResolutionTracer is a minimal package-external graphrank.ResolutionTracer
// double for this file's own tests -- graphrank's own captureResolutionTracer
// (tracer_test.go) is unexported and this file lives in the external
// falkorgraph_test package, mirroring fakeIdentityTelemetry's own rationale
// immediately below.
type fakeResolutionTracer struct {
	mu     sync.Mutex
	events []graphrank.ResolutionTraceEvent
}

func (f *fakeResolutionTracer) Trace(event graphrank.ResolutionTraceEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
}

func (f *fakeResolutionTracer) eventsForStage(stage string) []graphrank.ResolutionTraceEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []graphrank.ResolutionTraceEvent
	for _, event := range f.events {
		if event.Stage == stage {
			out = append(out, event)
		}
	}
	return out
}

// fakeIdentityTelemetry is a minimal falkorgraph.GraphTelemetry double for
// this file's own tests -- a package-local fake rather than reusing
// falkorgraph's internal recordingTelemetry (vector_test.go, package
// falkorgraph, unexported), since this file lives in the external
// falkorgraph_test package and can only see falkorgraph's EXPORTED
// GraphTelemetry interface.
type fakeIdentityTelemetry struct {
	identityGraphMissing int
}

func (f *fakeIdentityTelemetry) RecordObservationTraversalDegraded(context.Context, string, int)    {}
func (f *fakeIdentityTelemetry) RecordVectorRetrievalDegraded(context.Context, string)              {}
func (f *fakeIdentityTelemetry) RecordVectorRetrievalSuppressed(context.Context, string)            {}
func (f *fakeIdentityTelemetry) RecordVectorProjection(context.Context, string, int, int, int, int) {}
func (f *fakeIdentityTelemetry) RecordVectorProjectionEmbedFailuresEscalated(context.Context, string, int, bool) {
}
func (f *fakeIdentityTelemetry) RecordVectorIndexEfRuntimeMismatch(context.Context, string, int, int) {
}
func (f *fakeIdentityTelemetry) RecordIdentityGraphMissing(_ context.Context, _ string, count int) {
	f.identityGraphMissing += count
}
func (f *fakeIdentityTelemetry) RecordVectorFence(context.Context, string, falkorgraph.VectorFenceResult, bool) {
}
func (f *fakeIdentityTelemetry) RecordLexiconExpansion(context.Context, string, bool, int, int, bool) {
}
func (f *fakeIdentityTelemetry) RecordSubjectCandidatesAuthzDropped(context.Context, string, int) {}
func (f *fakeIdentityTelemetry) RecordCohortMembersAuthzDropped(context.Context, string, int)     {}
func (f *fakeIdentityTelemetry) RecordEdgesFilteredByReason(context.Context, string, int, int, int) {
}
func (f *fakeIdentityTelemetry) RecordCohortDeniedByAuthorization(context.Context, string, int) {}
func (f *fakeIdentityTelemetry) RecordCohortExactNameCensusGate(context.Context, string, bool, falkorgraph.CohortExactNameCensusBasis) {
}

func (f *fakeIdentityTelemetry) RecordCohortKindBasis(_ context.Context, _ string, _ contextfabric.SubjectKind, _ graphrank.CohortKindBasis, _ bool, _ CohortPoolTruncationBasis) {
}

// TestLiveAliasIdentityFastPathCommitsAUniqueClaimant is CHAOS-3884 Option
// C's live end-to-end proof: a repository entity projected into a real
// FalkorDB graph, matched by a fake (deterministic) IdentityUniverse
// returning exactly its own IdentityRow for the query term, commits via
// the identity fast path -- including the real graph existence check
// (nodeByKindID) round-tripping the projected node.
func TestLiveAliasIdentityFastPathCommitsAUniqueClaimant(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-identity-a-" + stamp
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:live-repo-1", Label: "full-chaos/dev-health-acr"}

	identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		if calledOrgID != orgID {
			return nil, time.Time{}, false, nil
		}
		return []graphrank.IdentityRow{{
			Kind: repo.Kind, CanonicalID: repo.CanonicalID, Label: repo.Label,
			Aliases: []string{"dev-health-acr"}, ObservedAt: time.Now().UTC(),
		}}, time.Now().UTC(), true, nil
	}
	adapter := newLiveAdapterWithIdentityUniverse(t, ctx, identityUniverse, falkorgraph.NoopTelemetry{})
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_00000001", OrgID: orgID, Source: "live-identity-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
		Entities: []contextfabric.EntityProjection{{
			Subject: repo, Aliases: []string{"dev-health-acr"},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repo.Label}},
			EvidenceRefIDs: []string{"evidence_repo_identity_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"dev-health-acr"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0] != repo {
		t.Fatalf("resolution.Committed = %#v, want exactly [%#v] via the identity fast path", resolution.Committed, repo)
	}
}

// TestLiveAliasIdentityCollisionNeverCommits is the collision-as-normal
// live proof: two DIFFERENT projected subjects both claiming the same
// alias must never auto-commit -- both survive to ambiguous/clarification.
func TestLiveAliasIdentityCollisionNeverCommits(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-identity-b-" + stamp
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:live-repo-2", Label: "full-chaos/chaos-ops"}
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:live-team-2", Label: "Chaos Team"}

	identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		if calledOrgID != orgID {
			return nil, time.Time{}, false, nil
		}
		return []graphrank.IdentityRow{
			{Kind: repo.Kind, CanonicalID: repo.CanonicalID, Label: repo.Label, Aliases: []string{"chaos-ops"}, ObservedAt: time.Now().UTC()},
			{Kind: team.Kind, CanonicalID: team.CanonicalID, Label: team.Label, Aliases: []string{"chaos-ops"}, ObservedAt: time.Now().UTC()},
		}, time.Now().UTC(), true, nil
	}
	adapter := newLiveAdapterWithIdentityUniverse(t, ctx, identityUniverse, falkorgraph.NoopTelemetry{})
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_00000002", OrgID: orgID, Source: "live-identity-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
		Entities: []contextfabric.EntityProjection{
			{
				Subject: repo, Aliases: []string{"chaos-ops"},
				Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repo.Label}},
				EvidenceRefIDs: []string{"evidence_repo_collision_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
			},
			{
				Subject: team, Aliases: []string{"chaos-ops"},
				Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"live-team-2"}},
				EvidenceRefIDs: []string{"evidence_team_collision_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
			},
		},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"*"}}
	request := liveInvestigationRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"chaos-ops"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING -- a genuine collision must never silently pick", resolution.Committed)
	}
}

// TestLiveAliasIdentityGraphMissingClaimantIsExcludedAndReported proves the
// existence-check exclusion end to end: an IdentityUniverse claimant that
// was NEVER projected into the graph is excluded from candidacy (never
// fabricated from source-table data alone) and reported via
// RecordIdentityGraphMissing, without the resolution erroring.
func TestLiveAliasIdentityGraphMissingClaimantIsExcludedAndReported(t *testing.T) {
	ctx := context.Background()
	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	orgID := "live-identity-c-" + stamp
	ghost := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:never-projected", Label: "full-chaos/ghost-repo"}

	identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
		if calledOrgID != orgID {
			return nil, time.Time{}, false, nil
		}
		return []graphrank.IdentityRow{{
			Kind: ghost.Kind, CanonicalID: ghost.CanonicalID, Label: ghost.Label,
			Aliases: []string{"ghost-repo"}, ObservedAt: time.Now().UTC(),
		}}, time.Now().UTC(), true, nil
	}
	telemetry := &fakeIdentityTelemetry{}
	adapter := newLiveAdapterWithIdentityUniverse(t, ctx, identityUniverse, telemetry)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

	// Seed one UNRELATED entity first, purely so this organization's graph
	// key actually exists (GRAPH.QUERY auto-creates on write; a read-only
	// lookup against a NEVER-written key errors ErrNotFound at the
	// key-existence level, a different, pre-existing condition this test
	// is not about) -- this is also the more realistic shape of
	// "projection lag" the existence check is meant to catch: an
	// otherwise-live, actively-projected graph missing ONE claimant, not a
	// graph that has never been touched at all.
	seedBatch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_00000003", OrgID: orgID, Source: "live-identity-test",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
		Entities: []contextfabric.EntityProjection{{
			Subject:        contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:unrelated-seed", Label: "Unrelated Seed Project"},
			Authorization:  contextfabric.AuthorizationScope{ProjectIDs: []string{"unrelated-seed"}},
			EvidenceRefIDs: []string{"evidence_seed_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, seedBatch); err != nil {
		t.Fatalf("seed ApplyProjectionBatch() error = %v", err)
	}

	principal := storage.Principal{OrgID: orgID}
	request := liveInvestigationRequest()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"ghost-repo"},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if err != nil {
		t.Fatalf("ResolveSubjects(nil) error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING -- a graph-missing claimant must never be fabricated into a candidate", resolution.Committed)
	}
	if telemetry.identityGraphMissing != 1 {
		t.Fatalf("telemetry.identityGraphMissing = %d, want 1 (RecordIdentityGraphMissing fired for the ghost claimant)", telemetry.identityGraphMissing)
	}
}

// TestLiveAliasIdentityUniverseCompletenessIsTracedThroughTheWiredComposition
// is chris's 2026-08-17 "turn the silent truncation into a counted, visible
// event" instrumentation, proven through the REAL falkorgraph/reader.go
// closure -- not a graphrank-level fake AliasLookup, which can only prove
// resolve.go's own wiring and has no way to reach reader.go's emission at
// all (the raw, pre-graphMissing-fold completeness value exists ONLY as a
// local inside that closure). Two subtests, one live adapter/org each:
// complete=true (the ordinary case) and complete=false (a fake
// IdentityUniverse standing in for fetchIdentityKind hitting
// identityUniverseRowBudget -- this test controls the returned bool
// directly rather than generating 20,001 rows, which would prove nothing
// this file's other tests don't already cover about row-budget arithmetic
// itself; devhealthsource owns that unit test).
func TestLiveAliasIdentityUniverseCompletenessIsTracedThroughTheWiredComposition(t *testing.T) {
	for _, tc := range []struct {
		name     string
		complete bool
	}{
		{name: "complete", complete: true},
		{name: "truncated", complete: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			stamp := time.Now().UTC().Format("20060102T150405.000000000")
			orgID := "live-identity-universe-trace-" + tc.name + "-" + stamp
			repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:live-repo-trace-" + tc.name, Label: "full-chaos/trace-" + tc.name}

			identityUniverse := func(ctx context.Context, calledOrgID string) ([]graphrank.IdentityRow, time.Time, bool, error) {
				if calledOrgID != orgID {
					return nil, time.Time{}, false, nil
				}
				return []graphrank.IdentityRow{{
					Kind: repo.Kind, CanonicalID: repo.CanonicalID, Label: repo.Label,
					Aliases: []string{"trace-" + tc.name}, ObservedAt: time.Now().UTC(),
				}}, time.Now().UTC(), tc.complete, nil
			}
			tracer := &fakeResolutionTracer{}
			adapter := newLiveAdapterWithIdentityUniverseAndTracer(t, ctx, identityUniverse, falkorgraph.NoopTelemetry{}, tracer)
			t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })

			batch := contextfabric.ProjectionBatch{
				SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_identity_universe_trace", OrgID: orgID, Source: "live-identity-test",
				SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: time.Now().UTC(),
				Entities: []contextfabric.EntityProjection{{
					Subject: repo, Aliases: []string{"trace-" + tc.name},
					Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{repo.Label}},
					EvidenceRefIDs: []string{"evidence_repo_trace_1234"}, ObservedAt: time.Now().UTC(), SourceVersion: "v1",
				}},
				Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
				Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
			}
			if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
				t.Fatalf("ApplyProjectionBatch() error = %v", err)
			}

			principal := storage.Principal{OrgID: orgID}
			request := liveInvestigationRequest()
			interpreted := contextfabric.InterpretedQuestion{
				Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{"trace-" + tc.name},
				TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
			}
			if _, _, _, _, err := adapter.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, ""); err != nil {
				t.Fatalf("ResolveSubjects(nil) error = %v", err)
			}

			events := tracer.eventsForStage("identity_universe")
			if len(events) != 1 {
				t.Fatalf("identity_universe trace events = %#v, want exactly 1 -- the closure must fire through the REAL wired composition", events)
			}
			if events[0].IdentityUniverseComplete != tc.complete {
				t.Fatalf("identity_universe event IdentityUniverseComplete = %v, want %v", events[0].IdentityUniverseComplete, tc.complete)
			}
			if events[0].RequestID != request.RequestID {
				t.Fatalf("identity_universe event RequestID = %q, want %q", events[0].RequestID, request.RequestID)
			}
		})
	}
}

// Decision 1's OWN pinning test ("a two-claimant scenario where one is
// graph-missing must NOT fast-path commit the survivor") is deliberately
// NOT a live test here. A live attempt (repo real + projected, team
// declared in identityUniverse but never projected, both claiming the same
// alias) is confounded: repo's alias attribute is a REAL graph value, so
// ordinary hybrid search independently finds and scores it via full-text
// relevance -- in a near-empty test organization that scores high enough to
// clear LoneFloor entirely on ITS OWN, unrelated to the identity mechanism
// this ticket touches. That is not a bug (the graph genuinely has exactly
// one resolvable candidate once team never lands), and this ticket's fix
// cannot and must not suppress it -- suppressing an ordinary, ungated
// search hit would be new, unrelated behavior change. See
// TestResolveSubjects_GraphMissingSiblingNeverCommitsViaIdentityTrust
// (graphrank package) for decision 1's actual, isolated pin: it controls
// ordinary search and the identity claim independently (a fake backend, no
// live full-text scoring), proving specifically that the IDENTITY
// MECHANISM's own confidence=1 bump never fires for a claimant reader.go
// has demoted (FromKeyedIdentityLookup=false, graphMissing>0 in this
// call), without needing to reason about what else might independently
// commit the same subject.
