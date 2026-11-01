package api

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12425: a user-class credential whose effective scope is empty gets
// 409 no_repository from the live access check, before any handler, on every
// route behind protectedRuntimeChain. The OpenAPI must list that 409 on each
// of them, because web maps exactly the statuses the OpenAPI names.

type openAPIOperation struct {
	method, template string
	secured          bool
	statuses         map[string]bool
}

// noRepositoryExceptions are secured OpenAPI operations that do NOT pass
// through the live access check, so they need not list 409 no_repository.
// Each carries its reason. A stale entry (no such operation) fails the test.
var noRepositoryExceptions = map[string]string{
	"GET /api/v1/agent-context/context-packets/{context_packet_id}": "documented but not registered in app.go: no handler, so no access check; move it into protectedRoutes() when a handler is added",
	"POST /api/v1/oauth/device_approval":                            "not behind protectedRuntimeChain; its 409 is a different code (device authorization state)",
	"POST /api/v1/auth/credentials/self/rotate":                     "credential lifecycle line, not behind requireLiveAccess; its 409 is credential_lifecycle_conflict",
	"POST /api/v1/auth/credentials/self/revoke":                     "credential lifecycle line, not behind requireLiveAccess; its 409 is credential_lifecycle_conflict",
	"POST /api/v1/auth/credentials/self/ack":                        "credential lifecycle line, not behind requireLiveAccess; its 409 is credential_lifecycle_conflict",
}

func loadOpenAPIOperations(t *testing.T) []openAPIOperation {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Security []json.RawMessage                     `json:"security"`
		Paths    map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var operations []openAPIOperation
	for template, item := range doc.Paths {
		for method, body := range item {
			switch method {
			case "get", "post", "put", "patch", "delete":
			default:
				continue
			}
			var op struct {
				Security  *[]json.RawMessage         `json:"security"`
				Responses map[string]json.RawMessage `json:"responses"`
			}
			if err := json.Unmarshal(body, &op); err != nil {
				t.Fatalf("%s %s: %v", method, template, err)
			}
			secured := len(doc.Security) > 0
			if op.Security != nil {
				secured = len(*op.Security) > 0
			}
			statuses := map[string]bool{}
			for status := range op.Responses {
				statuses[status] = true
			}
			operations = append(operations, openAPIOperation{method: strings.ToUpper(method), template: template, secured: secured, statuses: statuses})
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].template+operations[i].method < operations[j].template+operations[j].method
	})
	return operations
}

// templateMatches reports whether a concrete path fits a template: same
// number of segments, literal segments equal, a {name} segment matches any
// one non-empty segment. A shorter template never matches a longer path.
func templateMatches(template, path string) bool {
	want, got := strings.Split(template, "/"), strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if strings.HasPrefix(want[i], "{") && strings.HasSuffix(want[i], "}") {
			if got[i] == "" {
				return false
			}
			continue
		}
		if want[i] != got[i] {
			return false
		}
	}
	return true
}

func TestTemplateMatchingIsExactPerSegment(t *testing.T) {
	for _, test := range []struct {
		template, path string
		want           bool
	}{
		{"/a/investigations/{result_id}", "/a/investigations/res_1", true},
		{"/a/investigations/{result_id}", "/a/investigations/submissions/x", false},
		{"/a/investigations/{result_id}", "/a/investigations/", false},
		{"/a/investigations/feedback/{result_id}", "/a/investigations/submissions/x", false},
		{"/a/investigations/submissions/{id}", "/a/investigations/submissions/x/events", false},
		{"/a/investigations/submissions/{id}/events", "/a/investigations/submissions/x/events", true},
		{"/a/threads", "/a/threads/x", false},
	} {
		if got := templateMatches(test.template, test.path); got != test.want {
			t.Errorf("templateMatches(%q, %q) = %v, want %v", test.template, test.path, got, test.want)
		}
	}
}

