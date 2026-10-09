package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7072 (S1a): data_catalog, find_subjects and run_operation on the
// REAL App, over the REAL authenticator (the S0 harness), the REAL subject
// gate, the REAL operation runner and policy artifact, and the REAL
// HTTPQueryClient talking to a fake ops query service that records every
// request it receives.

const (
	c7072RepoA = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" // slug hostedTestRepository: in the restricted grant
	c7072RepoB = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" // slug acme/other: outside the grant
	c7072SlugB = "acme/other-secret-repo"
)

// c7072Graph is the caller's organization graph: two repositories. The gate
// side applies the REAL shared predicate; the lookup side lists by kind.
type c7072Graph struct{}

func c7072Nodes() []directread.LookupNode {
	node := func(id, slug string) directread.LookupNode {
		return directread.LookupNode{Kind: "repository", CanonicalID: "repository:" + id, Label: slug, Attributes: map[string]interface{}{
			"subject_kind": "repository", "canonical_id": "repository:" + id, "label": slug, "authorization_repositories": []string{slug},
		}}
	}
	return []directread.LookupNode{node(c7072RepoA, hostedTestRepository), node(c7072RepoB, c7072SlugB)}
}

func (c7072Graph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{GraphKey: "c7072", Epoch: 1}, nil
}

func (c7072Graph) AuthorizeStoredSubjects(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	nodes := map[string][]graphrank.CandidateNode{}
	if principal.OrgID == "org_1" {
		for _, node := range c7072Nodes() {
			ref := contextfabric.SubjectRef{Kind: contextfabric.SubjectKind(node.Kind), CanonicalID: node.CanonicalID}
			nodes[graphrank.SubjectKey(ref)] = []graphrank.CandidateNode{{Attributes: node.Attributes}}
		}
	}
	return graphrank.AuthorizeStoredSubjectNodes(principal, subjects, nodes), nil
}

func (c7072Graph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

func (c7072Graph) ListSubjectsByKind(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, kind, after string, pageSize int) (directread.LookupPage, error) {
	var out []directread.LookupNode
	if principal.OrgID == "org_1" {
		for _, node := range c7072Nodes() {
			if node.Kind == kind && node.CanonicalID > after {
				out = append(out, node)
			}
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

func (c7072Graph) FindSubjectsByExactName(_ context.Context, principal storage.Principal, _ contextfabric.ResolvedGraphBinding, query string, kind string, _ string, _ int) (directread.LookupPage, error) {
	var page directread.LookupPage
	if principal.OrgID != "org_1" {
		return page, nil
	}
	for _, node := range c7072Nodes() {
		{
			if node.Kind == kind && strings.EqualFold(node.Label, query) {
				node.Match = directread.MatchExact
				page.Nodes = append(page.Nodes, node)
			}
		}
	}
	return page, nil
}

// c7072Upstream is the fake ops query service.
type c7072Upstream struct {
	server  *httptest.Server
	mu      sync.Mutex
	seen    []*http.Request
	bodies  []map[string]any
	respond func(body map[string]any) string
}

func newC7072Upstream(t *testing.T) *c7072Upstream {
	u := &c7072Upstream{}
	u.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		u.mu.Lock()
		u.seen = append(u.seen, r.Clone(context.Background()))
		u.bodies = append(u.bodies, body)
		respond := u.respond
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if respond == nil {
			_, _ = io.WriteString(w, `{"data":{}}`)
			return
		}
		_, _ = io.WriteString(w, respond(body))
	}))
	t.Cleanup(u.server.Close)
	return u
}

func (u *c7072Upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.seen)
}

// compoundingRiskAnswer is a vendored-document-shaped compoundingRisk answer
// with one row per id, plus a field the document never selects (it must be
// removed by the output allowlist).
func compoundingRiskAnswer(ids ...string) string {
	rows := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, map[string]any{"day": "2026-09-27", "scope": "REPO", "scopeId": id, "scopeLabel": "repo", "score": 0.42, "severity": "ELEVATED", "undeclaredPersonField": "Jane Doe"})
	}
	encoded, _ := json.Marshal(map[string]any{"data": map[string]any{"compoundingRisk": map[string]any{"orgId": "org_1", "breakout": "REPO", "rows": rows, "trend": []any{}}}})
	return string(encoded)
}

type c7072Options struct {
	noQueryURL bool
	noGraph    bool
}

