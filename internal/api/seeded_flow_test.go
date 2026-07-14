package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestSeededProtectedFlowCorrelatesRealPacketPipeline(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialLifecycle(t, audit, now)
	repository := "example-org/widget-service"
	token := issueScopedCredential(t, credentials, audit, now, []string{auth.ScopeContextRead}, []string{repository})
	manager, err := limits.NewManager(limits.Options{Now: func() time.Time { return now }, Policies: limits.PolicySet{Context: limits.ContextPolicy{
		Window: time.Minute, PerOrgLimit: 2, Resources: limits.ResourceBudget{MaxItems: 10, MaxTokens: 500, MaxBytes: 8192},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	sink := &snapshotSink{}
	hooks := observability.NewHooks(sink, nil)
	traces := &capturingTraceBoundary{}
	store := seededEvaluationStore(t, "org_1", observability.NewEvidenceExpansionObserver(hooks))
	assembler := contextpacket.NewAssembler(store, contextpacket.Options{
		Now: func() time.Time { return now }, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0",
		Observer: observability.NewAssemblyObserver(hooks), StoreBackend: contextpacket.StoreBackendMemory,
		Tracer: observability.NewAssemblyTraceBoundary(hooks, traces),
	})
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{Value: contractsv1.Capabilities{SchemaVersion: contractsv1.CapabilitiesSchema}}, Observability: &hooks, Limits: manager,
		AuthAttempts: auth.NewBoundedMemoryLimiter(auth.MemoryLimiterOptions{Window: time.Minute, AttemptLimit: 5, FailureLimit: 2, MaxTrackedKeys: 4}), Now: func() time.Time { return now },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("authenticated principal missing")
		}
		packet, err := assembler.Assemble(r.Context(), principal, contractsv1.ContextPacketRequest{
			SchemaVersion: contractsv1.ContextPacketRequestSchema, RequestID: RequestID(r.Context()), Goal: "Investigate fixture evidence",
			Repository: contractsv1.RepositoryRef{Slug: repository}, Scope: contractsv1.RequestedScope{Branch: "main"},
			Options: contractsv1.PacketOptions{MaxItems: 10, MaxOutputTokens: 500, MaxSerializedBytes: 8192}, Client: contractsv1.ClientInfo{Name: "test", Version: "1.0", SidecarVersion: "0.1.0"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(packet.Items) == 0 || len(packet.Items[0].EvidenceRefIDs) == 0 {
			t.Fatal("seeded packet did not contain evidence")
		}
		if _, err := store.ResolveEvidence(r.Context(), principal, packet.Items[0].EvidenceRefIDs[0]); err != nil {
			t.Fatal(err)
		}
		if err := CompleteUsage(r.Context(), limits.ResourceUsage{Items: int64(packet.Budget.ItemsUsed), Tokens: int64(packet.Budget.EstimatedTokens), Bytes: int64(packet.Budget.SerializedBytes)}); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	protected, err := app.AuthenticatedHandler(credentials, audit, limits.RequestClassContext, terminal)
	if err != nil {
		t.Fatal(err)
	}
	handler := app.InstrumentedOperationHandler(observability.OperationContext, protected)
	request := httptest.NewRequest(http.MethodPost, "/context", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	requestID := "req_0123456789abcdef0123456789abcdef"
	request.Header.Set("X-Request-ID", requestID)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	snapshots := sink.all(t)
	if len(snapshots) != 6 {
		t.Fatalf("snapshot count = %d: %#v", len(snapshots), snapshots)
	}
	for _, snapshot := range snapshots {
		if snapshot.RequestID != observability.RequestID(requestID) {
			t.Fatalf("uncorrelated snapshot: %#v", snapshot)
		}
	}
	if snapshots[2].RankingVersion != observability.RankingVersionV1 || snapshots[3].PacketStatus != observability.PacketStatusComplete || snapshots[3].Compatibility != observability.CompatibilityCompatible || snapshots[5].Operation != observability.OperationContext {
		t.Fatalf("pipeline snapshots = %#v", snapshots)
	}
	if len(traces.observations) != 4 || len(traces.outcomes) != 4 {
		t.Fatalf("traces = %#v outcomes=%#v", traces.observations, traces.outcomes)
	}
	for _, trace := range traces.observations {
		if trace.RequestID != observability.RequestID(requestID) {
			t.Fatalf("uncorrelated trace = %#v", trace)
		}
	}
	usage, err := manager.Usage(limits.Subject{OrgID: "org_1", CredentialID: "probe_credential"}, limits.RequestClassContext)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Org.Completed != 1 || usage.Org.Items == 0 || usage.Org.Tokens == 0 || usage.Org.Bytes == 0 {
		t.Fatalf("usage = %#v", usage)
	}
}

func seededEvaluationStore(t *testing.T, orgID string, observer contextpacket.EvidenceExpansionObserver) storage.EvidenceStore {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	corpus, err := evalfixture.VerifyCorpus(filepath.Join(filepath.Dir(file), "..", "..", "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	store, err := contextpacket.NewObservedEvaluationStore(corpus, orgID, observer)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

type capturingTraceBoundary struct {
	observations []observability.TraceObservation
	outcomes     []observability.Outcome
}

func (b *capturingTraceBoundary) Start(ctx context.Context, observation observability.TraceObservation) (context.Context, func(observability.Outcome)) {
	b.observations = append(b.observations, observation)
	return ctx, func(outcome observability.Outcome) { b.outcomes = append(b.outcomes, outcome) }
}
