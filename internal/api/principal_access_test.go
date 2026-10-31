package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12092 (Ask Dev direct-browser design §3.1-3.2, North Star check 18):
// every protected call by a user-class credential (browser, OAuth user,
// native device) re-reads the user's access live, with no cache, and is
// refused before its handler when the answer refuses it.

const (
	accessTestUserID    = "11111111-2222-4333-8444-555555555555"
	accessTestSessionID = "ses_AAAAAAAAAAAAAAAAAAAAAA"
)

// fakePrincipalAccess is a PrincipalAccessReader that answers one settable
// access (or error), counts every read and records what it was asked.
type fakePrincipalAccess struct {
	mu     sync.Mutex
	access entitlements.PrincipalAccess
	err    error
	delay  time.Duration
	calls  []principalAccessCall
}

type principalAccessCall struct{ org, user, session string }

func (f *fakePrincipalAccess) PrincipalAccess(ctx context.Context, orgID, userID, sessionID string) (entitlements.PrincipalAccess, error) {
	f.mu.Lock()
	f.calls = append(f.calls, principalAccessCall{org: orgID, user: userID, session: sessionID})
	access, err, delay := f.access, f.err, f.delay
	f.mu.Unlock()
	if sessionID == "" {
		// As the real read: no session object unless one was asked for.
		access.Session = nil
	}
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return entitlements.PrincipalAccess{}, ctx.Err()
		}
	}
	return access, err
}

func (f *fakePrincipalAccess) set(access entitlements.PrincipalAccess, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access, f.err = access, err
}

func (f *fakePrincipalAccess) reset() []principalAccessCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// grantedAccess is a member with both flags, a live session and the harness
// repository in the inventory.
func grantedAccess() entitlements.PrincipalAccess {
	return entitlements.PrincipalAccess{
		Member: true, Session: &entitlements.SessionAccess{Live: true},
		AskDev: true, AgentContextRuntime: true, Repositories: []string{hostedTestRepository},
	}
}

