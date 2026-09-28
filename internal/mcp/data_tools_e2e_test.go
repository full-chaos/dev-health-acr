package mcp_test

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/full-chaos/dev-health-acr/internal/api"
	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	acrmcp "github.com/full-chaos/dev-health-acr/internal/mcp"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// CHAOS-7072 (S1a) end to end, model-free (design T8, A1.7): the three data
// tools run through the REAL MCP server, the REAL sidecar client, the REAL
// acr-api routes (authenticator, scope, rate class, subject gate, operation
// policy, output allowlist) and the REAL HTTP query client against a fake ops
// query service. Every seam that could reach a model on our side is armed to
// panic on use: the investigator (interpreter and synthesizer) is the only
// model-facing seam the API composition exposes, and the graph the lookup
// reads has no embedding method at all (directread.SubjectGraph).

const (
	dtRepoA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	dtRepoB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	dtSlugA = "example-org/widget-service"
	dtSlugB = "example-org/other-service"
)

type dtModelSeam struct{ used atomic.Int64 }

// Investigate is the interpreter-and-synthesizer seam of the API.
func (m *dtModelSeam) Investigate(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
	m.used.Add(1)
	panic("model seam used by a data tool: Investigate")
}

type dtGraph struct{}

func dtNodes() []directread.LookupNode {
	node := func(id, slug string) directread.LookupNode {
		return directread.LookupNode{Kind: "repository", CanonicalID: "repository:" + id, Label: slug, Attributes: map[string]interface{}{
			"subject_kind": "repository", "canonical_id": "repository:" + id, "label": slug, "authorization_repositories": []string{slug},
		}}
	}
	return []directread.LookupNode{node(dtRepoA, dtSlugA), node(dtRepoB, dtSlugB)}
}

func (dtGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "dt", Epoch: 1}, nil
}

func (dtGraph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	nodes := map[string][]graphrank.CandidateNode{}
	for _, node := range dtNodes() {
		ref := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(node.Kind), CanonicalID: node.CanonicalID}
		nodes[graphrank.SubjectKey(ref)] = []graphrank.CandidateNode{{Attributes: node.Attributes}}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

func (dtGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

func (dtGraph) ListSubjectsByKind(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, kind, after string, pageSize int) (directread.LookupPage, error) {
	var out []directread.LookupNode
	for _, node := range dtNodes() {
		if node.Kind == kind && node.CanonicalID > after {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CanonicalID < out[j].CanonicalID })
	page := directread.LookupPage{More: len(out) > pageSize}
	if page.More {
		out = out[:pageSize]
	}
	page.Nodes = out
	return page, nil
}

func (dtGraph) FindSubjectsByExactName(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, query string, kinds []string) (directread.LookupPage, error) {
	var out []directread.LookupNode
	for _, node := range dtNodes() {
		for _, kind := range kinds {
			if node.Kind == kind && strings.EqualFold(node.Label, query) {
				n := node
				n.Match = "exact"
				out = append(out, n)
			}
		}
	}
	return directread.LookupPage{Nodes: out}, nil
}

type dtQueryService struct {
	server *httptest.Server
	mu     sync.Mutex
	bodies []map[string]any
}

func newDTQueryService(t *testing.T) *dtQueryService {
	q := &dtQueryService{}
	q.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		q.mu.Lock()
		q.bodies = append(q.bodies, body)
		q.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		vars, _ := body["variables"].(map[string]any)
		filter, _ := vars["filter"].(map[string]any)
		input, _ := vars["input"].(map[string]any)
		ids, _ := filter["repoIds"].([]any)
		if ids == nil {
			ids, _ = input["repoIds"].([]any)
		}
		rows := []map[string]any{}
		for _, id := range ids {
			rows = append(rows, map[string]any{"day": "2026-09-27", "scope": "REPO", "scopeId": id, "scopeLabel": "repo", "score": 0.42, "severity": "ELEVATED", "repoId": id, "filePath": "a.go", "riskScore": 9})
		}
		out, _ := json.Marshal(map[string]any{"data": map[string]any{
			"compoundingRisk": map[string]any{"orgId": "org_1", "breakout": "REPO", "rows": rows, "trend": []any{}},
			"hotspots":        map[string]any{"rows": rows},
		}})
		_, _ = w.Write(out)
	}))
	t.Cleanup(q.server.Close)
	return q
}

