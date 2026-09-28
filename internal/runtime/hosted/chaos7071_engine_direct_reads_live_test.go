package hosted

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Same pinned image as the falkorgraph live tests.
const chaos7071FalkorImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"

type chaos7071NoInterpreter struct{}

func (chaos7071NoInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.QuestionFamilyOutcome{}, errors.New("not used")
}

type chaos7071NoSynthesizer struct{}

func (chaos7071NoSynthesizer) Synthesize(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.InvestigationResult, error) {
	return contextfabric.InvestigationResult{}, errors.New("not used")
}

type chaos7071RecordingFacts struct {
	requests []contextfabric.CanonicalFactRequest
}

func (f *chaos7071RecordingFacts) ReadFacts(_ context.Context, _ storage.Principal, request contextfabric.CanonicalFactRequest) (contextfabric.CanonicalFactBundle, error) {
	f.requests = append(f.requests, request)
	return contextfabric.CanonicalFactBundle{}, nil
}

// TestLiveChaos7071DirectReadsComposeFromTheRealEngine builds a REAL
// *contextfabric.Engine over a live FalkorDB adapter and a recording fact
// registry, hands it to buildDirectReads (the hosted wiring), and proves the
// gate and reader it returns sit on that engine's own graph and registry,
// through (*Engine).DirectReadSources: a subject projected into the engine's
// graph is admitted, one that is not is refused, and the admitted read lands
// on the engine's registry with the admitted subject only.
func TestLiveChaos7071DirectReadsComposeFromTheRealEngine(t *testing.T) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chaos7071FalkorImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start FalkorDB container: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := falkorgraph.New(falkorgraph.Config{
		Addr: host + ":" + port.Port(), GraphPrefix: "acr-cf-hosted-live", RequestTimeout: 15 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 4, AllowInsecure: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	orgID := "live-chaos7071-hosted-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	now := time.Now().UTC()
	repo := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/a", Label: "acme/a"}
	batch := contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_chaos7071_hosted_00000001", OrgID: orgID, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: now,
		Entities: []contextfabric.EntityProjection{{
			Subject: repo, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
			Authorization:  contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}},
			EvidenceRefIDs: []string{"evidence_repository_acme_a"}, ObservedAt: now, SourceVersion: "v1",
		}},
		Relationships: []contextfabric.RelationshipProjection{}, Contents: []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{},
		Tombstones: []contextfabric.ProjectionTombstone{},
	}
	if _, err := adapter.ApplyProjectionBatch(ctx, batch); err != nil {
		t.Fatalf("ApplyProjectionBatch() error = %v", err)
	}

	facts := &chaos7071RecordingFacts{}
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: chaos7071NoInterpreter{}, Graph: adapter, Facts: facts, Synthesizer: chaos7071NoSynthesizer{},
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_70710001" }})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	gate, reader := buildDirectReads(engine, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if gate == nil || reader == nil {
		t.Fatal("the real engine over the FalkorDB adapter built no direct-read gate")
	}

	sum := sha256.Sum256([]byte(t.Name()))
	requestCtx := observability.WithRequestID(ctx, "req_"+hex.EncodeToString(sum[:16]))
	principal := storage.Principal{OrgID: orgID, Subject: "user-1", CredentialID: "cred-1", RepositoryScopes: []string{"acme/a"}}
	unprojected := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: "repository:acme/never-projected"}
	proof, decision := gate.Authorize(requestCtx, principal, []contextfabric.SubjectRef{repo, unprojected})
	if decision.Decision != directread.DecisionPartial || proof.Len() != 1 {
		t.Fatalf("gate over the engine's graph: decision %s, %d admitted, want partial with 1", decision.Decision, proof.Len())
	}
	if _, err := reader.Read(requestCtx, principal, proof, contextfabric.CanonicalFactRequest{}); err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(facts.requests) != 1 {
		t.Fatalf("the engine's registry saw %d reads, want 1", len(facts.requests))
	}
	got := facts.requests[0].Subjects
	if len(got) != 1 || got[0].Kind != repo.Kind || got[0].CanonicalID != repo.CanonicalID || slices.ContainsFunc(got, func(s contextfabric.SubjectRef) bool { return s.CanonicalID == unprojected.CanonicalID }) {
		t.Fatalf("the engine's registry was asked for %v, want only %s", got, repo.CanonicalID)
	}
}