// authenticateAsCredential makes app authenticate every bearer as one
// credential row of class, with every scope and the grant repositories.
func authenticateAsCredential(t *testing.T, app *App, class storage.CredentialClientClass, grant []string) {
	t.Helper()
	provenance := contractsv1.CredentialProvenance{ClientClass: string(class)}
	switch class {
	case storage.CredentialClientClassBrowser:
		provenance.ApprovingUserID, provenance.SessionID = accessTestUserID, accessTestSessionID
	case storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice:
		provenance.ApprovingUserID = accessTestUserID
	}
	authenticator, err := auth.NewAuthenticator(classCredentialStore{row: contractsv1.ClientCredential{
		CredentialID: "access_test_credential", OrgID: "org_1", RepositoryScopes: grant,
		Scopes:     []string{auth.ScopeContextRead, auth.ScopeEvidenceRead, auth.ScopeEpisodeWrite, auth.ScopeDataRead},
		Provenance: provenance,
	}}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
}

type protectedRoute struct{ method, path, body string }

// protectedRoutes are every route app.go wraps in protectedRuntimeHandler.
func protectedRoutes() []protectedRoute {
	investigation, _ := json.Marshal(investigationRequestBody())
	return []protectedRoute{
		{http.MethodGet, "/api/v1/agent-context/capabilities", ""},
		{http.MethodPost, "/api/v1/agent-context/context-packets", "{}"},
		{http.MethodGet, "/api/v1/agent-context/evidence/ev_access_test", ""},
		{http.MethodPost, "/api/v1/agent-context/episodes", "{}"},
		{http.MethodPost, ContextFabricInvestigationsPath, string(investigation)},
		{http.MethodGet, "/api/v1/context-fabric/investigations/res_access_test", ""},
		{http.MethodGet, "/api/v1/context-fabric/investigations/submissions/submission-access-01", ""},
		{http.MethodGet, ContextFabricThreadsPath, ""},
		{http.MethodGet, "/api/v1/context-fabric/threads/thread-access-0001", ""},
		{http.MethodDelete, "/api/v1/context-fabric/threads/thread-access-0001", ""},
		{http.MethodGet, ContextFabricDataCatalogPath, ""},
		{http.MethodPost, ContextFabricDataSubjectsPath, "{}"},
		{http.MethodPost, ContextFabricDataFactsPath, "{}"},
		{http.MethodPost, ContextFabricDataOperationsPath, "{}"},
		{http.MethodPost, ContextFabricDataGraphQLPath, "{}"},
		{http.MethodPost, ContextFabricDataRelationshipsPath, "{}"},
	}
}

func serveProtected(app *App, token string, route protectedRoute, headers map[string]string) (*httptest.ResponseRecorder, string) {
	var body *bytes.Reader
	if route.body != "" {
		body = bytes.NewReader([]byte(route.body))
	} else {
		body = bytes.NewReader(nil)
	}
	request := httptest.NewRequest(route.method, route.path, body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(contractsv1.ContextFabricSubmissionIDHeader, "submission-access-01")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
	return recorder, envelope.Error.Code
}

func userClasses() []storage.CredentialClientClass {
	return []storage.CredentialClientClass{storage.CredentialClientClassBrowser, storage.CredentialClientClassOAuthUser, storage.CredentialClientClassNativeDevice}
}

type accessDenial struct {
	name   string
	access func() entitlements.PrincipalAccess
	err    error
	status int
	code   string
	// browserOnly: the reason refuses the browser class only.
	browserOnly bool
}

func accessDenials() []accessDenial {
	with := func(change func(*entitlements.PrincipalAccess)) func() entitlements.PrincipalAccess {
		return func() entitlements.PrincipalAccess {
			access := grantedAccess()
			change(&access)
			return access
		}
	}
	return []accessDenial{
		{name: "not a member", access: with(func(a *entitlements.PrincipalAccess) { a.Member = false }), status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied},
		{name: "impersonating", access: with(func(a *entitlements.PrincipalAccess) { a.Impersonating = true }), status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied},
		{name: "session impersonating", access: with(func(a *entitlements.PrincipalAccess) { a.Session.Impersonating = true }), status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied, browserOnly: true},
		{name: "session not live", access: with(func(a *entitlements.PrincipalAccess) { a.Session.Live = false }), status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied, browserOnly: true},
		{name: "principal unknown", access: grantedAccess, err: entitlements.ErrPrincipalUnknown, status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied},
		{name: "session id refused", access: grantedAccess, err: entitlements.ErrSessionInvalid, status: http.StatusForbidden, code: contractsv1.ErrorCodeAccessDenied},
		{name: "agent_context_runtime off", access: with(func(a *entitlements.PrincipalAccess) { a.AgentContextRuntime = false }), status: http.StatusForbidden, code: "feature_not_enabled"},
		{name: "ask_dev off", access: with(func(a *entitlements.PrincipalAccess) { a.AskDev = false }), status: http.StatusForbidden, code: "feature_not_enabled", browserOnly: true},
		{name: "empty inventory", access: with(func(a *entitlements.PrincipalAccess) { a.Repositories = nil }), status: http.StatusConflict, code: contractsv1.ErrorCodeNoRepository},
		{name: "grant outside the inventory", access: with(func(a *entitlements.PrincipalAccess) { a.Repositories = []string{"other-org/other-repo"} }), status: http.StatusConflict, code: contractsv1.ErrorCodeNoRepository},
		{name: "read failed", access: grantedAccess, err: errors.New("dev health down"), status: http.StatusServiceUnavailable, code: "upstream_unavailable"},
	}
}

// accessCodes are the codes only the access read writes.
var accessCodes = []string{contractsv1.ErrorCodeAccessDenied, contractsv1.ErrorCodeNoRepository}

func TestEveryProtectedRouteRereadsAccessForEveryUserClass(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	owner, _, _ := strings.Cut(hostedTestRepository, "/")
	for _, class := range userClasses() {
		// An owner grant, so a live inventory of another owner's
		// repositories is outside it.
		authenticateAsCredential(t, app, class, []string{owner + "/*"})
		wantSession := ""
		if class == storage.CredentialClientClassBrowser {
			wantSession = accessTestSessionID
		}
		for _, route := range protectedRoutes() {
			// Granted: the call passes the read, once, for this org, user
			// and (browser only) session.
			reader.set(grantedAccess(), nil)
			recorder, code := serveProtected(app, harness.tokenA, route, nil)
			calls := reader.reset()
			if slices.Contains(accessCodes, code) {
				t.Errorf("%s %s %s, granted: refused %d %s", class, route.method, route.path, recorder.Code, code)
			}
			if len(calls) != 1 || calls[0] != (principalAccessCall{org: "org_1", user: accessTestUserID, session: wantSession}) {
				t.Errorf("%s %s %s, granted: access reads %+v, want exactly one for org_1, %s, session %q", class, route.method, route.path, calls, accessTestUserID, wantSession)
			}
			for _, denial := range accessDenials() {
				reader.set(denial.access(), denial.err)
				attempts := harness.investigator.executions.Load()
				recorder, code := serveProtected(app, harness.tokenA, route, nil)
				calls := reader.reset()
				refused := recorder.Code == denial.status && code == denial.code
				if denial.browserOnly && class != storage.CredentialClientClassBrowser {
					if slices.Contains(accessCodes, code) || recorder.Code == http.StatusForbidden && code == "feature_not_enabled" {
						t.Errorf("%s %s %s, %s: refused %d %s, want the call to pass (browser-only reason)", class, route.method, route.path, denial.name, recorder.Code, code)
					}
					continue
				}
				if !refused || len(calls) != 1 {
					t.Errorf("%s %s %s, %s: %d %s after %d reads, want %d %s after one read: %s", class, route.method, route.path, denial.name, recorder.Code, code, len(calls), denial.status, denial.code, recorder.Body.String())
				}
				if got := harness.investigator.executions.Load(); got != attempts {
					t.Errorf("%s %s %s, %s: a refused call made %d model attempts", class, route.method, route.path, denial.name, got-attempts)
				}
			}
		}
	}
}

// Operator, workload, legacy credentials and web assertions carry no user:
// the read is never made for them, and their behaviour is unchanged.
func TestTheAccessReadIsNotMadeForNonUserCredentials(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(entitlements.PrincipalAccess{}, errors.New("must not be read"))
	app.runtime.PrincipalAccess = reader
	for _, class := range []storage.CredentialClientClass{storage.CredentialClientClassOperator, storage.CredentialClientClassWorkload, ""} {
		authenticateAsCredential(t, app, class, []string{"*"})
		for _, route := range protectedRoutes() {
			recorder, code := serveProtected(app, harness.tokenA, route, nil)
			if calls := reader.reset(); len(calls) != 0 || slices.Contains(accessCodes, code) {
				t.Errorf("class %q %s %s: %d %s after %d access reads, want no read", class, route.method, route.path, recorder.Code, code, len(calls))
			}
		}
	}
}

// No cross-call cache: a flag turned off between two calls well under 30
// seconds apart refuses the second one.
func TestAFlagTurnedOffBetweenTwoCallsRefusesTheSecond(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassBrowser, []string{"*"})
	route := protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}
	reader.set(grantedAccess(), nil)
	if recorder, code := serveProtected(app, harness.tokenA, route, nil); slices.Contains(accessCodes, code) || code == "feature_not_enabled" {
		t.Fatalf("first call: %d %s, want it served", recorder.Code, code)
	}
	for _, flag := range []string{askDevEntitlement, agentContextRuntimeEntitlement} {
		access := grantedAccess()
		if flag == askDevEntitlement {
			access.AskDev = false
		} else {
			access.AgentContextRuntime = false
		}
		reader.set(access, nil)
		if recorder, code := serveProtected(app, harness.tokenA, route, nil); recorder.Code != http.StatusForbidden || code != "feature_not_enabled" {
			t.Fatalf("call right after %s turned off: %d %s, want 403 feature_not_enabled", flag, recorder.Code, code)
		}
	}
	if calls := reader.reset(); len(calls) != 3 {
		t.Fatalf("access reads = %d, want 3 (one per call, none cached)", len(calls))
	}
}

