package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7074: the read_relationships route. These tests drive the REAL App
// handler (authentication, scope, entitlement, data-store readiness) over a
// REAL directread.RelationshipsReader and subject gate, on a fake graph.

type chaos7074Graph struct {
	absent  map[string]bool
	pageErr error
	edges   []directread.EdgeCandidate
}

func (g chaos7074Graph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, nil
}

func (g chaos7074Graph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i, subject := range subjects {
		out[i] = contextfabric.StoredSubjectAdmitted
		if g.absent[subject.CanonicalID] {
			out[i] = contextfabric.StoredSubjectAbsent
		}
	}
	return out, nil
}

func (g chaos7074Graph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for i := range out {
		out[i] = []string{hostedTestRepository}
	}
	return out, nil
}

func (g chaos7074Graph) DirectEdgePage(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, query directread.EdgePageQuery) (directread.EdgePage, error) {
	if g.pageErr != nil {
		return directread.EdgePage{}, g.pageErr
	}
	page := directread.EdgePage{}
	for _, e := range g.edges {
		if query.After != nil && !query.After.Less(e.Key) {
			continue
		}
		if len(page.Edges) == query.Limit {
			page.More = true
			break
		}
		page.Edges = append(page.Edges, e)
	}
	return page, nil
}

func chaos7074RouteGraph() chaos7074Graph {
	repo := contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectRepository, CanonicalID: "repository:a"}
	edge := func(id, team string) directread.EdgeCandidate {
		return directread.EdgeCandidate{
			Key: directread.EdgeKey{RelationshipID: id}, RelationType: "OWNED_BY_TEAM",
			Attributes: map[string]interface{}{"authorization_repositories": []string{hostedTestRepository}, "source_version": "v12", "derivation": "rule_inferred"},
			From:       directread.EdgeEnd{Subject: repo},
			To:         directread.EdgeEnd{Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectTeam, CanonicalID: team}},
		}
	}
	return chaos7074Graph{absent: map[string]bool{"team:gone": true}, edges: []directread.EdgeCandidate{edge("rel-1", "team:t"), edge("rel-2", "team:gone"), edge("rel-3", "team:u")}}
}

func setChaos7074Reader(h *chaos7071Harness, graph chaos7074Graph) {
	gate := directread.NewSubjectGate(graph, nil)
	h.app.runtime.DirectReadGate = gate
	h.app.runtime.DirectRelationships = directread.NewRelationshipsReader(gate, graph, nil)
}

func (h *chaos7071Harness) postRelationships(token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, ContextFabricDataRelationshipsPath, strings.NewReader(body))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
	}
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

const chaos7074ValidBody = `{"subject":{"kind":"repository","canonical_id":"repository:a"},"limit":2}`

func chaos7074Reason(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	reason, _ := envelope.Error.Details["reason"].(string)
	return reason
}

