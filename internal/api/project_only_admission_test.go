package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// CHAOS-12301, the c5 step of CHAOS-12228: the one change that admits
// project_only. Design: docs/design/context-fabric-project-only-scope.md and
// CHAOS-12107 D2.

var liveProjects = []string{"project.v2:jira:ENG", "project.v2:linear:P-1"}

// serveCapturing runs the real protected chain around a handler that records
// the principal it was given.
func serveCapturing(t *testing.T, app *App, token string) (*httptest.ResponseRecorder, string, *storage.Principal) {
	t.Helper()
	var seen *storage.Principal
	handler := app.protectedRuntimeChain(limits.RequestClassContext, auth.ScopeContextRead, false, false, true,
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal, ok := auth.PrincipalFromContext(r.Context()); ok {
				seen = &principal
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	request := httptest.NewRequest(http.MethodGet, ContextFabricThreadsPath, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	var envelope struct {
		Error struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
	return recorder, envelope.Error.Code, seen
}

func projectAccess(repositories, projects []string) entitlements.PrincipalAccess {
	access := grantedAccess()
	access.Repositories, access.Projects = repositories, projects
	return access
}

// The D2 table: only a "*" consent grant with an empty live repository
// inventory and live projects becomes project_only.
func TestOnlyAStarGrantWithNoRepositoryInventoryBecomesProjectOnly(t *testing.T) {
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	const (
		projectOnly = "project_only"
		repository  = "repository scope"
		refused     = "no_repository"
	)
	for _, test := range []struct {
		name         string
		grant        []string
		repositories []string
		projects     []string
		want         string
	}{
		{"a star grant, no repositories, projects", []string{"*"}, nil, liveProjects, projectOnly},
		{"a star among other scopes, no repositories, projects", []string{"*", hostedTestRepository}, nil, liveProjects, projectOnly},
		{"a star grant, repositories and projects", []string{"*"}, []string{hostedTestRepository}, liveProjects, repository},
		{"a star grant, repositories, no projects", []string{"*"}, []string{hostedTestRepository}, nil, repository},
		{"a star grant, no repositories, no projects", []string{"*"}, nil, nil, refused},
		{"a star grant, an inventory of no valid slug, projects", []string{"*"}, []string{"not a slug"}, liveProjects, refused},
		{"an explicit repository grant, no repositories, projects", []string{hostedTestRepository}, nil, liveProjects, refused},
		{"an owner wildcard, no repositories, projects", []string{owner + "/*"}, nil, liveProjects, refused},
		{"no grant list, no repositories, projects", nil, nil, liveProjects, refused},
		{"an explicit grant outside the inventory, projects", []string{"zeta/x"}, []string{hostedTestRepository}, liveProjects, refused},
	} {
		for _, class := range userClasses() {
			t.Run(test.name+"/"+string(class), func(t *testing.T) {
				harness, _ := newMemorySubmissionHarness(t, 1)
				app := harness.apps[0]
				reader := &fakePrincipalAccess{}
				reader.set(projectAccess(test.repositories, test.projects), nil)
				app.runtime.PrincipalAccess = reader
				authenticateAsCredential(t, app, class, test.grant)
				recorder, code, seen := serveCapturing(t, app, harness.tokenA)
				switch test.want {
				case projectOnly:
					if recorder.Code != http.StatusNoContent || seen == nil {
						t.Fatalf("%d %s, want the handler reached", recorder.Code, code)
					}
					if got := auth.ClassifyGrant(*seen); got != auth.GrantProjectOnly {
						t.Fatalf("class %q, want project_only", got)
					}
					if !slices.Equal(seen.ProjectScopes, test.projects) {
						t.Fatalf("ProjectScopes = %q, want %q", seen.ProjectScopes, test.projects)
					}
					if !slices.Equal(seen.RepositoryScopes, []string{contractsv1.ProjectOnlyRepositoryScope}) {
						t.Fatalf("RepositoryScopes = %q, want only the reserved value", seen.RepositoryScopes)
					}
				case repository:
					if recorder.Code != http.StatusNoContent || seen == nil {
						t.Fatalf("%d %s, want the handler reached", recorder.Code, code)
					}
					if len(seen.ProjectScopes) != 0 || auth.ClassifyGrant(*seen) == auth.GrantProjectOnly {
						t.Fatalf("principal = %+v, want today's repository scope", seen)
					}
					if !slices.Equal(seen.RepositoryScopes, test.repositories) {
						t.Fatalf("RepositoryScopes = %q, want the effective scope %q", seen.RepositoryScopes, test.repositories)
					}
				case refused:
					if recorder.Code != http.StatusConflict || code != contractsv1.ErrorCodeNoRepository || seen != nil {
						t.Fatalf("%d %s (handler reached %v), want 409 no_repository before the handler", recorder.Code, code, seen != nil)
					}
				}
			})
		}
	}
}

// North Star check 18: the grant comes from the live read on every call. A
// project left, added, or a first repository all move the next call.
func TestAProjectOnlyGrantFollowsTheLiveReadOnEveryCall(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassNativeDevice, []string{"*"})
	for step, live := range []struct {
		repositories, projects []string
		wantProjects           []string
		wantStatus             int
	}{
		{nil, []string{"project.v2:linear:P-1"}, []string{"project.v2:linear:P-1"}, http.StatusNoContent},
		{nil, []string{"project.v2:linear:P-2"}, []string{"project.v2:linear:P-2"}, http.StatusNoContent},
		{nil, nil, nil, http.StatusConflict},
		{[]string{hostedTestRepository}, nil, nil, http.StatusNoContent},
		{nil, []string{"project.v2:linear:P-3"}, []string{"project.v2:linear:P-3"}, http.StatusNoContent},
	} {
		reader.set(projectAccess(live.repositories, live.projects), nil)
		recorder, code, seen := serveCapturing(t, app, harness.tokenA)
		if calls := reader.reset(); len(calls) != 1 {
			t.Fatalf("step %d: %d access reads, want exactly one", step, len(calls))
		}
		if recorder.Code != live.wantStatus {
			t.Fatalf("step %d: %d %s, want %d", step, recorder.Code, code, live.wantStatus)
		}
		if seen != nil && !slices.Equal(seen.ProjectScopes, live.wantProjects) {
			t.Fatalf("step %d: ProjectScopes = %q, want %q", step, seen.ProjectScopes, live.wantProjects)
		}
	}
}

// Order unchanged: every refusal of the read comes before the scope check, so
// projects never buy a lost member, an impersonator or a flag that is off.
func TestAProjectScopeNeverOutranksARefusalOfTheRead(t *testing.T) {
	for name, deny := range map[string]func(*entitlements.PrincipalAccess){
		"not a member":  func(access *entitlements.PrincipalAccess) { access.Member = false },
		"impersonating": func(access *entitlements.PrincipalAccess) { access.Impersonating = true },
		"flag off":      func(access *entitlements.PrincipalAccess) { access.AgentContextRuntime = false },
	} {
		t.Run(name, func(t *testing.T) {
			harness, _ := newMemorySubmissionHarness(t, 1)
			app := harness.apps[0]
			reader := &fakePrincipalAccess{}
			access := projectAccess(nil, liveProjects)
			deny(&access)
			reader.set(access, nil)
			app.runtime.PrincipalAccess = reader
			authenticateAsCredential(t, app, storage.CredentialClientClassNativeDevice, []string{"*"})
			recorder, code, seen := serveCapturing(t, app, harness.tokenA)
			if recorder.Code != http.StatusForbidden || seen != nil {
				t.Fatalf("%d %s (handler reached %v), want 403 before the handler", recorder.Code, code, seen != nil)
			}
		})
	}
}

// Credentials with no user are never read, so they are never project_only.
func TestACredentialWithNoUserIsNeverProjectOnly(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(projectAccess(nil, liveProjects), nil)
	app.runtime.PrincipalAccess = reader
	for _, class := range []storage.CredentialClientClass{storage.CredentialClientClassOrgBearer, storage.CredentialClientClassOperator, storage.CredentialClientClassWorkload} {
		authenticateAsCredential(t, app, class, []string{"*"})
		_, _, seen := serveCapturing(t, app, harness.tokenA)
		if calls := reader.reset(); len(calls) != 0 {
			t.Fatalf("class %q: %d access reads, want none", class, len(calls))
		}
		if seen == nil || len(seen.ProjectScopes) != 0 || !slices.Equal(seen.RepositoryScopes, []string{"*"}) {
			t.Fatalf("class %q: principal = %+v, want it unchanged", class, seen)
		}
	}
}

// Every protected route takes the narrowed principal past the read. The
// reserved value is set after normalization and nothing downstream normalizes
// it again: no route fails on it.
func TestEveryProtectedRouteTakesAProjectOnlyPrincipalPastTheRead(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassNativeDevice, []string{"*"})
	for _, route := range protectedRoutes() {
		reader.set(projectAccess(nil, liveProjects), nil)
		recorder, code := serveProtected(app, harness.tokenA, route, nil)
		if calls := reader.reset(); len(calls) != 1 {
			t.Errorf("%s %s: %d access reads, want one", route.method, route.path, len(calls))
		}
		if slices.Contains(accessCodes, code) || recorder.Code == http.StatusInternalServerError || code == "internal_error" {
			t.Errorf("%s %s: refused %d %s: %s", route.method, route.path, recorder.Code, code, recorder.Body.String())
		}
	}
}

// The admission basis is logged from the narrowed principal: at Info, with
// the project-only class, and never an id.
func TestALiveProjectOnlyAdmissionWritesItsNarrowedBasisAtInfo(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(projectAccess(nil, liveProjects), nil)
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"*"})
	logs := harness.logs[0]
	logs.Reset()
	recorder, code, _ := serveCapturing(t, app, harness.tokenA)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("%d %s", recorder.Code, code)
	}
	var granted map[string]any
	for _, entry := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var decoded map[string]any
		if json.Unmarshal([]byte(entry), &decoded) == nil && decoded["msg"] == "principal access read granted" {
			granted = decoded
		}
	}
	if granted == nil || granted["level"] != "INFO" || granted["grant_class"] != "project_only" ||
		granted["project_scope_count"] != float64(len(liveProjects)) || granted["grant_digest_class"] != "project" ||
		granted["scope_repositories"] != float64(0) {
		t.Fatalf("granted line = %v, want the project-only basis at Info:\n%s", granted, logs.String())
	}
	for _, project := range liveProjects {
		if strings.Contains(logs.String(), project) {
			t.Fatalf("a project identity reached the log:\n%s", logs.String())
		}
	}
}