// An empty effective scope refuses a replay and an evidence read too, before
// the handler: never an unrestricted [] scope.
func TestAnEmptyGrantRefusesReplayAndEvidence(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"acme/*"})
	access := grantedAccess()
	access.Repositories = []string{"other-org/repo-a"}
	reader.set(access, nil)
	for _, route := range []protectedRoute{
		{http.MethodGet, "/api/v1/context-fabric/investigations/res_access_test", ""},
		{http.MethodGet, "/api/v1/context-fabric/investigations/submissions/submission-access-01", ""},
		{http.MethodGet, "/api/v1/agent-context/evidence/ev_access_test", ""},
	} {
		if recorder, code := serveProtected(app, harness.tokenA, route, nil); recorder.Code != http.StatusConflict || code != contractsv1.ErrorCodeNoRepository {
			t.Errorf("%s %s with a grant outside the inventory: %d %s, want 409 no_repository", route.method, route.path, recorder.Code, code)
		}
	}
}

// A failed read is 503, never 401 and never a stale answer.
func TestAFailedAccessReadIs503(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassNativeDevice, []string{"*"})
	reader.set(grantedAccess(), errors.New("connection refused"))
	recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}, nil)
	if recorder.Code != http.StatusServiceUnavailable || code != "upstream_unavailable" {
		t.Fatalf("read failed: %d %s, want 503 upstream_unavailable", recorder.Code, code)
	}
}

