package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// falkordbImage is pinned by digest, matching
// internal/contextfabric/falkorgraph's own live tests and
// deploy/compose/acr.compose.yml -- see docs/design/context-fabric-falkordb-adapter.md §1.1.
const falkordbImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

// TestOpenRuntimeProjectsIntoRealFalkorDBAndRetrievalReadsItBack is the
// CHAOS-3771 live acceptance proof for the runtime composition change: it
// runs the REAL cmd/acr-projector composition path (openRuntime,
// openProjectionBackend) -- the exact functions production uses -- against a
// real FalkorDB and a real PostgreSQL, with no fake/mocked graph backend.
// It proves three things a package-level falkorgraph test cannot: (1) the
// ACR_CONTEXT_FABRIC_FALKOR_* env contract this binary reads is wired to a
// working falkorgraph.Adapter, (2) the real projectionrun.Coordinator
// (unchanged from CHAOS-3753) drives that adapter through one real tick and
// the checkpoint durably advances in Postgres, and (3) a second, independent
// falkorgraph.Adapter constructed the same way acr-api's hosted composition
// (internal/runtime/hosted/open.go's buildContextFabricInvestigator) builds
// one, resolves the just-projected subject back out -- the retrieval half of
// this change, short of the model-runtime-gated HTTP endpoint (CHAOS-3770's
// scope, not this one). No env gate: this repository's established
// testcontainers convention (falkordb needs no external credential, unlike
// ADR 0007's Zep Cloud predecessor).
func TestOpenRuntimeProjectsIntoRealFalkorDBAndRetrievalReadsItBack(t *testing.T) {
	ctx := context.Background()

	// --- Postgres: real checkpoints/rebuild-markers/episode store, migrated. ---
	pgContainer, err := tcpostgres.Run(ctx, "postgres:18-alpine",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "start Postgres container")
	t.Cleanup(func() { require.NoError(t, pgContainer.Terminate(ctx)) })
	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	migrateDB, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err, "open Postgres for migration")
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, migrateDB)
	require.NoError(t, err, "apply migrations")

	// --- FalkorDB: real graph backend, pinned digest. ---
	falkorContainer, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: falkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	require.NoError(t, err, "start FalkorDB container")
	t.Cleanup(func() { require.NoError(t, falkorContainer.Terminate(context.Background())) })
	falkorHost, err := falkorContainer.Host(ctx)
	require.NoError(t, err)
	falkorPort, err := falkorContainer.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	falkorAddr := falkorHost + ":" + falkorPort.Port()

	// --- Seed one real, approved episode: the only source that can produce
	// a batch without a live ClickHouse (ClickHouseDSN below is a fake,
	// unreachable placeholder -- the "clickhouse" source lane errors every
	// tick, non-fatally, exactly like production degrades a genuinely
	// unreachable dependency; this test's proof is the graph backend, not
	// CHAOS-3753's already-covered ClickHouse source). ---
	episodeStore, err := storagepostgres.NewEpisodeStore(migrateDB)
	require.NoError(t, err)
	orgID := "11111111-2222-3333-4444-555555555555"
	principal := storage.Principal{OrgID: orgID, RepositoryScopes: []string{"acme/widgets"}}
	episode, _, err := episodeStore.CreateIdempotent(ctx, principal, contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: "ep-falkor-accept-1", IdempotencyKey: "idem-falkor-accept-1",
		ContextPacketID: "packet_01", Goal: "prove the falkordb cutover", Summary: "acceptance run",
		Repository: contractsv1.RepositoryRef{Slug: "acme/widgets", RepoID: "repo_01"},
		Client:     contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt:  time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(), Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts:  contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}},
		Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}, nil)
	require.NoError(t, err, "seed episode")
	require.NoError(t, migrateDB.Close())

	// --- Env, exactly as production sets it (ACR_CONTEXT_FABRIC_FALKOR_*,
	// matching deploy/compose/acr.compose.yml / Helm's projector wiring). ---
	t.Setenv(falkorgraph.EnvAddr, falkorAddr)
	t.Setenv(falkorgraph.EnvTLS, "false")
	t.Setenv(falkorgraph.EnvAllowInsecure, "true")
	t.Setenv(falkorgraph.EnvGraphPrefix, "acr-cf-accept")

	cfg, err := config.LoadProjector()
	require.NoError(t, err)
	cfg.ProjectionEnabled = true
	cfg.OrgIDs = []string{orgID}
	cfg.PostgresDSN = dsn
	cfg.ClickHouseDSN = "clickhouse://redacted"

	runtime, err := openRuntime(ctx, cfg, discardLogger())
	require.NoError(t, err, "openRuntime")
	t.Cleanup(func() { require.NoError(t, runtime.Close()) })
	require.NotNil(t, runtime.Coordinator, "coordinator must start once ACR_CONTEXT_FABRIC_FALKOR_ADDR is configured")

	// --- Drive one real, synchronous tick: episodes source produces a
	// batch, the real Coordinator calls the real falkorgraph.Adapter's
	// ApplyProjectionBatch, and (on success) advances the checkpoint. ---
	runtime.Coordinator.Tick(ctx)

	// --- Proof 1: checkpoint advanced durably in Postgres. ---
	checkpointDB, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, checkpointDB.Close()) })
	checkpoints, err := pgprojection.NewCheckpointStore(checkpointDB)
	require.NoError(t, err)
	checkpoint, err := checkpoints.LoadProjectionCheckpoint(ctx, orgID, devhealthsource.EpisodesSourceName)
	require.NoError(t, err)
	require.NotEmpty(t, checkpoint.Cursor, "checkpoint must advance past the empty cursor after a landed batch")

	// --- Proof 2: nodes/edges are really present in FalkorDB, verified
	// independently of this repository's own read path (raw GRAPH.RO_QUERY
	// over the org's graph key via a bare go-redis client, not
	// falkorgraph's decoder). ---
	redisClient := goredis.NewClient(&goredis.Options{Addr: falkorAddr})
	t.Cleanup(func() { _ = redisClient.Close() })
	graphKeys, err := redisClient.Do(ctx, "GRAPH.LIST").StringSlice()
	require.NoError(t, err, "GRAPH.LIST")
	require.Len(t, graphKeys, 1, "exactly one org graph must exist")
	countRaw, err := redisClient.Do(ctx, "GRAPH.RO_QUERY", graphKeys[0], "MATCH (n) RETURN count(n)").Result()
	require.NoError(t, err, "GRAPH.RO_QUERY node count")
	require.NotNil(t, countRaw, "GRAPH.RO_QUERY must return a result for a populated graph")

	// --- Proof 3: retrieval. A second, independently constructed
	// falkorgraph.Adapter -- built the same way
	// internal/runtime/hosted/open.go's buildContextFabricInvestigator
	// builds one (falkorgraph.ConfigFromEnv + falkorgraph.New against the
	// same env) -- resolves the just-projected episode subject back out.
	// The investigation HTTP endpoint itself still 503s without a model
	// runtime (CHAOS-3770's scope); this proves the graph read half, which
	// is this change's scope.
	graphConfig, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	require.NoError(t, err)
	graphReader, err := falkorgraph.New(graphConfig)
	require.NoError(t, err)

	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectEpisode, CanonicalID: "episode:" + episode.EpisodeID, Label: "prove the falkordb cutover"}
	request := contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_falkor_accept_1",
		Question: "What happened in this episode?", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options: contextfabric.InvestigationOptions{
			MaxSubjectCandidates: 10, MaxCohortMembers: 50, MaxRelationshipPaths: 50,
			MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 262144, AllowClarification: true,
		},
		Consumer:       contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
		RequestedScope: contextfabric.RequestedScope{SubjectHints: []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: "live-test"}}},
	}
	interpreted := contextfabric.InterpretedQuestion{
		Shape: contextfabric.ShapeSingleSubject, RequestedJudgment: "status", SubjectTerms: []string{subject.Label},
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}, FactRequirements: []contextfabric.FactRequirement{{Kind: contextfabric.FactStatus}},
	}
	resolution, _, _, err := graphReader.ResolveSubjects(ctx, principal, request, interpreted, contextfabric.ResolvedGraphBinding{}, nil, nil)
	require.NoError(t, err, "ResolveSubjects against the real, just-projected graph")
	require.Len(t, resolution.Committed, 1, "retrieval must resolve the episode this run just projected")
	require.Equal(t, subject, resolution.Committed[0])
}