const projectOnlyTestOrg = "8f6b9b8e-2d0b-4a6e-9a3e-1c2d3e4f5a6b"

// realAccessClient is the real access read against a stand-in for go-api.
func realAccessClient(t *testing.T, status int, body string) *entitlements.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/principals/"+accessTestUserID+"/access") {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_standin_token_0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 16 << 10,
		PositiveCacheTTL: time.Second, NegativeCacheTTL: time.Second, CacheCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// opsAccessAnswer is go-api's answer for an organization with no repository
// inventory, with its project scope flag on (ops CHAOS-12224, accessBody).
func opsAccessAnswer(projectScope string) string {
	return `{"schema_version":"acr_principal_access.v1","org_id":"` + projectOnlyTestOrg + `","user_id":"` + accessTestUserID + `",` +
		`"member":true,"impersonating":false,"entitlements":{"ask_dev":true,"agent_context_runtime":true},` +
		`"repository_scope":{"kind":"none"},"project_scope":` + projectScope + `}`
}

func authenticateInProjectOnlyOrg(t *testing.T, app *App, grant []string) {
	t.Helper()
	authenticator, err := auth.NewAuthenticator(classCredentialStore{row: contractsv1.ClientCredential{
		CredentialID: "access_test_credential", OrgID: projectOnlyTestOrg, RepositoryScopes: grant,
		Scopes:     []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeDataRead},
		Provenance: contractsv1.CredentialProvenance{ClientClass: string(storage.CredentialClientClassNativeDevice), ApprovingUserID: accessTestUserID},
	}}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
}