type c7072Harness struct {
	*chaos7071Harness
	upstream *c7072Upstream
}

func newC7072Harness(t *testing.T, opts c7072Options) *c7072Harness {
	t.Helper()
	catalogue, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	upstream := newC7072Upstream(t)
	configure := func(deps *RuntimeDependencies) {
		deps.DataCatalogue = catalogue
		graph := c7072Graph{}
		var gate *directread.SubjectGate
		if !opts.noGraph {
			gate = directread.NewSubjectGate(graph, nil)
			deps.DirectReadGate = gate
			deps.DataSubjects = directread.NewSubjectLookup(graph, gate, nil)
		}
		if opts.noQueryURL {
			return
		}
		client, err := directread.NewHTTPQueryClientWithHTTP(upstream.server.URL, 5*time.Second, upstream.server.Client())
		if err != nil {
			t.Fatal(err)
		}
		runnerGate := gate
		var grants directread.GrantedRepositories
		if runnerGate == nil {
			runnerGate = directread.NewSubjectGate(nil, nil)
		} else {
			grants = directread.NewGrantedRepositories(directread.NewSubjectLookup(graph, gate, nil))
		}
		runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{Catalogue: catalogue, Gate: runnerGate, Client: client, Grants: grants})
		if err != nil {
			t.Fatal(err)
		}
		deps.DataOperations = runner
	}
	return &c7072Harness{chaos7071Harness: newChaos7071Harness(t, 100, configure), upstream: upstream}
}