func TestChaos7074RelationshipsRouteAnswers(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token

	t.Run("no reader composed fails closed as 503 unavailable", func(t *testing.T) {
		response := h.postRelationships(token, chaos7074ValidBody)
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
	})

	setChaos7074Reader(h, chaos7074RouteGraph())

	var next string
	t.Run("a page, the withheld edge counted, a next cursor", func(t *testing.T) {
		response := h.postRelationships(token, chaos7074ValidBody)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var page directread.RelationshipsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Status != directread.RelationshipsPartial || len(page.Edges) != 1 || page.Edges[0].RelationshipID != "rel-1" ||
			page.Withheld.EdgesNotVisible != 1 || page.Page.NextCursor == "" || page.Meaning != directread.RelationshipsMeaning {
			t.Fatalf("page = %s", response.Body.String())
		}
		if strings.Contains(response.Body.String(), "team:gone") || strings.Contains(response.Body.String(), "rel-2") {
			t.Fatalf("withheld edge leaked: %s", response.Body.String())
		}
		next = page.Page.NextCursor
	})
	t.Run("the cursor continues the walk to its end", func(t *testing.T) {
		response := h.postRelationships(token, `{"subject":{"kind":"repository","canonical_id":"repository:a"},"limit":2,"cursor":"`+next+`"}`)
		var page directread.RelationshipsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
		if page.Status != directread.RelationshipsComplete || len(page.Edges) != 1 || page.Edges[0].RelationshipID != "rel-3" || page.Page.NextCursor != "" {
			t.Fatalf("page 2 = %s", response.Body.String())
		}
	})
	t.Run("a cursor for another walk is refused 400 invalid_cursor", func(t *testing.T) {
		response := h.postRelationships(token, `{"subject":{"kind":"repository","canonical_id":"repository:a"},"direction":"in","cursor":"`+next+`"}`)
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		if reason := chaos7074Reason(t, response); reason != directread.RelationshipsRefusalInvalidCursor {
			t.Fatalf("reason %q", reason)
		}
	})
	t.Run("malformed and out-of-bounds requests are 400", func(t *testing.T) {
		for _, body := range []string{`{`, `{"subject":{"kind":"repository","canonical_id":"repository:a"},"depth":3}`, `{"subject":{"kind":"repository","canonical_id":"repository:a"},"unknown":1}`} {
			response := h.postRelationships(token, body)
			assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		}
	})
	t.Run("a root the caller cannot read answers denied_or_not_found", func(t *testing.T) {
		response := h.postRelationships(token, `{"subject":{"kind":"team","canonical_id":"team:gone"}}`)
		var page directread.RelationshipsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || response.Code != http.StatusOK {
			t.Fatalf("%d %s", response.Code, response.Body.String())
		}
		if page.Status != directread.RelationshipsDenied || page.Reason != directread.RelationshipsRefusalDeniedOrNotFound || len(page.Edges) != 0 {
			t.Fatalf("denied = %s", response.Body.String())
		}
	})
	t.Run("a graph failure is a retryable 503, no error text", func(t *testing.T) {
		graph := chaos7074RouteGraph()
		graph.pageErr = errors.New("falkordb: secret-host:6379 connection refused")
		setChaos7074Reader(h, graph)
		response := h.postRelationships(token, chaos7074ValidBody)
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
		if strings.Contains(response.Body.String(), "secret-host") {
			t.Fatalf("dependency error leaked: %s", response.Body.String())
		}
	})
}

func TestChaos7074RelationshipsRouteRequiresContextReadScope(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	setChaos7074Reader(h, chaos7074RouteGraph())
	dataOnly := h.issue(t, []string{auth.ScopeDataRead}, nil).Token
	assertErrorResponse(t, h.postRelationships(dataOnly, chaos7074ValidBody), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.postRelationships("", chaos7074ValidBody), http.StatusUnauthorized, "invalid_token")
	*h.entitled = false
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	response := h.postRelationships(token, chaos7074ValidBody)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unentitled caller: %d %s", response.Code, response.Body.String())
	}
}

// The capability is advertised exactly when the reader is composed.
func TestChaos7074CapabilitiesAdvertiseReadRelationshipsOnlyWhenComposed(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil).Token
	tools := func() []string {
		response := h.call(http.MethodGet, "/api/v1/agent-context/capabilities", token)
		var value contractsv1.Capabilities
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if err := value.Validate(); err != nil {
			t.Fatalf("capabilities invalid: %v", err)
		}
		return value.EnabledTools
	}
	has := func(list []string) bool {
		for _, tool := range list {
			if tool == "read_relationships" {
				return true
			}
		}
		return false
	}
	if has(tools()) {
		t.Fatal("read_relationships advertised without a reader")
	}
	setChaos7074Reader(h, chaos7074RouteGraph())
	if !has(tools()) {
		t.Fatal("read_relationships not advertised with a reader")
	}
}

// CHAOS-6745 placement: a ClickHouse outage answers the typed, retryable
// store_unavailable 503 before any read, and only after authentication.
func TestChaos7074RelationshipsRouteRequiresDataStoresReady(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7074Reader(h, chaos7074RouteGraph())
	h.app.dataStoreChecks = []ReadinessCheck{
		CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error { return errors.New("clickhouse: connection refused") }},
	}
	assertErrorResponse(t, h.postRelationships(token, chaos7074ValidBody), http.StatusServiceUnavailable, "store_unavailable")
	assertErrorResponse(t, h.postRelationships("", chaos7074ValidBody), http.StatusUnauthorized, "invalid_token")
}
