package projectionrun_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// This file is the executed proof that no lifecycle path activates an epoch
// whose recorded source version is not the running binary's. It runs the
// REAL coordinator against a REAL Postgres (lifecycle row, per-epoch
// checkpoints) and a REAL FalkorDB whose reads resolve the active epoch
// through the same pglifecycle resolver production wires, so "activated"
// is observed where it matters: a repository-restricted principal's
// resolution on the graph the resolver now serves.
//
// Two coordinators over one database stand for two binaries. The OLD one
// projects a team whose scope admits the restricted principal's repository;
// the NEW one projects the same team with a scope that denies it -- the
// shape a producer fix (a source version bump) takes. Every path below ends
// with the restricted principal's committed count on the active graph: 1
// means the OLD epoch is being served again.

// epochGuardFalkorImage is the same digest-pinned FalkorDB every live
// falkorgraph test uses.
const epochGuardFalkorImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

const (
	epochGuardSource     = "dev_health_teams_projects"
	epochGuardOldVersion = "devhealthsource.teams_projects.v16"
	epochGuardNewVersion = "devhealthsource.teams_projects.v17"
	epochGuardRepo       = "acme/r"
)

var epochGuardTeam = contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T7283", Label: "Team T7283"}

// epochGuardTeamSource is a cursor-driven source: page k (1-based) is served
// when the checkpoint cursor holds k-1 "p" runes, so two binaries reading the
// same durable checkpoint agree on where the stream stands. Every page
// carries the team under this binary's version and scope.
type epochGuardTeamSource struct {
	version string
	scope   []string
	pages   int
	failing atomic.Bool
	calls   atomic.Int32
}

func (s *epochGuardTeamSource) NextProjectionBatch(_ context.Context, checkpoint contextfabric.ProjectionCheckpoint) (contextfabric.ProjectionBatch, bool, error) {
	s.calls.Add(1)
	if s.failing.Load() {
		return contextfabric.ProjectionBatch{}, false, errors.New("epoch guard test source is down")
	}
	page := len(checkpoint.Cursor)
	if page >= s.pages {
		return contextfabric.ProjectionBatch{}, false, nil
	}
	next := checkpoint.Cursor + "p"
	observed := time.Date(2026, 9, 30, 12, page, 0, 0, time.UTC)
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_7283_" + s.version + "_" + next,
		OrgID: checkpoint.OrgID, Source: checkpoint.Source, SourceVersion: s.version,
		Cursor: checkpoint.Cursor, NextCursor: next, GeneratedAt: observed, CompleteEnumeration: page+1 == s.pages,
		Entities: []contextfabric.EntityProjection{{
			Subject: epochGuardTeam, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"T7283"}, RepositorySlugs: append([]string(nil), s.scope...)},
			EvidenceRefIDs: []string{"evidence_7283_team"}, ObservedAt: observed, SourceVersion: s.version,
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}, true, nil
}

// CurrentProjectionSourceVersion is the binary's own producer identity --
// the capability every production source implements.
func (s *epochGuardTeamSource) CurrentProjectionSourceVersion() string { return s.version }

// disabledEpochGuardSource is the same source with the teams toggle off.
type disabledEpochGuardSource struct{ *epochGuardTeamSource }

func (disabledEpochGuardSource) Enabled() bool { return false }

func oldTeamSource(pages int) *epochGuardTeamSource {
	return &epochGuardTeamSource{version: epochGuardOldVersion, scope: []string{epochGuardRepo}, pages: pages}
}

func newTeamSource(pages int) *epochGuardTeamSource {
	return &epochGuardTeamSource{version: epochGuardNewVersion, scope: []string{"acme/other"}, pages: pages}
}

type epochGuardHarness struct {
	t           *testing.T
	ctx         context.Context
	lifecycle   *pglifecycle.Store
	checkpoints *pgprojection.CheckpointStore
	adapter     *falkorgraph.Adapter
	org         string
}

func startEpochGuardFalkor(t *testing.T, ctx context.Context) string {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: epochGuardFalkorImage, ExposedPorts: []string{"6379/tcp"},
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
	return host + ":" + port.Port()
}