// The tie is both ways, and loud on a parse that finds nothing.
func TestEveryProtectedRouteListsNoRepositoryInTheOpenAPI(t *testing.T) {
	operations := loadOpenAPIOperations(t)
	routes := protectedRoutes()
	if len(routes) == 0 || len(operations) == 0 {
		t.Fatalf("matched nothing: %d protected routes, %d OpenAPI operations", len(routes), len(operations))
	}
	listed := 0
	for _, op := range operations {
		if op.statuses["409"] {
			listed++
		}
	}
	if listed == 0 {
		t.Fatal("no OpenAPI operation lists a 409: the OpenAPI was not read as expected")
	}

	// Direction 1: every protected route is an OpenAPI operation that lists 409.
	matched := map[string]bool{}
	for _, route := range routes {
		path, _, _ := strings.Cut(route.path, "?")
		var hits []openAPIOperation
		for _, op := range operations {
			if op.method == route.method && templateMatches(op.template, path) {
				hits = append(hits, op)
			}
		}
		if len(hits) != 1 {
			t.Errorf("%s %s matches %d OpenAPI operations, want exactly 1", route.method, route.path, len(hits))
			continue
		}
		matched[hits[0].method+" "+hits[0].template] = true
		if !hits[0].secured {
			t.Errorf("%s %s: the OpenAPI operation is not secured", route.method, hits[0].template)
		}
		if !hits[0].statuses["409"] {
			t.Errorf("%s %s: the OpenAPI does not list 409 (no_repository)", route.method, hits[0].template)
		}
	}
	if len(matched) != len(routes) {
		t.Errorf("%d protected routes matched %d distinct OpenAPI operations", len(routes), len(matched))
	}

	// Direction 2: every secured operation is matched, or a named exception.
	seen := map[string]bool{}
	for _, op := range operations {
		key := op.method + " " + op.template
		seen[key] = true
		if !op.secured || matched[key] {
			continue
		}
		if reason, ok := noRepositoryExceptions[key]; !ok || reason == "" {
			t.Errorf("secured operation %s is in neither protectedRoutes() nor noRepositoryExceptions", key)
		}
	}
	for key := range noRepositoryExceptions {
		if !seen[key] {
			t.Errorf("exception %q names no OpenAPI operation", key)
		}
		if matched[key] {
			t.Errorf("exception %q is also in protectedRoutes(): drop the exception", key)
		}
	}
}

// The listed status and the executed status agree: with an empty effective
// scope every protected route answers 409 no_repository before its handler,
// and an exception route does not answer it. (The same empty-inventory case
// runs for every route and user class in
// TestEveryProtectedRouteRereadsAccessForEveryUserClass; this one pins it to
// the OpenAPI.)
func TestExecutedNoRepositoryStatusAgreesWithTheOpenAPI(t *testing.T) {
	operations := loadOpenAPIOperations(t)
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"*"})
	access := grantedAccess()
	access.Repositories = nil
	reader.set(access, nil)
	executed := 0
	for _, route := range protectedRoutes() {
		recorder, code := serveProtected(app, harness.tokenA, route, nil)
		got409 := recorder.Code == http.StatusConflict && code == contractsv1.ErrorCodeNoRepository
		listed := false
		for _, op := range operations {
			if op.method == route.method && templateMatches(op.template, route.path) {
				listed = op.statuses["409"]
			}
		}
		if !got409 || !listed {
			t.Errorf("%s %s: executed %d %s, OpenAPI lists 409 = %v; want both", route.method, route.path, recorder.Code, code, listed)
		}
		executed++
	}
	if executed == 0 {
		t.Fatal("no route was executed")
	}
	// The unserved packet read answers no 409 no_repository: that is why it is
	// an exception, not a protected route.
	recorder, code := serveProtected(app, harness.tokenA, protectedRoute{http.MethodGet, "/api/v1/agent-context/context-packets/pkt_access_test", ""}, nil)
	if code == contractsv1.ErrorCodeNoRepository || recorder.Code == http.StatusConflict {
		t.Errorf("GET context-packets/{id} answered %d %s: it is now served; move it from noRepositoryExceptions into protectedRoutes()", recorder.Code, code)
	}
	t.Logf("GET context-packets/{id} with an empty scope: %d %q", recorder.Code, code)
}

// outsideAccessCheck are routes registerRoutes gives the mux that are not
// behind requireLiveAccess (only protectedRuntimeChain installs it), each with
// its reason. TestOutsideAccessCheckRoutesNeverAnswerNoRepository drives each
// with an empty-scope user credential: none reads the access or answers
// 409 no_repository.
var outsideAccessCheck = map[string]string{
	"GET /healthz": "unauthenticated probe",
	"GET /readyz":  "unauthenticated probe",
	"POST /api/v1/oauth/device_authorization":     "device flow start, unauthenticated",
	"POST /api/v1/oauth/token":                    "device flow token, unauthenticated",
	"GET /.well-known/oauth-authorization-server": "OAuth metadata, unauthenticated",
	"GET /authorize":                              "OAuth authorize, code flow, no bearer",
	"POST /authorize/consent":                     "OAuth consent, code flow, no bearer",
	"POST /device_authorization":                  "OAuth device authorization, unauthenticated",
	"POST /register":                              "OAuth client registration, unauthenticated",
	"POST /token":                                 "OAuth token, unauthenticated",
}