// A browser credential with no bound session is refused without a read.
func TestABrowserCredentialWithoutASessionIsRefusedWithoutARead(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	authenticator, err := auth.NewAuthenticator(classCredentialStore{row: contractsv1.ClientCredential{
		CredentialID: "browser_no_session", OrgID: "org_1", RepositoryScopes: []string{"*"}, Scopes: []string{auth.ScopeContextRead},
		Provenance: contractsv1.CredentialProvenance{ClientClass: string(storage.CredentialClientClassBrowser), ApprovingUserID: accessTestUserID},
	}}, nil, auth.AuthenticatorOptions{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	app.authenticator = authenticator
	recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}, nil)
	if recorder.Code != http.StatusForbidden || code != contractsv1.ErrorCodeAccessDenied || len(reader.reset()) != 0 {
		t.Fatalf("browser without a session: %d %s, want 403 access_denied with no read", recorder.Code, code)
	}
}

// The effective scope is the grant within the live inventory: "*" is the
// inventory, owner/* keeps that owner's repositories, an exact grant keeps
// itself when listed, and nothing outside the grant is ever added.
func TestTheEffectiveScopeIsTheGrantWithinTheLiveInventory(t *testing.T) {
	inventory := []string{"acme/api", "acme/web", "Other/Tool", "not a slug"}
	for _, test := range []struct {
		grant []string
		want  []string
	}{
		{[]string{"*"}, []string{"acme/api", "acme/web", "other/tool"}},
		{[]string{"acme/*"}, []string{"acme/api", "acme/web"}},
		{[]string{"acme/web", "zeta/x"}, []string{"acme/web"}},
		{[]string{"zeta/*"}, []string{}},
	} {
		if got := effectiveRepositoryScope(test.grant, inventory); !slices.Equal(got, test.want) {
			t.Errorf("grant %v: effective %v, want %v", test.grant, got, test.want)
		}
	}
	if got := effectiveRepositoryScope([]string{"*"}, nil); len(got) != 0 {
		t.Errorf("empty inventory: effective %v, want empty", got)
	}
}

// The live read's flag is the one the handlers use: with the 30-second
// entitlement provider saying off, a user-class call the read grants is
// served, and the provider is not asked for agent_context_runtime.
func TestUserClassCallsUseTheLiveFlagNotTheCachedEntitlement(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	asked := 0
	app.runtime.Entitlements = EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		if entitlement == agentContextRuntimeEntitlement {
			asked++
			return false, nil
		}
		return true, nil
	})
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"*"})
	recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, "/api/v1/agent-context/capabilities", ""}, nil)
	if recorder.Code != http.StatusOK || code != "" || asked != 0 {
		t.Fatalf("capabilities: %d %s, cached provider asked %d times; want 200 and no cached read", recorder.Code, code, asked)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(recorder.Body.Bytes(), &capabilities); err != nil || !capabilities.Entitlements.AgentContextRuntime {
		t.Fatalf("capabilities entitlement = %+v (%v), want agent_context_runtime from the live read", capabilities.Entitlements, err)
	}
}

// Design §3.2: an investigation create whose budget left after the access
// read is below the minimum is refused budget_exhausted before admission: no
// submission row and no model attempt. An injected 8 s delay on a 15 s
// budget leaves 7 s.
func TestASlowAccessReadExhaustsTheCreateBudgetBeforeAdmission(t *testing.T) {
	if testing.Short() {
		t.Skip("waits 8 seconds")
	}
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{delay: 8 * time.Second}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassBrowser, []string{"*"})
	investigation, _ := json.Marshal(investigationRequestBody())
	recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodPost, ContextFabricInvestigationsPath, string(investigation)},
		map[string]string{contractsv1.ContextFabricWaitBudgetHeader: "15"})
	if recorder.Code != http.StatusServiceUnavailable || code != contractsv1.ErrorCodeBudgetExhausted {
		t.Fatalf("create after an 8 s access read on a 15 s budget: %d %s, want 503 budget_exhausted: %s", recorder.Code, code, recorder.Body.String())
	}
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("model attempts = %d, want 0", got)
	}
	// No submission row: the same id's status, read with no delay, is 404.
	reader.mu.Lock()
	reader.delay = 0
	reader.mu.Unlock()
	status, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, "/api/v1/context-fabric/investigations/submissions/submission-access-01", ""}, nil)
	if status.Code != http.StatusNotFound || code != "not_found" {
		t.Fatalf("status of the refused submission: %d %s, want 404 not_found (no row): %s", status.Code, code, status.Body.String())
	}
}