func (h *epochGuardHarness) coordinator(budget int, sources ...projectionrun.SourcePair) *projectionrun.Coordinator {
	h.t.Helper()
	coordinator, err := projectionrun.NewCoordinator(projectionrun.Config{
		OrgIDs: []string{h.org}, Sources: sources,
		Backend: h.adapter, Checkpoints: h.checkpoints, RebuildMarkers: newFakeRebuildMarker(),
		Lifecycle: h.lifecycle, EpochCheckpoints: h.checkpoints.ForEpoch,
		GraceWindow: time.Hour, MaxBackoff: time.Millisecond, DrainBatchBudget: budget, Logger: discardLogger(),
	})
	require.NoError(h.t, err)
	return coordinator
}

// committed is the restricted principal's committed-subject count on the
// graph the resolver serves right now. A graph that was never projected
// commits nothing.
func (h *epochGuardHarness) committed() int {
	h.t.Helper()
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{epochGuardTeam.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_00007283",
		Question: "What is Team T7283 working on?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer: contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: epochGuardTeam.Kind, ID: epochGuardTeam.CanonicalID, Label: epochGuardTeam.Label, Source: "live-test"}}
	principal := storage.Principal{OrgID: h.org, RepositoryScopes: []string{epochGuardRepo}}
	resolution, _, _, _, err := h.adapter.ResolveSubjects(h.ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
	if errors.Is(err, contextfabric.ErrGraphNotProjected) {
		return 0
	}
	require.NoError(h.t, err)
	return len(resolution.Committed)
}

// state reads the lifecycle row and the recorded source version of its
// ACTIVE epoch -- the two facts the decision is made from.
func (h *epochGuardHarness) state() (contextfabric.OrgGraphLifecycle, string) {
	h.t.Helper()
	row, found, err := h.lifecycle.Get(h.ctx, h.org)
	require.NoError(h.t, err)
	if !found {
		row = contextfabric.OrgGraphLifecycle{OrgID: h.org, Status: contextfabric.LifecycleStatusServing}
	}
	checkpoint, err := h.checkpoints.LoadProjectionCheckpointForEpoch(h.ctx, h.org, row.ActiveEpoch, epochGuardSource)
	require.NoError(h.t, err)
	return row, checkpoint.SourceVersion
}

func (h *epochGuardHarness) observe(label string) (contextfabric.OrgGraphLifecycle, string, int) {
	h.t.Helper()
	row, recorded := h.state()
	committed := h.committed()
	h.t.Logf("%s: status=%s active_epoch=%d active_epoch_source_version=%q restricted_principal_committed=%d", label, row.Status, row.ActiveEpoch, recorded, committed)
	return row, recorded, committed
}

func TestEpochActivationGuard_LivePathsNeverActivateAnOlderSourceVersion(t *testing.T) {
	ctx := context.Background()
	db := newProjectionRunTestDatabase(t, ctx)
	lifecycle, err := pglifecycle.NewStore(db)
	require.NoError(t, err)
	checkpoints, err := pgprojection.NewCheckpointStore(db)
	require.NoError(t, err)
	resolver, err := pglifecycle.NewResolver(lifecycle)
	require.NoError(t, err)
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: startEpochGuardFalkor(t, ctx), GraphPrefix: "acr-cf-epoch-guard", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 10, AllowInsecure: true, TLS: false, EpochResolver: resolver,
	})
	require.NoError(t, err)
	harness := func(t *testing.T, org string) *epochGuardHarness {
		return &epochGuardHarness{t: t, ctx: ctx, lifecycle: lifecycle, checkpoints: checkpoints, adapter: adapter, org: org}
	}

	// Rollback: the OLD binary served epoch 0; the NEW binary rebuilt and
	// flipped to epoch 1, whose team denies the restricted principal. A
	// rollback now would hand epoch 0 -- recorded under the OLD version --
	// back to every reader.
	t.Run("rollback", func(t *testing.T) {
		h := harness(t, "org-7283-rollback")
		old := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(1)})
		old.Tick(ctx)
		_, recorded, committed := h.observe("OLD binary served epoch 0")
		require.Equal(t, epochGuardOldVersion, recorded)
		require.Equal(t, 1, committed, "precondition: the OLD epoch admits the restricted principal")

		next := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)})
		require.NoError(t, next.Rebuild(ctx, h.org))
		tickUntilStatus(t, ctx, next, lifecycle, h.org, contextfabric.LifecycleStatusGrace, 5)
		row, recorded, committed := h.observe("NEW binary flipped")
		require.Equal(t, int64(1), row.ActiveEpoch)
		require.Equal(t, epochGuardNewVersion, recorded)
		require.Equal(t, 0, committed, "precondition: the NEW epoch denies the restricted principal")

		rollbackErr := next.Rollback(ctx, h.org)
		row, recorded, committed = h.observe("after NEW binary rollback")
		require.ErrorIs(t, rollbackErr, contextfabric.ErrLifecycleTransitionRefused, "rollback to an epoch recorded under an older source version must be refused")
		require.Equal(t, contextfabric.LifecycleStatusGrace, row.Status)
		require.Equal(t, int64(1), row.ActiveEpoch)
		require.Equal(t, epochGuardNewVersion, recorded)
		require.Equal(t, 0, committed, "the restricted principal must stay denied")
	})

	// The production rollback pair. The NEW binary's rollback onto a grace
	// epoch recorded at the OLD version is refused; the documented way back
	// across a version bump is to run the OLD binary again (helm rollback),
	// whose source reports the version the grace epoch recorded -- so its
	// rollback passes and serves exactly the pre-bump baseline.
	t.Run("rollback_by_old_binary_passes_new_binary_refuses", func(t *testing.T) {
		h := harness(t, "org-7283-rollback-pair")
		old := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(1)})
		old.Tick(ctx)
		next := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)})
		require.NoError(t, next.Rebuild(ctx, h.org))
		tickUntilStatus(t, ctx, next, lifecycle, h.org, contextfabric.LifecycleStatusGrace, 5)

		require.ErrorIs(t, next.Rollback(ctx, h.org), contextfabric.ErrEpochSourceVersionStale, "the NEW binary must refuse the OLD-version grace epoch")
		row, recorded, committed := h.observe("after NEW binary rollback")
		require.Equal(t, contextfabric.LifecycleStatusGrace, row.Status)
		require.Equal(t, int64(1), row.ActiveEpoch)
		require.Equal(t, epochGuardNewVersion, recorded)
		require.Equal(t, 0, committed)

		oldAgain := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(1)})
		require.NoError(t, oldAgain.Rollback(ctx, h.org), "the OLD binary's version matches the grace epoch, so its rollback passes")
		row, recorded, committed = h.observe("after OLD binary rollback")
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
		require.Equal(t, int64(0), row.ActiveEpoch)
		require.Equal(t, epochGuardOldVersion, recorded)
		require.Equal(t, 1, committed, "the OLD binary serves its own baseline again")
	})

	// Rollback onto a grace epoch whose data came from a source this binary
	// no longer configures at all (codex #749 r1): the guard reads the
	// epoch's own checkpoint rows, so the dropped source cannot hide there.
	t.Run("rollback_dropped_source", func(t *testing.T) {
		h := harness(t, "org-7283-rollback-dropped")
		const retired = "retired_teams_projects"
		old := h.coordinator(0, projectionrun.SourcePair{Name: retired, Source: oldTeamSource(1)})
		old.Tick(ctx)
		retiredCheckpoint, err := checkpoints.LoadProjectionCheckpointForEpoch(ctx, h.org, 0, retired)
		require.NoError(t, err)
		require.Equal(t, epochGuardOldVersion, retiredCheckpoint.SourceVersion)
		require.Equal(t, 1, h.committed(), "precondition: the OLD epoch admits the restricted principal")

		next := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)})
		require.NoError(t, next.Rebuild(ctx, h.org))
		tickUntilStatus(t, ctx, next, lifecycle, h.org, contextfabric.LifecycleStatusGrace, 5)
		require.Equal(t, 0, h.committed(), "precondition: the NEW epoch denies the restricted principal")

		rollbackErr := next.Rollback(ctx, h.org)
		row, _, committed := h.observe("after NEW binary rollback onto a dropped source's epoch")
		require.ErrorIs(t, rollbackErr, contextfabric.ErrEpochSourceVersionStale)
		require.Equal(t, int64(1), row.ActiveEpoch)
		require.Equal(t, 0, committed)
	})

	// Rollback control: an epoch recorded under the binary's OWN version is
	// still a legal rollback target -- the guard is not a blanket refusal.
	t.Run("rollback_same_version_still_allowed", func(t *testing.T) {
		h := harness(t, "org-7283-rollback-ok")
		next := h.coordinator(0, projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)})
		next.Tick(ctx)
		require.NoError(t, next.Rebuild(ctx, h.org))
		tickUntilStatus(t, ctx, next, lifecycle, h.org, contextfabric.LifecycleStatusGrace, 5)
		require.NoError(t, next.Rollback(ctx, h.org))
		row, recorded, _ := h.observe("same-version rollback")
		require.Equal(t, contextfabric.LifecycleStatusServing, row.Status)
		require.Equal(t, int64(0), row.ActiveEpoch)
		require.Equal(t, epochGuardNewVersion, recorded)
	})

	// Resume: the OLD binary opened a build; its team source reached a
	// terminal mode while a sibling source failed, so it never flipped. The
	// NEW binary resumes the build, skips the terminal source, and flips.
	t.Run("resume_terminal_source", func(t *testing.T) {
		h := harness(t, "org-7283-resume")
		broken := &epochGuardTeamSource{version: "sibling.v1", pages: 0}
		broken.failing.Store(true)
		old := h.coordinator(0,
			projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(1)},
			projectionrun.SourcePair{Name: "sibling", Source: broken},
		)
		require.NoError(t, old.Rebuild(ctx, h.org))
		old.Tick(ctx)
		row, _ := h.state()
		require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status, "precondition: the failing sibling holds the flip")

		next := h.coordinator(0,
			projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)},
			projectionrun.SourcePair{Name: "sibling", Source: &epochGuardTeamSource{version: "sibling.v1", pages: 0}},
		)
		for i := 0; i < 3; i++ {
			next.Tick(ctx)
		}
		assertBuildNotActivated(t, h, "after NEW binary resumed the build")
	})

	// Dormant: the OLD binary projected page 1 of a build and stopped (no
	// in-tick drain). The NEW binary finds no rows past that cursor, so the
	// source is cursor_exhausted without a batch ever reaching RunOnce's
	// version check -- and the build flips.
	t.Run("resume_dormant_source", func(t *testing.T) {
		h := harness(t, "org-7283-dormant")
		old := h.coordinator(-1, projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(2)})
		require.NoError(t, old.Rebuild(ctx, h.org))
		old.Tick(ctx)
		row, _ := h.state()
		require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status, "precondition: page 2 is still pending")

		next := h.coordinator(-1, projectionrun.SourcePair{Name: epochGuardSource, Source: newTeamSource(1)})
		for i := 0; i < 3; i++ {
			next.Tick(ctx)
		}
		assertBuildNotActivated(t, h, "after NEW binary found the source dormant")
	})

	// Disabled: the same partial build, resumed by a NEW binary whose teams
	// source is switched off -- disabled_at_freeze is recorded without any
	// version check and the build flips.
	t.Run("resume_disabled_source", func(t *testing.T) {
		h := harness(t, "org-7283-disabled")
		old := h.coordinator(-1, projectionrun.SourcePair{Name: epochGuardSource, Source: oldTeamSource(2)})
		require.NoError(t, old.Rebuild(ctx, h.org))
		old.Tick(ctx)
		row, _ := h.state()
		require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status, "precondition: page 2 is still pending")

		disabled := disabledEpochGuardSource{newTeamSource(1)}
		next := h.coordinator(-1, projectionrun.SourcePair{Name: epochGuardSource, Source: disabled})
		for i := 0; i < 3; i++ {
			next.Tick(ctx)
		}
		assertBuildNotActivated(t, h, "after NEW binary resumed with the source disabled")
		require.Zero(t, disabled.calls.Load(), "a disabled source is never read")
	})
}

// assertBuildNotActivated is the resume paths' shared verdict: the build
// that holds OLD-version data in epoch 1 must still be building, epoch 0
// (never projected for this organization) must still be the active one,
// and the restricted principal must resolve nothing.
func assertBuildNotActivated(t *testing.T, h *epochGuardHarness, label string) {
	t.Helper()
	row, recorded, committed := h.observe(label)
	target, err := h.checkpoints.LoadProjectionCheckpointForEpoch(h.ctx, h.org, 1, epochGuardSource)
	require.NoError(t, err)
	t.Logf("%s: epoch 1 %s source_version=%q", label, epochGuardSource, target.SourceVersion)
	require.Equal(t, epochGuardOldVersion, target.SourceVersion, "precondition: epoch 1 holds data recorded under the OLD version")
	require.Equal(t, contextfabric.LifecycleStatusBuilding, row.Status, "a build holding older-version data must not flip")
	require.Equal(t, int64(0), row.ActiveEpoch)
	require.Empty(t, recorded)
	require.Equal(t, 0, committed, "the restricted principal must not be admitted by the older epoch")
}