// End to end, with no fake in between: an ops-shaped answer through the real
// client, decoder and access gate to a principal the node rule reads. It
// admits a member work item and refuses a non-member, a work item with no
// stated membership, a project outside the grant and every repository.
func TestAnOpsProjectScopeReachesTheGatesAsAProjectOnlyPrincipal(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	app.runtime.PrincipalAccess = realAccessClient(t, http.StatusOK, opsAccessAnswer(
		`{"kind":"projects","projects":[{"provider":"jira","id":"ENG"},{"provider":"linear","id":"a:b"}]}`))
	authenticateInProjectOnlyOrg(t, app, []string{"*"})
	recorder, code, seen := serveCapturing(t, app, harness.tokenA)
	if recorder.Code != http.StatusNoContent || seen == nil {
		t.Fatalf("%d %s: %s", recorder.Code, code, recorder.Body.String())
	}
	if want := []string{"project.v2:jira:ENG", "project.v2:linear:a%3Ab"}; !slices.Equal(seen.ProjectScopes, want) {
		t.Fatalf("ProjectScopes = %q, want %q", seen.ProjectScopes, want)
	}
	workItem := func(projects any) map[string]interface{} {
		attributes := map[string]interface{}{"subject_kind": string(contextfabric.SubjectWorkItem), "canonical_id": "work_item.v2:r:1"}
		if projects != nil {
			attributes[graphrank.AuthorizationProjectIdentitiesAttr] = projects
		}
		return attributes
	}
	project := func(id string) map[string]interface{} {
		return map[string]interface{}{"subject_kind": string(contextfabric.SubjectProject), "canonical_id": id}
	}
	for name, test := range map[string]struct {
		attributes map[string]interface{}
		admitted   bool
	}{
		"a member work item":             {workItem([]string{"project.v2:linear:a%3Ab"}), true},
		"a work item of another project": {workItem([]string{"project.v2:linear:OTHER"}), false},
		"a work item in no project":      {workItem([]string{}), false},
		"a work item with none stated":   {workItem(nil), false},
		"a granted project":              {project("project.v2:jira:ENG"), true},
		"a project outside the grant":    {project("project.v2:jira:OTHER"), false},
	} {
		if got := graphrank.AuthorizedAttributes(*seen, contextfabric.RequestedScope{}, test.attributes); got != test.admitted {
			t.Errorf("%s: admitted = %v, want %v", name, got, test.admitted)
		}
	}
	if err := auth.AuthorizeRepository(*seen, hostedTestRepository); err == nil {
		t.Error("a repository was authorized")
	}
	if digest := contextfabric.StoredResultGrantDigest(*seen); !strings.HasPrefix(digest, "p:") {
		t.Errorf("grant digest = %q, want a project digest", digest)
	}
}

