package mcp_test

import (
	"context"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// The isolation matrix runs against a REAL acr-api composition: api.NewApp
// with the real Authenticator, the real stored-result route and gate, the
// real evidence store (contextpacket.EvaluationStore over the fixed
// evaluation corpus) and the real context-packet assembler. Only the pieces
// that have no in-memory production twin are stand-ins, and each says so:
// the graph the stored-result gate reads (matrixGraph), the investigator
// (identityInvestigator) and the episode sink (countingEpisodes).

// matrixRigDeadline bounds every call the matrix rig makes to another part of
// the rig (the endpoint to acr-api, the runner to the endpoint).
const matrixRigDeadline = 120 * time.Second

// Organizations, repositories and identities of the matrix.
const (
	orgOne = "org_1"
	orgTwo = "org_2"

	// repoWidget is the repository the fixed evaluation corpus belongs to.
	repoWidget = "example-org/widget-service"
	// repoOther is a repository no corpus, result or evidence belongs to.
	repoOther = "example-org/other-service"

	corpusBranch = "main"
	corpusCommit = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
	evidenceRef  = "ev-ci-checkout-001"

	resultOwnedByA  = "result_matrix_org1_owner"
	resultOwnedByB  = "result_matrix_org2_owner"
	resultNeverMade = "result_matrix_never_made"
	evidenceNever   = "ev-never-existed-000"

	// Markers that appear ONLY in the data a caller may not read. A denial
	// body carrying any of them is a leak.
	judgmentOfA = "JUDGMENT-SECRET-ORG1-OWNER"
	labelOfA    = "LABEL-SECRET-ORG1-SUBJECT"
	judgmentOfB = "JUDGMENT-SECRET-ORG2-OWNER"
	labelOfB    = "LABEL-SECRET-ORG2-SUBJECT"
)

// matrixGraph is the graph the stored-result gate reads. It is
// deliberately org-agnostic: a real graph is keyed by the principal's
// organization, and that would make the organization scope of the result
// store unobservable behind the gate. Here a cross-organization read that
// slipped past the store would still be admitted by a grant naming the
// repository, so each guard has a caller that isolates it.
type matrixGraph struct {
	nodes map[string]map[string]interface{}
}

func (matrixGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "matrix", Epoch: 1}, nil
}

func (matrixGraph) ResolveSubjects(context.Context, storage.Principal, contextfabric.InvestigationRequest, contextfabric.InterpretedQuestion, contextfabric.ResolvedGraphBinding, *contextfabric.ConfirmedExpectedKind, *contextfabric.ConfirmedAnchorSelection, *contextfabric.QuestionFrame, contextfabric.SubjectKind) (contextfabric.SubjectResolution, contextfabric.StructureOfferMaterial, contextfabric.CommitBasisSet, contextfabric.CommitDecisionDigestSet, error) {
	return contextfabric.SubjectResolution{}, contextfabric.StructureOfferMaterial{}, nil, nil, errors.New("matrixGraph resolves no subjects")
}

func (matrixGraph) DiscoverContext(context.Context, storage.Principal, contextfabric.GraphDiscoveryRequest) (contextfabric.GraphContext, error) {
	return contextfabric.GraphContext{}, errors.New("matrixGraph discovers nothing")
}

