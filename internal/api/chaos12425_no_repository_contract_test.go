package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// CHAOS-12425: every route the access read guards answers 409 no_repository
// (TestEveryProtectedRouteRereadsAccessForEveryUserClass drives that, per route
// and user class), so every one of them must list it. Web's transport maps the
// statuses the contract names for a route.

// outsideTheAccessRead are secured operations that never reach
// requireLiveAccess: the credential's own lifecycle, and device approval.
var outsideTheAccessRead = []string{
	"post /api/v1/oauth/device_approval",
	"post /api/v1/auth/credentials/self/rotate",
	"post /api/v1/auth/credentials/self/revoke",
	"post /api/v1/auth/credentials/self/ack",
}

// documentedWithoutAHandler is published but not served: no handler is
// registered, so it cannot answer the refusal and must not list it.
// TestTheUnservedContextPacketRouteAnswersNothing fails when that changes.
const documentedWithoutAHandler = "get /api/v1/agent-context/context-packets/{context_packet_id}"

type openAPIDocument struct {
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Responses map[string]struct {
			Description string `json:"description"`
		} `json:"responses"`
	} `json:"components"`
}

type openAPIOperation struct {
	Security  []map[string]any `json:"security"`
	Responses map[string]struct {
		Ref string `json:"$ref"`
	} `json:"responses"`
}

func loadOpenAPIDocument(t *testing.T) openAPIDocument {
	t.Helper()
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document openAPIDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func (d openAPIDocument) operation(t *testing.T, template, method string) (openAPIOperation, bool) {
	t.Helper()
	raw, ok := d.Paths[template][method]
	if !ok {
		return openAPIOperation{}, false
	}
	var operation openAPIOperation
	if err := json.Unmarshal(raw, &operation); err != nil {
		t.Fatalf("%s %s: %v", method, template, err)
	}
	return operation, true
}

// templateOf finds the one published path a concrete route serves.
func (d openAPIDocument) templateOf(t *testing.T, path string) string {
	t.Helper()
	var found []string
	for template := range d.Paths {
		shape := "^" + regexp.MustCompile(`\\\{[^/]+\\\}`).ReplaceAllString(regexp.QuoteMeta(template), `[^/]+`) + "$"
		if regexp.MustCompile(shape).MatchString(path) {
			found = append(found, template)
		}
	}
	if len(found) != 1 {
		t.Fatalf("route %s matches %d published paths: %v", path, len(found), found)
	}
	return found[0]
}

func TestEveryProtectedRouteListsTheNoRepositoryRefusal(t *testing.T) {
	document := loadOpenAPIDocument(t)
	routes := protectedRoutes()
	if len(routes) == 0 {
		t.Fatal("no protected route was read")
	}
	for _, route := range routes {
		template := document.templateOf(t, route.path)
		method := strings.ToLower(route.method)
		operation, ok := document.operation(t, template, method)
		if !ok {
			t.Errorf("%s %s is not published", method, template)
			continue
		}
		response, ok := operation.Responses["409"]
		if !ok {
			t.Errorf("%s %s lists no 409", method, template)
			continue
		}
		name := strings.TrimPrefix(response.Ref, "#/components/responses/")
		component, ok := document.Components.Responses[name]
		if !ok {
			t.Errorf("%s %s: 409 is %q, not a shared response", method, template, response.Ref)
			continue
		}
		if !strings.Contains(component.Description, "no_repository") {
			t.Errorf("%s %s: the 409 response %s does not name no_repository", method, template, name)
		}
	}
}

// The other direction: a secured operation that a new protected route adds to
// the contract cannot be left out of protectedRoutes, which is what proves it.
func TestEverySecuredOperationIsAProtectedRoute(t *testing.T) {
	document := loadOpenAPIDocument(t)
	served := map[string]bool{}
	for _, route := range protectedRoutes() {
		served[strings.ToLower(route.method)+" "+document.templateOf(t, route.path)] = true
	}
	checked := 0
	for template, methods := range document.Paths {
		for method := range methods {
			key := method + " " + template
			operation, ok := document.operation(t, template, method)
			if !ok || len(operation.Security) == 0 {
				continue
			}
			checked++
			if key == documentedWithoutAHandler || slices.Contains(outsideTheAccessRead, key) {
				if served[key] {
					t.Errorf("%s is exempt here but is in protectedRoutes", key)
				}
				continue
			}
			if !served[key] {
				t.Errorf("%s is secured but not in protectedRoutes, so nothing proves its 409", key)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no secured operation was read")
	}
}

// The contract publishes this route, but no handler serves it: it answers no
// refusal, so it lists none. Adding a handler fails here, and the route then
// joins protectedRoutes and lists the 409.
func TestTheUnservedContextPacketRouteAnswersNothing(t *testing.T) {
	harness, _ := newMemorySubmissionHarness(t, 1)
	app := harness.apps[0]
	reader := &fakePrincipalAccess{}
	app.runtime.PrincipalAccess = reader
	authenticateAsCredential(t, app, storage.CredentialClientClassOAuthUser, []string{"acme/*"})
	access := grantedAccess()
	access.Repositories = []string{"other-org/repo-a"}
	reader.set(access, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/context-packets/cp_access_test", nil)
	request.Header.Set("Authorization", "Bearer "+harness.tokenA)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound || len(reader.reset()) != 0 {
		t.Fatalf("GET context-packets/{id}: %d, want 404 with no access read: a handler now serves it", recorder.Code)
	}
}
