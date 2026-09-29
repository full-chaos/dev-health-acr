package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7126: find_subjects owned_by and handle through the REAL route, the
// REAL lookup and subject/edge gates, on the chaos7074 fake graph.

type chaos7126Graph struct {
	chaos7074Graph
	grantRepos int
}

// ListSubjectsByKind lists the one repository of the route graph (the grant
// listing a restricted handle lookup anchors its census on).
func (g chaos7126Graph) ListSubjectsByKind(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, kind, after string, _ int) (directread.LookupPage, error) {
	if kind != "repository" || after != "" {
		return directread.LookupPage{}, nil
	}
	attributes := map[string]interface{}{"authorization_repositories": []string{hostedTestRepository}}
	page := directread.LookupPage{Nodes: []directread.LookupNode{{Kind: "repository", CanonicalID: "repository:a", Label: hostedTestRepository, Attributes: attributes}}}
	for i := 0; i < g.grantRepos; i++ {
		page.Nodes = append(page.Nodes, directread.LookupNode{Kind: "repository", CanonicalID: fmt.Sprintf("repository:b%03d", i), Label: hostedTestRepository, Attributes: attributes})
	}
	return page, nil
}
func (chaos7126Graph) FindSubjectsByExactName(context.Context, storage.Principal, contextfabric.ResolvedGraphBinding, string, []string) (directread.LookupPage, error) {
	return directread.LookupPage{}, nil
}
func (chaos7126Graph) ReadSubjectNodes(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]directread.LookupNode, error) {
	out := make([]directread.LookupNode, 0, len(subjects))
	for _, s := range subjects {
		out = append(out, directread.LookupNode{Kind: string(s.Kind), CanonicalID: s.CanonicalID, Label: "label of " + s.CanonicalID})
	}
	return out, nil
}

func chaos7126Harness(t *testing.T, extraGrantRepos ...int) *chaos7071Harness {
	graph := chaos7126Graph{chaos7074Graph: chaos7074RouteGraph()}
	if len(extraGrantRepos) > 0 {
		graph.grantRepos = extraGrantRepos[0]
	}
	census := func(context.Context, string, graphrank.CensusKind, string, bool, contextfabric.SubjectKind, string, bool) (graphrank.CensusOutcome, error) {
		return graphrank.CensusOutcome{Count: 2, SatisfierCanonicalIDs: []string{"pull_request.v2:x:532", "pull_request.v2:gone"}}, nil
	}
	graph.absent["pull_request.v2:gone"] = true
	return newChaos7071Harness(t, 100, func(deps *RuntimeDependencies) {
		gate := directread.NewSubjectGate(graph, nil)
		deps.DirectReadGate = gate
		deps.DataSubjects = directread.NewSubjectLookup(graph, gate, nil).WithOwnershipAndHandles(graph, census, graph)
	})
}

func (h *chaos7071Harness) postSubjects(token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, ContextFabricDataSubjectsPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

func TestChaos7126FindSubjectsRouteServesOwnedByAndHandle(t *testing.T) {
	h := chaos7126Harness(t)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	schema := dataSchemaFor(t, "mcp_find_subjects_response.v1.schema.json")
	for _, tc := range []struct {
		name, body, mode string
		status           directread.FindStatus
		ids              string
	}{
		// The route graph's team:t owns repository:a (edge rel-1); the
		// other edges point at other teams.
		{"owned_by", `{"owned_by":"team:t"}`, "owned_by", directread.FindComplete, "repository:a"},
		{"handle, one of two readable", `{"handle":"PR 532"}`, "handle", directread.FindComplete, "pull_request.v2:x:532"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := h.postSubjects(token, tc.body)
			if response.Code != http.StatusOK {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			assertValidAgainstSchema(t, schema, tc.name, response.Body.Bytes())
			var got struct {
				directread.FindResponse
				Request struct {
					Mode string `json:"mode"`
				} `json:"request"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0)
			for _, s := range got.Subjects {
				ids = append(ids, s.CanonicalID)
			}
			if got.Request.Mode != tc.mode || got.Status != tc.status || strings.Join(ids, ",") != tc.ids || got.Population.TotalKnown != len(ids) {
				t.Fatalf("got %s", response.Body.String())
			}
			if strings.Contains(response.Body.String(), "gone") {
				t.Fatalf("unreadable handle match leaked: %s", response.Body.String())
			}
		})
	}
	for _, body := range []string{`{"owned_by":"team:t","handle":"PR 1"}`, `{"handle":"PR 1","kind":"pull_request"}`, `{"handle":"payments"}`, `{"owned_by":"team:t","kinds":["work_item"]}`} {
		response := h.postSubjects(token, body)
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	}
}

// r1 ruling: past the per-repository census bound, handle mode is a typed
// refusal (invalid_request, reason scope_required) with no count.
func TestChaos7126_R1_HandleScopeRequiredRefusal(t *testing.T) {
	h := chaos7126Harness(t, directread.MaxHandleGrantRepositories)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	response := h.postSubjects(token, `{"handle":"PR 532"}`)
	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	if reason := chaos7074Reason(t, response); reason != "scope_required" {
		t.Fatalf("reason %q", reason)
	}
	if strings.Contains(response.Body.String(), "51") || strings.Contains(response.Body.String(), "50") {
		t.Fatalf("refusal carries a count: %s", response.Body.String())
	}
}