func (h *c7072Harness) post(path, token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

func (h *c7072Harness) get(path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

func decodeC7072(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status %d body %s", response.Code, response.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

var (
	// c7072AllRepos is the universal ("*") grant: an unrestricted caller.
	c7072AllRepos = []string{"*"}
	c7072Data     = []string{auth.ScopeContextRead, auth.ScopeDataRead}
	c7072Context  = []string{auth.ScopeContextRead}
)

// The protection chain on a fully composed deployment: 401 without a
// bearer; 403 for operations without data:read; 403 for the catalogue and
// the subject lookup without context:read; 429 on the Data class.
func TestChaos7072DataRoutesProtectionChainWhenComposed(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	contextOnly := h.issueFor(t, c7072Context, c7072AllRepos, nil).Token
	dataOnly := h.issueFor(t, []string{auth.ScopeDataRead}, c7072AllRepos, nil).Token
	assertErrorResponse(t, h.post(ContextFabricDataOperationsPath, "", `{"operation":"compoundingRisk"}`), http.StatusUnauthorized, "invalid_token")
	assertErrorResponse(t, h.post(ContextFabricDataSubjectsPath, "", `{"kind":"repository"}`), http.StatusUnauthorized, "invalid_token")
	assertErrorResponse(t, h.post(ContextFabricDataOperationsPath, contextOnly, `{"operation":"compoundingRisk"}`), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.get(ContextFabricDataCatalogPath, dataOnly), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.post(ContextFabricDataSubjectsPath, dataOnly, `{"kind":"repository"}`), http.StatusForbidden, "insufficient_scope")
	if h.upstream.count() != 0 {
		t.Fatalf("a refused request reached the query service %d times", h.upstream.count())
	}

	limited := newChaos7071Harness(t, 1, func(deps *RuntimeDependencies) {
		base := newC7072Harness(t, c7072Options{})
		deps.DataCatalogue = base.app.runtime.DataCatalogue
		deps.DataOperations = base.app.runtime.DataOperations
		deps.DirectReadGate = base.app.runtime.DirectReadGate
	})
	token := limited.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	first := limited.call(http.MethodPost, ContextFabricDataOperationsPath, token)
	if first.Code == http.StatusTooManyRequests {
		t.Fatalf("first call already rate limited")
	}
	assertErrorResponse(t, limited.call(http.MethodPost, ContextFabricDataOperationsPath, token), http.StatusTooManyRequests, "rate_limited")
}

// Refusals are a typed body with HTTP 200 and ZERO upstream requests: a
// mutation name, a person-scoped variable value, and the K14-A
// basis_dependent_shape (investmentBreakdown by TEAM).
func TestChaos7072OperationRefusalsNeverReachTheQueryService(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	token := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	cases := []struct {
		name, body, code string
	}{
		{"mutation", `{"operation":"createSavedReport","variables":{}}`, "unknown_operation"},
		{"person variable", `{"operation":"catalogValues","variables":{"dimension":"AUTHOR"}}`, "person_scope_not_served"},
		{"K14 basis dependent shape", `{"operation":"investmentBreakdown","variables":{"batch":{"breakdowns":[{"dimension":"TEAM","dateRange":{"startDate":"2026-08-01","endDate":"2026-09-01"}}]}}}`, "basis_dependent_shape"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := decodeC7072(t, h.post(ContextFabricDataOperationsPath, token, tc.body))
			refusal, _ := body["refusal"].(map[string]any)
			if body["call"] != "refused" || refusal == nil || refusal["code"] != tc.code || refusal["reason"] == "" {
				t.Fatalf("refusal body %v", body)
			}
			if _, hasData := body["data"]; hasData {
				t.Fatalf("a refusal carried data: %v", body)
			}
			request, _ := body["request"].(map[string]any)
			if request == nil || request["max_bytes"] != float64(directread.DefaultOperationMaxBytes) {
				t.Fatalf("no effective request echo: %v", body)
			}
			if _, echoed := request["variables"]; echoed {
				t.Fatalf("a refused request echoed the client's variables: %v", request)
			}
			if tc.name == "mutation" && body["operation"] != "createSavedReport" {
				// a listed not-served name is echoed as its catalogue name
				t.Fatalf("operation echo %v", body["operation"])
			}
		})
	}
	if h.upstream.count() != 0 {
		t.Fatalf("refused shapes reached the query service %d times", h.upstream.count())
	}
}

// The served path, restricted: the forced scope is the grant only, and the
// exact upstream headers are the four internal identity headers plus
// X-Request-Id, with no Authorization. A foreign row in the answer refuses
// the WHOLE answer.
func TestChaos7072OperationRestrictedForcedScopeHeadersAndForeignRow(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	token := h.issueFor(t, c7072Data, []string{hostedTestRepository}, nil).Token
	h.upstream.respond = func(map[string]any) string { return compoundingRiskAnswer(c7072RepoA) }

	body := decodeC7072(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO"}}}`))
	if body["call"] != "served" {
		t.Fatalf("restricted served call: %v", body)
	}
	scope, _ := body["effective_scope"].(map[string]any)
	if scope["forced_by_grant"] != true || len(scope["repo_ids"].([]any)) != 1 || scope["repo_ids"].([]any)[0] != "repository:"+c7072RepoA {
		t.Fatalf("effective scope %v", scope)
	}
	if h.upstream.count() != 1 {
		t.Fatalf("upstream calls %d", h.upstream.count())
	}
	sent := h.upstream.bodies[0]["variables"].(map[string]any)
	filter := sent["filter"].(map[string]any)
	if ids := filter["repoIds"].([]any); len(ids) != 1 || ids[0] != c7072RepoA {
		t.Fatalf("forced repoIds %v", filter["repoIds"])
	}
	if sent["orgId"] != "org_1" {
		t.Fatalf("orgId %v", sent["orgId"])
	}
	header := h.upstream.seen[0].Header
	for name, want := range map[string]string{
		directread.HeaderInternalOrgID: "org_1", directread.HeaderInternalRole: directread.InternalRoleLeast,
		directread.HeaderInternalSuperuser: "false", directread.HeaderInternalImpersonationActive: "false",
	} {
		if got := header.Values(name); len(got) != 1 || got[0] != want {
			t.Fatalf("upstream header %s = %v, want %q", name, got, want)
		}
	}
	if header.Get("Authorization") != "" {
		t.Fatal("the internal call carried an Authorization header")
	}
	if header.Get(directread.HeaderRequestID) == "" {
		t.Fatal("the internal call carried no X-Request-Id")
	}
	if strings.Contains(h.upstream.seen[0].Header.Get(directread.HeaderInternalOrgID), token) {
		t.Fatal("bearer leaked")
	}

	h.upstream.respond = func(map[string]any) string { return compoundingRiskAnswer(c7072RepoA, c7072RepoB) }
	body = decodeC7072(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO"}}}`))
	refusal, _ := body["refusal"].(map[string]any)
	if body["call"] != "refused" || refusal["code"] != "row_outside_grant" {
		t.Fatalf("foreign row answer: %v", body)
	}
	if strings.Contains(h.lastBody(t, body), c7072RepoB) {
		t.Fatal("the foreign row id reached the caller")
	}

	// A restricted caller naming a repository outside the grant: one public
	// answer, no upstream request.
	before := h.upstream.count()
	body = decodeC7072(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO","repoIds":["repository:`+c7072RepoB+`"]}}}`))
	if refusal, _ := body["refusal"].(map[string]any); body["call"] != "refused" || refusal["code"] != "denied_or_not_found" {
		t.Fatalf("out-of-grant repository: %v", body)
	}
	if h.upstream.count() != before {
		t.Fatal("an out-of-grant repository reached the query service")
	}
}

func (h *c7072Harness) lastBody(t *testing.T, body map[string]any) string {
	t.Helper()
	encoded, _ := json.Marshal(body)
	return string(encoded)
}

// The served path, unrestricted: data passes, the output allowlist removes
// a path the document never selects, the echo carries the variables.
func TestChaos7072OperationUnrestrictedServedWithOutputAllowlist(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	token := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	h.upstream.respond = func(map[string]any) string { return compoundingRiskAnswer(c7072RepoA, c7072RepoB) }
	response := h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO"}}}`)
	body := decodeC7072(t, response)
	if body["call"] != "served" || body["result"] != "data" || body["completeness"] != "declared_complete" || body["consistency"] != "best_effort" {
		t.Fatalf("served body %v", body)
	}
	rows := body["data"].(map[string]any)["compoundingRisk"].(map[string]any)["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows %v", rows)
	}
	if strings.Contains(response.Body.String(), "undeclaredPersonField") || strings.Contains(response.Body.String(), "Jane Doe") {
		t.Fatalf("a path outside the output allowlist reached the caller: %s", response.Body.String())
	}
	if untrusted := body["untrusted_content"].(map[string]any); untrusted["untrusted"] != true {
		t.Fatalf("untrusted label %v", untrusted)
	}
	request := body["request"].(map[string]any)
	if request["operation"] != "compoundingRisk" || request["variables"] == nil {
		t.Fatalf("echo %v", request)
	}
	if scope := body["effective_scope"].(map[string]any); scope["forced_by_grant"] != false {
		t.Fatalf("unrestricted scope %v", scope)
	}
	if sent := h.upstream.bodies[0]["variables"].(map[string]any)["filter"].(map[string]any); sent["repoIds"] != nil {
		t.Fatalf("an unrestricted org-wide request was scoped: %v", sent)
	}
}

// Strict decode: unknown fields and a body over 16 KiB are 4xx before any
// work; a caller cannot set the organization in the body.
func TestChaos7072OperationRequestIsStrictlyDecoded(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	token := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	assertErrorResponse(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","org_id":"org_2"}`), http.StatusBadRequest, "invalid_request")
	assertErrorResponse(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":`), http.StatusBadRequest, "invalid_request")
	assertErrorResponse(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"x":"`+strings.Repeat("a", 17*1024)+`"}}`), http.StatusRequestEntityTooLarge, "invalid_request")
	body := decodeC7072(t, h.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"orgId":"org_2","filter":{"breakout":"REPO"}}}`))
	if refusal, _ := body["refusal"].(map[string]any); body["call"] != "refused" || refusal["code"] != "variable_not_allowed" {
		t.Fatalf("a client orgId was not refused: %v", body)
	}
	if h.upstream.count() != 0 {
		t.Fatal("a malformed request reached the query service")
	}
}

// Feature switch and gate: no query URL = 501 data_query_not_configured;
// URL but no graph (no subject gate) = 503; find_subjects without a graph =
// 503.
func TestChaos7072OperationsFeatureSwitchAndGateState(t *testing.T) {
	off := newC7072Harness(t, c7072Options{noQueryURL: true})
	token := off.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	response := off.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk"}`)
	assertErrorResponse(t, response, http.StatusNotImplemented, "feature_not_enabled")
	if !strings.Contains(response.Body.String(), contextFabricDataNotConfiguredReason) {
		t.Fatalf("no reason: %s", response.Body.String())
	}
	noGraph := newC7072Harness(t, c7072Options{noGraph: true})
	token = noGraph.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	assertErrorResponse(t, noGraph.post(ContextFabricDataOperationsPath, token, `{"operation":"compoundingRisk","variables":{"filter":{"breakout":"REPO"}}}`), http.StatusServiceUnavailable, "upstream_unavailable")
	assertErrorResponse(t, noGraph.post(ContextFabricDataSubjectsPath, token, `{"kind":"repository"}`), http.StatusServiceUnavailable, "upstream_unavailable")
	if noGraph.upstream.count() != 0 || off.upstream.count() != 0 {
		t.Fatal("an unconfigured deployment reached the query service")
	}
}

// find_subjects through the route: a restricted caller lists only its
// granted repository; a malformed request is 400.
func TestChaos7072SubjectsRoute(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	restricted := h.issueFor(t, c7072Context, []string{hostedTestRepository}, nil).Token
	response := h.post(ContextFabricDataSubjectsPath, restricted, `{"kind":"repository"}`)
	body := decodeC7072(t, response)
	subjects := body["subjects"].([]any)
	if len(subjects) != 1 || subjects[0].(map[string]any)["canonical_id"] != "repository:"+c7072RepoA {
		t.Fatalf("restricted list %v", subjects)
	}
	if strings.Contains(response.Body.String(), c7072SlugB) || strings.Contains(response.Body.String(), c7072RepoB) {
		t.Fatal("a repository outside the grant reached the caller")
	}
	if body["request"].(map[string]any)["mode"] != "list" || body["untrusted_content"].(map[string]any)["untrusted"] != true {
		t.Fatalf("echo or label %v", body)
	}
	assertErrorResponse(t, h.post(ContextFabricDataSubjectsPath, restricted, `{"kind":"not_a_kind"}`), http.StatusBadRequest, "invalid_request")
	assertErrorResponse(t, h.post(ContextFabricDataSubjectsPath, restricted, `{"kind":"repository","org":"org_2"}`), http.StatusBadRequest, "invalid_request")
}

// The catalogue for a restricted credential lists exactly the 3 operations
// served to that class; for an unrestricted credential, 19. Both list the
// mutation and the K14-A shape as not served, and the caller section never
// carries a repository name.
func TestChaos7072CatalogPerCallerClass(t *testing.T) {
	h := newC7072Harness(t, c7072Options{})
	restricted := h.issueFor(t, c7072Data, []string{hostedTestRepository}, nil).Token
	unrestricted := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
	for _, tc := range []struct {
		token string
		want  int
		class string
	}{{restricted, 2, "restricted"}, {unrestricted, 20, "unrestricted"}} {
		response := h.get(ContextFabricDataCatalogPath, tc.token)
		body := decodeC7072(t, response)
		operations := body["operations"].(map[string]any)
		if got := len(operations["operations"].([]any)); got != tc.want {
			t.Fatalf("%s: %d operations, want %d", tc.class, got, tc.want)
		}
		notServed := map[string]string{}
		for _, entry := range operations["not_served"].([]any) {
			m := entry.(map[string]any)
			notServed[m["name"].(string)] = m["code"].(string)
		}
		if notServed["createSavedReport"] != "unknown_operation" || notServed["busFactor"] != "unknown_operation" {
			t.Fatalf("%s: mutation / person operation not listed as not served: %v", tc.class, notServed)
		}
		k14 := false
		for _, entry := range operations["refused_shapes"].([]any) {
			m := entry.(map[string]any)
			if m["operation"] == "investmentBreakdown" && m["value"] == "TEAM" && m["code"] == "basis_dependent_shape" && strings.HasPrefix(m["reason"].(string), directread.BasisDependentShapeText) {
				k14 = true
			}
		}
		if !k14 {
			t.Fatalf("%s: the K14-A refusal is not listed", tc.class)
		}
		caller := body["caller"].(map[string]any)
		if len(caller) != 2 || caller["grant_class"] != tc.class {
			t.Fatalf("%s: caller section %v", tc.class, caller)
		}
		if strings.Contains(response.Body.String(), hostedTestRepository) {
			t.Fatalf("%s: the catalogue leaks a granted repository name", tc.class)
		}
		if body["facts"].(map[string]any)["note"] != directread.CatalogFactsNote {
			t.Fatalf("%s: facts note %v", tc.class, body["facts"])
		}
	}
	// A caller without data:read learns only that the section is not
	// available and why: no operation detail (CHAOS-7075 class sweep).
	contextOnly := h.issueFor(t, c7072Context, c7072AllRepos, nil).Token
	body := decodeC7072(t, h.get(ContextFabricDataCatalogPath+"?sections=operations", contextOnly))
	operations := body["operations"].(map[string]any)
	if operations["available"] != false || operations["reason"] != "scope_missing_data_read" || len(operations["operations"].([]any)) != 0 || len(operations["not_served"].([]any)) != 0 || len(operations["refused_shapes"].([]any)) != 0 {
		t.Fatalf("no-data:read catalogue %v", operations)
	}
	if _, ok := body["subjects"]; ok {
		t.Fatal("sections filter ignored")
	}
	assertErrorResponse(t, h.get(ContextFabricDataCatalogPath+"?sections=bogus", contextOnly), http.StatusBadRequest, "invalid_request")
}

// Capabilities advertise the data tools per design E.5.
func TestChaos7072CapabilitiesAdvertiseDataToolsPerE5(t *testing.T) {
	tools := func(h *c7072Harness, scopes []string) []string {
		t.Helper()
		token := h.issueFor(t, scopes, c7072AllRepos, nil).Token
		body := decodeC7072(t, h.get("/api/v1/agent-context/capabilities", token))
		var out []string
		for _, name := range body["enabled_tools"].([]any) {
			out = append(out, name.(string))
		}
		return out
	}
	has := func(list []string, name string) bool {
		for _, v := range list {
			if v == name {
				return true
			}
		}
		return false
	}
	full := newC7072Harness(t, c7072Options{})
	withData := tools(full, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeDataRead})
	if !has(withData, "run_operation") || !has(withData, "data_catalog") || !has(withData, "find_subjects") {
		t.Fatalf("fully composed, data:read: %v", withData)
	}
	withoutData := tools(full, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead})
	if has(withoutData, "run_operation") || !has(withoutData, "data_catalog") {
		t.Fatalf("no data:read: %v", withoutData)
	}
	noURL := tools(newC7072Harness(t, c7072Options{noQueryURL: true}), []string{auth.ScopeContextRead, auth.ScopeDataRead})
	if has(noURL, "run_operation") || !has(noURL, "find_subjects") {
		t.Fatalf("no query URL: %v", noURL)
	}
	noGraph := tools(newC7072Harness(t, c7072Options{noGraph: true}), []string{auth.ScopeContextRead, auth.ScopeDataRead})
	if has(noGraph, "run_operation") || has(noGraph, "data_catalog") || has(noGraph, "find_subjects") {
		t.Fatalf("no graph: %v", noGraph)
	}
}

// A broken proof (expired, spent, never issued) is an internal error, never
// a subject refusal.
func TestChaos7072BrokenProofIsInternalNotRefusal(t *testing.T) {
	for _, cause := range []error{directread.ErrAuthorizationExpired, directread.ErrAuthorizationSpent, directread.ErrUngatedRead} {
		h := newChaos7071Harness(t, 100, func(deps *RuntimeDependencies) {
			catalogue, _ := directread.DefaultCatalogue()
			deps.DataCatalogue = catalogue
			deps.DirectReadGate = directread.NewSubjectGate(c7072Graph{}, nil)
			deps.DataOperations = c7072FailingRunner{err: cause}
			deps.DataSubjects = c7072FailingFinder{err: cause}
		})
		token := h.issueFor(t, c7072Data, c7072AllRepos, nil).Token
		for _, path := range []string{ContextFabricDataOperationsPath, ContextFabricDataSubjectsPath} {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"operation":"compoundingRisk"}`))
			if path == ContextFabricDataSubjectsPath {
				request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"kind":"repository"}`))
			}
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			response := httptest.NewRecorder()
			h.app.Handler().ServeHTTP(response, request)
			assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
			if strings.Contains(response.Body.String(), "denied_or_not_found") {
				t.Fatalf("%v on %s answered as a subject refusal", cause, path)
			}
		}
	}
}

type c7072FailingRunner struct{ err error }

func (r c7072FailingRunner) Run(context.Context, storage.Principal, directread.OperationRequest) (directread.OperationResponse, error) {
	return directread.OperationResponse{}, r.err
}

type c7072FailingFinder struct{ err error }

func (f c7072FailingFinder) Find(context.Context, storage.Principal, directread.FindRequest) (directread.FindResponse, error) {
	return directread.FindResponse{}, f.err
}