type dtStack struct {
	t       *testing.T
	server  *httptest.Server
	caPath  string
	service *auth.Service
	seam    *dtModelSeam
	query   *dtQueryService
}

func newDTStack(t *testing.T) *dtStack {
	t.Helper()
	now := time.Now()
	audit := memory.NewAuditStore()
	credentials, err := memory.NewCredentialStoreWithOptions(memory.CredentialStoreOptions{Audit: audit, Now: func() time.Time { return now.Add(-time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	service, err := auth.NewService(credentials, auth.ServiceOptions{Now: func() time.Time { return now.Add(-time.Hour) }})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := memory.NewDeviceAuthorizationStore(memory.DeviceAuthorizationStoreOptions{Credentials: credentials, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := limits.NewManager(limits.Options{Now: time.Now, PerOrgConcurrency: 64, Policies: limits.PolicySet{
		Auth:     limits.AuthPolicy{Window: time.Minute, PerOrgLimit: 100000},
		Context:  limits.ContextPolicy{Window: time.Minute, PerOrgLimit: 100000, Resources: limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}},
		Evidence: limits.EvidencePolicy{Window: time.Minute, PerOrgLimit: 100000},
		Data:     limits.DataPolicy{Window: time.Minute, PerCredentialLimit: 100000},
	}})
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
	evaluation, err := contextpacket.NewEvaluationStore(corpus, "org_1")
	if err != nil {
		t.Fatal(err)
	}
	assembler := contextpacket.NewAssembler(evaluation, contextpacket.Options{Now: time.Now, ServiceVersion: "test", MinimumSidecarVersion: "0.1.0"})
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	query := newDTQueryService(t)
	graph := dtGraph{}
	gate := directread.NewSubjectGate(graph, nil)
	lookup := directread.NewSubjectLookup(graph, gate, nil)
	client, err := directread.NewHTTPQueryClientWithHTTP(query.server.URL, 5*time.Second, query.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{Catalogue: catalogue, Gate: gate, Client: client, Grants: directread.NewGrantedRepositories(lookup)})
	if err != nil {
		t.Fatal(err)
	}
	seam := &dtModelSeam{}
	app, err := api.NewApp(api.AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: 30 * time.Second}, api.Dependencies{
		Capabilities: api.StaticCapabilitiesProvider{Now: time.Now, Value: contractsv1.Capabilities{
			SchemaVersion: contractsv1.CapabilitiesSchema, Service: "dev-health-acr", ServiceVersion: "1.2.3", MinimumSidecarVersion: "1.0.0",
			SupportedSchemaVersions: contractsv1.AllSchemaVersions,
			Limits:                  contractsv1.CapabilityLimits{MaxItems: 30, MaxOutputTokens: 4000, MaxSerializedBytes: 262144, RequestsPerMinute: 60},
		}},
		Limits: manager, Now: time.Now,
		Runtime: &api.RuntimeDependencies{
			Credentials: credentials, Audit: audit, Assembler: assembler, Evidence: evaluation,
			Entitlements:         api.EntitlementFunc(func(context.Context, string, string) (bool, error) { return true, nil }),
			DeviceAuthorizations: devices, DeviceVerificationURL: "https://verify.example.test/device",
			DeviceAuthorizationLimiter: api.NewDeviceAuthorizationLimiter(api.ClockFunc(time.Now)),
			ReadinessChecks:            []api.ReadinessCheck{api.CheckFunc{CheckName: "postgres"}, api.CheckFunc{CheckName: "entitlement"}},
			DataStoreChecks:            []api.ReadinessCheck{api.CheckFunc{CheckName: "clickhouse"}},
			Investigator:               seam,
			DirectReadGate:             gate, DataCatalogue: catalogue, DataOperations: runner, DataSubjects: lookup,
		},
	}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewTLSServer(app.Handler())
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "dt-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return &dtStack{t: t, server: server, caPath: caPath, service: service, seam: seam, query: query}
}

// session connects a real MCP server, built from the hosted capabilities this
// credential really gets, to an in-memory client.
func (s *dtStack) session(scopes, repositories []string) (*mcpsdk.ClientSession, *acrmcp.Bootstrap) {
	s.t.Helper()
	issued, err := s.service.Create(context.Background(), auth.CreateCredentialRequest{OrgID: "org_1", Name: "dt", RepositoryScopes: repositories, Scopes: scopes, CreatedBy: "test_actor"})
	if err != nil {
		s.t.Fatal(err)
	}
	base, _ := url.Parse(s.server.URL)
	cfg := sidecar.Config{APIBaseURL: base, Timeout: 30 * time.Second, MaxResponseBytes: 1 << 20, MaxRequestBodyBytes: 256 << 10, ClientName: "test-sidecar", ClientVersion: "1.0.0", SidecarVersion: "1.0.0", CACertPath: s.caPath, AllowInsecureLoopback: true}
	client, err := sidecar.NewClient(cfg, func() (sidecar.CredentialResult, error) {
		return sidecar.CredentialResult{Token: issued.Token, Source: "test"}, nil
	})
	if err != nil {
		s.t.Fatal(err)
	}
	caps, err := client.Capabilities(context.Background())
	if err != nil {
		s.t.Fatal(err)
	}
	boot := &acrmcp.Bootstrap{Config: cfg, Client: client, Capabilities: caps}
	server := acrmcp.NewServer(boot, "test")
	t1, t2 := mcpsdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), t1, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	cs, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0.0.1"}, nil).Connect(context.Background(), t2, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs, boot
}

func dtCallTool(t *testing.T, cs *mcpsdk.ClientSession, name string, args map[string]any) (map[string]any, string) {
	t.Helper()
	result, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text string
	for _, c := range result.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok {
			text += tc.Text
		}
	}
	if result.IsError {
		return nil, text
	}
	encoded, _ := json.Marshal(result.StructuredContent)
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatal(err)
	}
	return out, text
}

func TestDataToolsEndToEndAreModelFreeAndScopedToTheCredential(t *testing.T) {
	stack := newDTStack(t)
	ids := func(body map[string]any) []string {
		var out []string
		for _, s := range body["subjects"].([]any) {
			out = append(out, s.(map[string]any)["canonical_id"].(string))
		}
		return out
	}

	// Unrestricted credential with data:read: the whole flow of the design's
	// first question, three calls.
	cs, boot := stack.session([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"})
	listed, _ := cs.ListTools(context.Background(), nil)
	names := map[string]bool{}
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"data_catalog", "find_subjects", "run_operation"} {
		if !names[want] {
			t.Fatalf("the hosted API advertised %v but %s is not registered (tools %v)", boot.Capabilities.EnabledTools, want, names)
		}
	}
	found, text := dtCallTool(t, cs, "find_subjects", map[string]any{"kind": "repository"})
	if found == nil || len(ids(found)) != 2 {
		t.Fatalf("find_subjects: %v %q", found, text)
	}
	risk, text := dtCallTool(t, cs, "run_operation", map[string]any{"operation": "compoundingRisk", "variables": map[string]any{"filter": map[string]any{"breakout": "REPO", "repoIds": []string{"repository:" + dtRepoA}}}})
	if risk == nil || risk["call"] != "served" || risk["result"] != "data" || risk["completeness"] != "unknown" {
		t.Fatalf("compoundingRisk: %v %q", risk, text)
	}
	hot, text := dtCallTool(t, cs, "run_operation", map[string]any{"operation": "hotspots", "variables": map[string]any{"input": map[string]any{"repoIds": []string{"repository:" + dtRepoA}, "sinceUtc": "2026-08-29T00:00:00Z", "untilUtc": "2026-09-28T00:00:00Z"}}})
	if hot == nil || hot["call"] != "served" || !strings.Contains(text, "call=served") {
		t.Fatalf("hotspots: %v %q", hot, text)
	}
	catalog, _ := dtCallTool(t, cs, "data_catalog", map[string]any{"sections": []string{"operations"}})
	if got := len(catalog["operations"].(map[string]any)["operations"].([]any)); got != 16 {
		t.Fatalf("an unrestricted credential sees %d operations, want 16", got)
	}
	// A foreign id is a typed refusal, never data.
	foreign, _ := dtCallTool(t, cs, "run_operation", map[string]any{"operation": "hotspots", "variables": map[string]any{"input": map[string]any{"repoIds": []string{"repository:cccccccc-cccc-4ccc-8ccc-cccccccccccc"}, "sinceUtc": "2026-08-29T00:00:00Z", "untilUtc": "2026-09-28T00:00:00Z"}}})
	if foreign["call"] != "refused" || foreign["refusal"].(map[string]any)["code"] != "denied_or_not_found" {
		t.Fatalf("an unknown id: %v", foreign)
	}
	// The shape the design refuses (K14-A) is a refusal with its code.
	shape, _ := dtCallTool(t, cs, "run_operation", map[string]any{"operation": "investmentBreakdown", "variables": map[string]any{"batch": map[string]any{"breakdowns": []map[string]any{{"dimension": "TEAM", "measure": "COUNT", "dateRange": map[string]any{"startDate": "2026-06-29", "endDate": "2026-09-28"}, "topN": 10}}}}})
	if shape["call"] != "refused" || shape["refusal"].(map[string]any)["code"] != "basis_dependent_shape" {
		t.Fatalf("investment by team: %v", shape)
	}

	// A credential restricted to repository A: only A is listed, the foreign
	// repository is refused, and the catalogue shows the restricted class.
	rs, _ := stack.session([]string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{dtSlugA})
	restrictedFound, _ := dtCallTool(t, rs, "find_subjects", map[string]any{"kind": "repository"})
	if got := ids(restrictedFound); len(got) != 1 || got[0] != "repository:"+dtRepoA {
		t.Fatalf("restricted list: %v", got)
	}
	denied, _ := dtCallTool(t, rs, "run_operation", map[string]any{"operation": "hotspots", "variables": map[string]any{"input": map[string]any{"repoIds": []string{"repository:" + dtRepoB}, "sinceUtc": "2026-08-29T00:00:00Z", "untilUtc": "2026-09-28T00:00:00Z"}}})
	if denied["call"] != "refused" {
		t.Fatalf("a repository outside the grant was not refused: %v", denied)
	}
	if strings.Contains(mustJSON(denied), dtSlugB) || strings.Contains(mustJSON(denied), dtRepoB) {
		t.Fatalf("the refusal names the repository outside the grant: %s", mustJSON(denied))
	}
	restrictedCatalog, _ := dtCallTool(t, rs, "data_catalog", nil)
	if got := len(restrictedCatalog["operations"].(map[string]any)["operations"].([]any)); got != 3 {
		t.Fatalf("a restricted credential sees %d operations, want 3", got)
	}

	// Without data:read run_operation is not advertised, and a direct call
	// (as a stale client would make) is the typed scope error.
	noData, noDataBoot := stack.session([]string{auth.ScopeContextRead}, []string{"*"})
	listed, _ = noData.ListTools(context.Background(), nil)
	for _, tool := range listed.Tools {
		if tool.Name == "run_operation" {
			t.Fatalf("run_operation registered for a credential without data:read (advertised %v)", noDataBoot.Capabilities.EnabledTools)
		}
	}
	if _, err := noDataBoot.Client.RunOperation(context.Background(), contractsv1.MCPRunOperationRequest{Operation: "hotspots"}); !isInsufficientScope(err) {
		t.Fatalf("a direct call without data:read: %v", err)
	}

	// No model seam was touched by any of the above.
	if n := stack.seam.used.Load(); n != 0 {
		t.Fatalf("the model seam was used %d times by data tools", n)
	}
}

func isInsufficientScope(err error) bool {
	return errors.Is(err, sidecar.ErrInsufficientScope)
}

func mustJSON(v any) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}