// unservedInTheMux are exception entries that are OpenAPI-only: no handler is
// registered for them.
var unservedInTheMux = map[string]bool{
	"GET /api/v1/agent-context/context-packets/{context_packet_id}": true,
}

// Every pattern registerRoutes really gives the mux is covered: a protected
// route, a named no-409 exception, or a named route outside the access check.
// A route added to the mux but to no list fails here.
func TestEveryRegisteredRouteIsClassifiedForNoRepository(t *testing.T) {
	patterns := registeredPatterns(t, true)
	if len(patterns) == 0 {
		t.Fatal("no registered pattern was recorded")
	}
	covered := map[string]bool{}
	for _, pattern := range patterns {
		method, template, ok := strings.Cut(pattern, " ")
		if !ok {
			t.Fatalf("registered pattern %q has no method", pattern)
		}
		inProtected := false
		for _, route := range protectedRoutes() {
			path, _, _ := strings.Cut(route.path, "?")
			if route.method == method && templateMatches(template, path) {
				inProtected = true
			}
		}
		_, inException := noRepositoryExceptions[pattern]
		_, inOutside := outsideAccessCheck[pattern]
		covered[pattern] = true
		switch {
		case inProtected && inOutside:
			t.Errorf("%s is in protectedRoutes() and outsideAccessCheck", pattern)
		case inProtected && inException:
			t.Errorf("%s is in protectedRoutes() and noRepositoryExceptions", pattern)
		case !inProtected && !inException && !inOutside:
			t.Errorf("registered route %s is in none of protectedRoutes(), noRepositoryExceptions, outsideAccessCheck", pattern)
		}
	}
	for _, list := range []map[string]string{noRepositoryExceptions, outsideAccessCheck} {
		for key, reason := range list {
			if reason == "" {
				t.Errorf("%s has no reason", key)
			}
			if !covered[key] && !unservedInTheMux[key] {
				t.Errorf("list entry %q is not a registered route", key)
			}
			if covered[key] && unservedInTheMux[key] {
				t.Errorf("list entry %q is registered: drop it from unservedInTheMux", key)
			}
		}
	}
	// Every protected route must map to a registered pattern too.
	for _, route := range protectedRoutes() {
		path, _, _ := strings.Cut(route.path, "?")
		found := false
		for _, pattern := range patterns {
			method, template, _ := strings.Cut(pattern, " ")
			if method == route.method && templateMatches(template, path) {
				found = true
			}
		}
		if !found {
			t.Errorf("protectedRoutes() entry %s %s matches no registered route", route.method, route.path)
		}
	}
}

func TestOutsideAccessCheckRoutesNeverAnswerNoRepository(t *testing.T) {
	// The rig has the OAuth routes on, so every route in the list is served
	// (a 404 here would be a route that is not there, not a proof).
	app := newPublicRouteRig(t, true).app
	token := auth.TokenPrefix + strings.Repeat("A", 43)
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"*"})
	access := grantedAccess()
	access.Repositories = nil
	reader.set(access, nil)
	for pattern := range outsideAccessCheck {
		method, path, _ := strings.Cut(pattern, " ")
		body := ""
		if method == http.MethodPost {
			body = "{}"
		}
		recorder, code := serveProtected(app, token, protectedRoute{method, path, body}, nil)
		if calls := reader.reset(); len(calls) != 0 || code == contractsv1.ErrorCodeNoRepository || recorder.Code == http.StatusConflict {
			t.Errorf("%s: %d %q after %d access reads; want none and no 409 no_repository", pattern, recorder.Code, code, len(calls))
		}
		if recorder.Code == http.StatusNotFound && code == "" {
			t.Errorf("%s answered a bare 404: the route is not served in this rig, so nothing was proved", pattern)
		}
		t.Logf("%s -> %d %q", pattern, recorder.Code, code)
	}
}