// With the ops flag off nothing moves: a "*" grant in an organization with no
// repositories stays no_repository (the roll order's safe side).
func TestAnOpsAnswerWithNoProjectScopeStaysNoRepository(t *testing.T) {
	for name, body := range map[string]string{
		"no project_scope": strings.Replace(opsAccessAnswer(`null`), `,"project_scope":null`, "", 1),
		"kind none":        opsAccessAnswer(`{"kind":"none"}`),
	} {
		harness, _ := newMemorySubmissionHarness(t, 1)
		app := harness.apps[0]
		app.runtime.PrincipalAccess = realAccessClient(t, http.StatusOK, body)
		authenticateInProjectOnlyOrg(t, app, []string{"*"})
		recorder, code, seen := serveCapturing(t, app, harness.tokenA)
		if recorder.Code != http.StatusConflict || code != contractsv1.ErrorCodeNoRepository || seen != nil {
			t.Fatalf("%s: %d %s, want 409 no_repository", name, recorder.Code, code)
		}
	}
}

// A malformed scope is a failed read: 503, retryable, never a class change.
func TestAMalformedProjectScopeIsAFailedRead(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	app.runtime.PrincipalAccess = realAccessClient(t, http.StatusOK, opsAccessAnswer(`{"kind":"projects","projects":[]}`))
	authenticateInProjectOnlyOrg(t, app, []string{"*"})
	recorder, code, seen := serveCapturing(t, app, harness.tokenA)
	if recorder.Code != http.StatusServiceUnavailable || code != "upstream_unavailable" || seen != nil {
		t.Fatalf("%d %s, want 503 upstream_unavailable before the handler", recorder.Code, code)
	}
}