func (g matrixGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	nodes := map[string][]graphrank.CandidateNode{}
	for _, subject := range subjects {
		key := graphrank.SubjectKey(subject)
		if attributes, ok := g.nodes[key]; ok {
			nodes[key] = []graphrank.CandidateNode{{Attributes: attributes}}
		}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

// identityInvestigator answers with the identity it was asked on behalf of,
// so an answer that reached the wrong caller is visible in its own text.
type identityInvestigator struct{}

func (identityInvestigator) Investigate(_ context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	result := matrixResult("result_matrix_investigated_"+principal.OrgID, "investigated-for-"+principal.OrgID+"/"+principal.CredentialID, "Investigated "+principal.OrgID, "project_investigated")
	result.RequestID = request.RequestID
	return result, nil
}

// countingEpisodes records every write that reaches the episode sink.
type countingEpisodes struct {
	mu    sync.Mutex
	calls int
}

func (c *countingEpisodes) Create(context.Context, storage.Principal, contractsv1.AgentEpisodeCreate) (contractsv1.AgentEpisode, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return contractsv1.AgentEpisode{}, false, errors.New("the isolation matrix never writes an episode")
}

func (c *countingEpisodes) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// countingEvidence counts (context = scope resolutions and packet reads) what reaches the real evidence store, so a deny
// row can assert the store was never consulted where a guard above it must
// have refused first.
type countingEvidence struct {
	inner   storage.EvidenceStore
	mu      sync.Mutex
	resolve int
	context int
}

func (c *countingEvidence) ResolveScope(ctx context.Context, p storage.Principal, r contractsv1.ContextPacketRequest) (contractsv1.ResolvedScope, error) {
	c.mu.Lock()
	c.context++
	c.mu.Unlock()
	return c.inner.ResolveScope(ctx, p, r)
}

func (c *countingEvidence) ContextForTask(ctx context.Context, p storage.Principal, r contractsv1.ContextPacketRequest) (storage.EvidenceBundle, error) {
	c.mu.Lock()
	c.context++
	c.mu.Unlock()
	return c.inner.ContextForTask(ctx, p, r)
}

func (c *countingEvidence) ResolveEvidence(ctx context.Context, p storage.Principal, id string) (contractsv1.ExpandedEvidence, error) {
	c.mu.Lock()
	c.resolve++
	c.mu.Unlock()
	return c.inner.ResolveEvidence(ctx, p, id)
}

func (c *countingEvidence) counts() (resolve, packets int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolve, c.context
}

// matrixResult is a valid stored investigation result for one subject.
func matrixResult(id, judgment, label, subjectID string) contractsv1.ContextFabricInvestigationResult {
	subject := contractsv1.ContextFabricSubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: subjectID, Label: label}
	result := contractsv1.ContextFabricInvestigationResult{
		SchemaVersion: contractsv1.ContextFabricInvestigationResultSchema,
		ResultID:      id, RequestID: "request_matrix_0001", GeneratedAt: time.Now().UTC(),
		Status: contractsv1.ContextFabricInvestigationComplete, Question: "why is the project not ready to ship?",
		Interpretation: contractsv1.ContextFabricInterpretedQuestion{
			Shape: contractsv1.ContextFabricShapeSingleSubject, RequestedJudgment: "status",
			TimeContext:      contractsv1.ContextFabricTimeContext{Axis: contractsv1.ContextFabricTemporalCurrent},
			FactRequirements: []contractsv1.ContextFabricFactRequirement{{Kind: contractsv1.ContextFabricFactStatus}},
		},
		SubjectResolution: contractsv1.ContextFabricSubjectResolution{Candidates: []contractsv1.ContextFabricSubjectCandidate{}, Committed: []contractsv1.ContextFabricSubjectRef{subject}},
		DirectJudgment:    judgment, CurrentState: "Nominal.", StrongestPressures: []string{},
		Drivers: []contractsv1.ContextFabricDriverJudgment{}, RemainingWork: []contractsv1.ContextFabricFinding{},
		ReadinessGaps: []contractsv1.ContextFabricFinding{}, Paths: []contractsv1.ContextFabricRelationshipPath{},
		Conflicts: []contractsv1.ContextFabricFinding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
		ClaimedFacts: []contractsv1.ContextFabricClaimedFact{},
		Coverage:     contractsv1.ContextFabricCoverage{Sources: []contractsv1.ContextFabricSourceObservation{}, DegradedReasons: []string{}},
		Versions: contractsv1.ContextFabricVersionSet{
			ServiceVersion: "test", ContractVersion: contractsv1.ContextFabricInvestigationResultSchema, Backend: "graph",
			ProjectionVersion: "v1", QueryVersion: "v1", InterpretationVersion: "v1", SynthesisVersion: "v1", CanonicalServiceVersion: "v1", ModelIdentity: "test/model-v1",
		},
		DeterministicAnswer: "Nominal based on available context.", Warnings: []string{},
	}
	result.Completeness = contextfabric.ComputeAnswerCompleteness(result)
	return result
}

// apiCall is one request the acr-api composition served.
type apiCall struct {
	method, path string
	status       int
}

// callRecorder records every request the acr-api composition serves, in
// order, with its final status.
type callRecorder struct {
	inner http.Handler
	mu    sync.Mutex
	calls []apiCall
}

type recordedWriter struct {
	http.ResponseWriter
	status int
}

func (w *recordedWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *recordedWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *recordedWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *callRecorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	rw := &recordedWriter{ResponseWriter: w}
	r.inner.ServeHTTP(rw, req)
	status := rw.status
	if status == 0 {
		status = http.StatusOK
	}
	r.mu.Lock()
	r.calls = append(r.calls, apiCall{method: req.Method, path: req.URL.Path, status: status})
	r.mu.Unlock()
}

func (r *callRecorder) mark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *callRecorder) since(mark int) []apiCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]apiCall(nil), r.calls[mark:]...)
}