// The three refusals' bodies, written through the route by its own writers,
// are the published examples and validate against error.v1.
// ACR_WRITE_ACCESS_GOLDENS=1 rewrites them.
func TestTheAccessRefusalsAreThePublishedExamples(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassBrowser, []string{"*"})
	investigation, _ := json.Marshal(investigationRequestBody())
	notMember := grantedAccess()
	notMember.Member = false
	noRepository := grantedAccess()
	noRepository.Repositories = nil
	for _, example := range []struct {
		file   string
		access entitlements.PrincipalAccess
		delay  time.Duration
		route  protectedRoute
		budget string
		status int
	}{
		{"error_context_fabric_access_denied.v1.json", notMember, 0, protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}, "", http.StatusForbidden},
		{"error_context_fabric_no_repository.v1.json", noRepository, 0, protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}, "", http.StatusConflict},
	} {
		reader.set(example.access, nil)
		recorder, _ := serveProtected(app, harness.tokenA, example.route, map[string]string{"X-Request-ID": submissionTestRequestID})
		checkAccessGolden(t, example.file, recorder, example.status)
	}
	// budget_exhausted: a 10 s budget is below the minimum as soon as the
	// read has taken any time at all.
	reader.set(grantedAccess(), nil)
	reader.mu.Lock()
	reader.delay = 10 * time.Millisecond
	reader.mu.Unlock()
	recorder, _ := serveProtected(app, harness.tokenA, protectedRoute{http.MethodPost, ContextFabricInvestigationsPath, string(investigation)},
		map[string]string{"X-Request-ID": submissionTestRequestID, contractsv1.ContextFabricWaitBudgetHeader: "10"})
	checkAccessGolden(t, "error_context_fabric_budget_exhausted.v1.json", recorder, http.StatusServiceUnavailable)
	if got := harness.investigator.executions.Load(); got != 0 {
		t.Fatalf("model attempts = %d, want 0", got)
	}
}

func checkAccessGolden(t *testing.T, file string, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("%s: status %d, want %d: %s", file, recorder.Code, status, recorder.Body.String())
	}
	body := append(bytes.TrimRight(recorder.Body.Bytes(), "\n"), '\n')
	if err := contractcheck.ValidateSerializedStrict("", "error.v1.schema.json", body); err != nil {
		t.Fatalf("%s: schema rejects the body: %v", file, err)
	}
	path := repopath.Path(t, "contracts", "examples", "v1", file)
	if os.Getenv("ACR_WRITE_ACCESS_GOLDENS") == "1" {
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	golden, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(golden, body) {
		t.Fatalf("%s differs from the route's body (err %v):\n%s\nwant\n%s", file, err, body, golden)
	}
}

// The handler runs with the principal narrowed to the effective scope: a "*"
// grant reaches only the live inventory's repositories. The route-level
// entitlement gate and the context-packet route take agent_context_runtime
// from the live read, not from the cached provider (which says off here).
func TestTheHandlerSeesTheNarrowedScopeAndTheLiveFlag(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	reader.set(grantedAccess(), nil)
	app.runtime.PrincipalAccess = reader
	app.runtime.Entitlements = EntitlementFunc(func(_ context.Context, _ string, entitlement string) (bool, error) {
		return entitlement != agentContextRuntimeEntitlement, nil
	})
	authenticateAsCredential(t, app, storage.CredentialClientClassNativeDevice, []string{"*"})
	serve := func(request *http.Request) (*httptest.ResponseRecorder, string) {
		recorder := httptest.NewRecorder()
		app.Handler().ServeHTTP(recorder, request)
		var envelope struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &envelope)
		return recorder, envelope.Error.Code
	}
	if recorder, code := serve(contextPacketRequest(t, app, harness.tokenA, contextRequestFor("other-org/other-repo"))); recorder.Code != http.StatusForbidden || code != "repo_forbidden" {
		t.Fatalf("context packet outside the live inventory under a * grant: %d %s, want 403 repo_forbidden", recorder.Code, code)
	}
	if recorder, code := serve(contextPacketRequest(t, app, harness.tokenA, contextRequestFor(hostedTestRepository))); code == "repo_forbidden" || code == "feature_not_enabled" {
		t.Fatalf("context packet inside the live inventory: %d %s, want it past the scope and flag gates", recorder.Code, code)
	}
	if recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, ContextFabricThreadsPath, ""}, nil); recorder.Code != http.StatusOK {
		t.Fatalf("thread list (an entitlement-gated route) with the live flag on and the cached one off: %d %s, want 200", recorder.Code, code)
	}
}