// Over its limit an inventory is a typed limitation with its own reason: the
// status and the code stay (a new code would be a wire value), nothing is
// admitted, and a retry cannot clear it. Any other 503 stays a retryable
// outage with the failure line.
func TestAnOverLimitInventoryIsATypedLimitationNotAnOutage(t *testing.T) {
	for _, test := range []struct {
		name, body, reason string
		limitation         bool
	}{
		{"projects", `{"detail":"Service unavailable","code":"acr_access_project_scope_over_limit"}`, "project_scope_over_limit", true},
		{"repositories", `{"detail":"Service unavailable","code":"acr_access_repository_scope_over_limit"}`, "repository_scope_over_limit", true},
		{"a plain 503", `{"detail":"Service unavailable"}`, "status", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			harness, _ := newMemorySubmissionHarness(t, 1)
			app := harness.apps[0]
			app.runtime.PrincipalAccess = realAccessClient(t, http.StatusServiceUnavailable, test.body)
			authenticateInProjectOnlyOrg(t, app, []string{"*"})
			logs := harness.logs[0]
			logs.Reset()
			recorder, code, seen := serveCapturing(t, app, harness.tokenA)
			if recorder.Code != http.StatusServiceUnavailable || code != "upstream_unavailable" || seen != nil {
				t.Fatalf("%d %s, want 503 upstream_unavailable before the handler", recorder.Code, code)
			}
			var envelope struct {
				Error struct {
					Retryable bool   `json:"retryable"`
					Message   string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Retryable == test.limitation {
				t.Fatalf("retryable = %v, want %v", envelope.Error.Retryable, !test.limitation)
			}
			refused := strings.Contains(logs.String(), `"msg":"principal access read refused"`)
			failed := strings.Contains(logs.String(), `"msg":"principal access read failed"`)
			if refused != test.limitation || failed == test.limitation || !strings.Contains(logs.String(), `"reason":"`+test.reason+`"`) {
				t.Fatalf("log lines refused=%v failed=%v, want reason %q:\n%s", refused, failed, test.reason, logs.String())
			}
			if test.limitation {
				if envelope.Error.Message != contractsv1.ContextFabricAccessInventoryOverLimitMessage {
					t.Fatalf("message = %q", envelope.Error.Message)
				}
			}
			audited := false
			if store, ok := app.runtime.Audit.(*memory.AuditStore); ok {
				for _, event := range store.Events() {
					audited = audited || (event.Action == "access_denied" && event.ResourceID == test.reason)
				}
				if audited != test.limitation {
					t.Fatalf("audited = %v, want %v", audited, test.limitation)
				}
			}
		})
	}
}

// AC-8 (CHAOS-12107): project_only never reopens a "*" investigation. A web
// assertion may name every repository on the two credential approvals only,
// so a "*" assertion on the investigation route is refused at authentication,
// with either permission set, and the explicit form is the control.
func TestAStarInvestigationAssertionIsRefused(t *testing.T) {
	now := time.Now().UTC()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := auth.NewWebAssertionVerifier(auth.WebAssertionOptions{
		Issuer: "https://web.example.test", Audience: "acr-api", JWKSPath: writeAPIJWKS(t, public), Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, _ := newHostedTestAppWithWebAssertions(t, nil, nil, nil, nil, nil, verifier)
	body, err := json.Marshal(investigationRequestBody())
	if err != nil {
		t.Fatal(err)
	}
	sign := func(request *http.Request, repositories, permissions []string, jti string) string {
		digest := sha256.Sum256(body)
		claims := map[string]any{
			"iss": "https://web.example.test", "aud": "acr-api", "sub": "user_123", "org_id": "org_1",
			"repository_scopes": repositories, "permissions": permissions,
			"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": jti,
			"method": request.Method, "path": request.URL.EscapedPath(), "body_sha256": base64.RawURLEncoding.EncodeToString(digest[:]),
		}
		header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "JWT", "kid": "current"})
		payload, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
	}
	serve := func(repositories, permissions []string, jti string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, strings.NewReader(string(body)))
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-star-0001")
		request.Header.Set(auth.WebAssertionHeader, sign(request, repositories, permissions, jti))
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	for index, permissions := range [][]string{{auth.ScopeContextRead}, {auth.WebAssertionPermissionCredentialIssue}} {
		response := serve([]string{"*"}, permissions, fmt.Sprintf("star_%d", index))
		assertErrorResponse(t, response, http.StatusUnauthorized, "invalid_token")
	}
	if response := serve([]string{hostedTestRepository}, []string{auth.ScopeContextRead}, "explicit_1"); response.Code == http.StatusUnauthorized {
		t.Fatalf("the explicit assertion: %d %s, want it past authentication", response.Code, response.Body.String())
	}
}