// matrixStack is the real acr-api composition the matrix runs against.
type matrixStack struct {
	t        *testing.T
	app      *api.App
	server   *httptest.Server
	caPath   string
	calls    *callRecorder
	apiLogs  *syncBuffer
	store    *storage.CredentialLifecycle
	service  *auth.Service
	evidence *countingEvidence
	episodes *countingEpisodes
}

func newMatrixStack(t *testing.T) *matrixStack {
	t.Helper()
	issuedAt := time.Now().Add(-2 * time.Hour)
	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	corpus, err := evalfixture.VerifyCorpus(filepath.Join(filepath.Dir(thisFile), "..", "..", "testdata", "evaluation", "v1"))
	if err != nil {
		t.Fatalf("verify corpus: %v", err)
	}
	if corpus.Scenario.Repository.Slug != repoWidget {
		t.Fatalf("corpus repository %q, the matrix expects %q", corpus.Scenario.Repository.Slug, repoWidget)
	}
	evaluation, err := contextpacket.NewEvaluationStore(corpus, orgOne)
	if err != nil {
		t.Fatal(err)
	}
	evidence := &countingEvidence{inner: evaluation}
	assembler := contextpacket.NewAssembler(evidence, contextpacket.Options{Now: time.Now, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"})

	results := memoryinvestigation.NewStore()
	seed := func(orgID string, result contractsv1.ContextFabricInvestigationResult) {
		err := results.Save(context.Background(), storage.Principal{OrgID: orgID}, result, contextfabric.SourceWatermarkSnapshot{}, nil,
			contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}), contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{},
			contextfabric.ReuseVersionAuthorities{}, 0, "", contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation))
		if err != nil {
			t.Fatalf("seed %s: %v", result.ResultID, err)
		}
	}
	seed(orgOne, matrixResult(resultOwnedByA, judgmentOfA, labelOfA, "project_org1_owner"))
	seed(orgTwo, matrixResult(resultOwnedByB, judgmentOfB, labelOfB, "project_org2_owner"))
	graph := matrixGraph{nodes: map[string]map[string]interface{}{
		graphrank.SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_org1_owner"}): {"authorization_repositories": []string{repoWidget}},
		graphrank.SubjectKey(contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_org2_owner"}): {"authorization_repositories": []string{repoWidget}},
	}}

	manager, err := limits.NewManager(limits.Options{Now: time.Now, PerOrgConcurrency: 4096, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 1_000_000},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 1_000_000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 1_000_000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	episodes := &countingEpisodes{}
	apiLogs := &syncBuffer{}
	app, err := api.NewApp(api.AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: matrixRigDeadline}, api.Dependencies{
		Capabilities: api.StaticCapabilitiesProvider{Now: time.Now, Value: contractsv1.Capabilities{
			SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "1.2.3", MinimumSidecarVersion: "1.0.0",
			SupportedSchemaVersions: contractsv1.AllSchemaVersions,
			Limits:                  contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
		}},
		Limits: manager, Now: time.Now,
		Runtime: &api.RuntimeDependencies{
			Credentials: credentials, Audit: audit,
			Entitlements: api.EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			Assembler:    assembler, Evidence: evidence, Episodes: episodes,
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(time.Now)),
			ReadinessChecks:            []api.ReadinessCheck{api.CheckFunc{CheckName: "postgres"}, api.CheckFunc{CheckName: "clickhouse"}, api.CheckFunc{CheckName: "entitlement"}},
			Investigator:               identityInvestigator{},
			InvestigationResults:       results,
			StoredResultGate:           contextfabric.NewStoredResultGate(graph),
		},
	}, slog.New(slog.NewJSONHandler(apiLogs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })

	recorder := &callRecorder{inner: app.Handler()}
	server := httptest.NewTLSServer(recorder)
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "matrix-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &matrixStack{t: t, app: app, server: server, caPath: caPath, calls: recorder, apiLogs: apiLogs, store: credentials, service: service, evidence: evidence, episodes: episodes}
}

// issue creates one credential in an organization.
func (s *matrixStack) issue(orgID string, scopes, repositories []string, expires *time.Time) issued {
	s.t.Helper()
	credential, err := s.service.Create(context.Background(), auth.CreateCredentialRequest{
		OrgID: orgID, Name: "auth-matrix", RepositoryScopes: repositories, Scopes: scopes, CreatedBy: "test_actor", ExpiresAt: expires,
	})
	if err != nil {
		s.t.Fatal(err)
	}
	return issued{token: credential.Token, credentialID: credential.Credential.CredentialID}
}

func (s *matrixStack) revoke(orgID string, c issued) {
	s.t.Helper()
	if _, err := s.store.RevokeCredential(context.Background(), storage.CredentialRevocationInput{OrgID: orgID, CredentialID: c.credentialID, ActorID: "admin"}); err != nil {
		s.t.Fatal(err)
	}
}

func (s *matrixStack) sidecarConfig() sidecar.Config {
	s.t.Helper()
	base, err := url.Parse(s.server.URL)
	if err != nil {
		s.t.Fatal(err)
	}
	return sidecar.Config{
		APIBaseURL: base,
		// The matrix measures isolation, not capacity. Its concurrency test
		// puts 660 requests, each with its own credential decision, on one
		// process pair; on a two-CPU race build a request can wait seconds
		// for a slot, and a deadline that fires then is the rig running out
		// of CPU, not an answer about who may read what.
		Timeout:               matrixRigDeadline,
		MaxResponseBytes:      1 << 20,
		MaxRequestBodyBytes:   256 << 10,
		ClientName:            "test-sidecar",
		ClientVersion:         "1.0.0",
		SidecarVersion:        "1.0.0",
		CACertPath:            s.caPath,
		AllowInsecureLoopback: true,
		EnableWriteback:       true,
	}
}

// staleStore is a credential store that still returns a row the lookup
// contract says it must not: it serves a revoked or expired credential's row
// on a hash lookup, as a lagging replica or a store defect could. The
// production stores filter such rows in the lookup itself, so the
// authenticator's own revocation and expiry checks are a second layer that no
// row of a correct store can reach; this store is the only way to observe it.
type staleStore struct {
	storage.CredentialStore
	orgID  string
	mu     sync.Mutex
	byHash map[string]string // token hash -> credential id
}

func (s *staleStore) FindByTokenHash(ctx context.Context, tokenHash string) (contractsv1.ClientCredential, error) {
	credential, err := s.CredentialStore.FindByTokenHash(ctx, tokenHash)
	if !errors.Is(err, storage.ErrNotFound) {
		return credential, err
	}
	s.mu.Lock()
	id, known := s.byHash[tokenHash]
	s.mu.Unlock()
	if !known {
		return credential, err
	}
	return s.CredentialStore.GetByID(ctx, s.orgID, id)
}

func (s *staleStore) remember(token string, c issued) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byHash == nil {
		s.byHash = map[string]string{}
	}
	s.byHash[auth.HashToken(token)] = c.credentialID
}

// newStaleStoreAPI is a hosted API whose real Authenticator sits over a
// staleStore, exposing only the capabilities route the endpoint decides a
// caller with.
func newStaleStoreAPI(t *testing.T) (*httptest.Server, string, *staleStore, *auth.Service, *storage.CredentialLifecycle) {
	t.Helper()
	issuedAt := time.Now().Add(-2 * time.Hour)
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: memory.NewAuditStore(), Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return issuedAt }})
	if err != nil {
		t.Fatal(err)
	}
	stale := &staleStore{CredentialStore: credentials, orgID: orgOne}
	authenticator, err := auth.NewAuthenticator(stale, memory.NewAuditStore(), auth.AuthenticatorOptions{Logger: slog.New(slog.NewJSONHandler(&syncBuffer{}, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authenticator.Close() })
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/agent-context/capabilities", authenticator.Middleware(authenticator.RequireScope(auth.ScopeContextRead, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, _ := auth.PrincipalFromContext(r.Context())
		writeHostedJSON(w, http.StatusOK, contractsv1.Capabilities{
			SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "1.2.3", MinimumSidecarVersion: "0.1.0",
			SupportedSchemaVersions: acrmcp.OurSchemaVersionsForTest,
			EnabledTools:            []string{acrmcp.ToolContextForTaskForTest, acrmcp.ToolSourceEvidenceForTest},
			Entitlements:            contractsv1.CapabilityEntitlements{AgentContextRuntime: true},
			Permissions: contractsv1.CapabilityPermissions{
				ContextRead:  auth.HasScope(principal.Permissions, auth.ScopeContextRead),
				EvidenceRead: auth.HasScope(principal.Permissions, auth.ScopeEvidenceRead),
			},
			Limits:      contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
			GeneratedAt: time.Now().UTC(),
		})
	}))))
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "stale-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return server, caPath, stale, service, credentials
}
