package api

import (
	"bytes"
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

// CHAOS-7073: the read_facts route. These tests drive the REAL App handler
// (authentication, scope, entitlement) over a REAL directread.FactsReader
// built on the production registry with a stub provider and a fake graph.

type admitAllGraph struct {
	absent  map[string]bool
	bindErr error
}

func (g admitAllGraph) ResolveInvestigationBinding(context.Context, storage.Principal) (contextfabric.ResolvedGraphBinding, error) {
	return contextfabric.ResolvedGraphBinding{}, g.bindErr
}

func (g admitAllGraph) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for i, subject := range subjects {
		out[i] = contextfabric.StoredSubjectAdmitted
		if g.absent[subject.CanonicalID] {
			out[i] = contextfabric.StoredSubjectAbsent
		}
	}
	return out, nil
}

func (g admitAllGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	return make([][]string, len(subjects)), nil
}

type chaos7073Provider struct{ fail bool }

func (p chaos7073Provider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactHealth, Name: "health_route_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository},
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "repo_count", Type: contextfabric.FactFieldInteger}},
	}
}

func (p chaos7073Provider) ReadFacts(_ context.Context, _ storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if p.fail {
		return contextfabric.FactProviderResult{}, context.DeadlineExceeded
	}
	count := int64(7)
	facts := []contextfabric.CanonicalFact{}
	for _, subject := range query.Subjects {
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactHealth, Subject: subject,
			Fields:      map[string]contextfabric.FactValue{"repo_count": {Integer: &count}},
			SourceState: contextfabric.SourceAvailable,
		})
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable}, nil
}

func newChaos7073Reader(t *testing.T, graph directread.GraphAuthority, provider contextfabric.FactProvider) *directread.FactsReader {
	t.Helper()
	registry, err := contextfabric.NewFactCapabilityRegistry([]contextfabric.FactProvider{provider}, contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return directread.NewFactsReader(directread.NewSubjectGate(graph, nil), directread.NewFactReader(registry.WithoutScopeExpansion()), nil)
}

func (h *chaos7071Harness) postFacts(token, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, ContextFabricDataFactsPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(response, request)
	return response
}

const chaos7073ValidBody = `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}]}`

func TestChaos7073FactsRouteAnswers(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token

	t.Run("no reader composed fails closed as 503 unavailable", func(t *testing.T) {
		response := h.postFacts(token, chaos7073ValidBody)
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
		var envelope contractsv1.ErrorEnvelope
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if !envelope.Error.Retryable {
			t.Fatalf("unavailable must be retryable: %#v", envelope.Error)
		}
	})

	h.app.runtime.DirectFacts = newChaos7073Reader(t, admitAllGraph{absent: map[string]bool{"repository:gone": true}}, chaos7073Provider{})

	t.Run("happy path serves the facts document", func(t *testing.T) {
		response := h.postFacts(token, chaos7073ValidBody)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var document directread.FactsResponse
		decoder := json.NewDecoder(bytes.NewReader(response.Body.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&document); err != nil {
			t.Fatal(err)
		}
		if document.Tool != "read_facts" || document.Status != directread.StatusComplete || len(document.Facts) != 1 {
			t.Fatalf("unexpected document: %s", response.Body.String())
		}
		if got := document.Facts[0].Fields["repo_count"]; got != "7" {
			t.Fatalf("integers are strings on the wire, got %#v", got)
		}
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control %q", response.Header().Get("Cache-Control"))
		}
	})

	t.Run("an absent subject is refused in the document, not as an error", func(t *testing.T) {
		response := h.postFacts(token, `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:gone"}]}`)
		if response.Code != http.StatusOK {
			t.Fatalf("status %d: %s", response.Code, response.Body.String())
		}
		var document directread.FactsResponse
		if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
			t.Fatal(err)
		}
		if document.Status != directread.StatusDenied || len(document.Request.SubjectsRefused) != 1 || len(document.Facts) != 0 {
			t.Fatalf("unexpected document: %s", response.Body.String())
		}
	})

	invalid := map[string]string{
		"unknown field":     `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}],"nope":1}`,
		"not json":          `{`,
		"trailing json":     chaos7073ValidBody + `{}`,
		"no kinds":          `{"kinds":[],"subjects":[{"kind":"repository","canonical_id":"repository:a"}]}`,
		"unknown kind":      `{"kinds":["no_such_kind"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}]}`,
		"max_bytes too low": `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}],"max_bytes":10}`,
		"bad window":        `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}],"window":{"mode":"sometime"}}`,
		"future as_of":      `{"kinds":["health"],"subjects":[{"kind":"repository","canonical_id":"repository:a"}],"window":{"mode":"as_of","as_of":"2999-01-01T00:00:00Z"}}`,
	}
	for name, body := range invalid {
		t.Run("400 invalid_request: "+name, func(t *testing.T) {
			response := h.postFacts(token, body)
			assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
			var envelope contractsv1.ErrorEnvelope
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Retryable {
				t.Fatalf("an invalid request is not retryable: %#v", envelope.Error)
			}
		})
	}

	t.Run("a provider failure answers 200 with coverage, never a leaked error", func(t *testing.T) {
		h.app.runtime.DirectFacts = newChaos7073Reader(t, admitAllGraph{}, chaos7073Provider{fail: true})
		response := h.postFacts(token, chaos7073ValidBody)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"outcome":"unavailable"`) {
			t.Fatalf("a provider failure is coverage, not an error: %d %s", response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "deadline") {
			t.Fatalf("raw error text leaked: %s", response.Body.String())
		}
	})

	t.Run("a graph failure answers 503 without the raw error", func(t *testing.T) {
		h.app.runtime.DirectFacts = newChaos7073Reader(t, admitAllGraph{bindErr: errors.New("secret-graph-host:6379 refused")}, chaos7073Provider{})
		response := h.postFacts(token, chaos7073ValidBody)
		assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
		if strings.Contains(response.Body.String(), "secret-graph-host") {
			t.Fatalf("raw dependency error leaked: %s", response.Body.String())
		}
	})
}

func TestChaos7073FactsRouteRequiresContextReadScope(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	h.app.runtime.DirectFacts = newChaos7073Reader(t, admitAllGraph{}, chaos7073Provider{})
	evidenceOnly := h.issue(t, []string{auth.ScopeEvidenceRead}, nil).Token
	assertErrorResponse(t, h.postFacts(evidenceOnly, chaos7073ValidBody), http.StatusForbidden, "insufficient_scope")
	assertErrorResponse(t, h.postFacts("", chaos7073ValidBody), http.StatusUnauthorized, "invalid_token")
}

// The capability is advertised exactly when the reader is composed and the
// caller holds context:read.
func TestChaos7073CapabilitiesAdvertiseReadFactsOnlyWhenComposed(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil).Token
	tools := func() string {
		response := h.call(http.MethodGet, "/api/v1/agent-context/capabilities", token)
		var value contractsv1.Capabilities
		if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		if err := value.Validate(); err != nil {
			t.Fatalf("capabilities invalid: %v", err)
		}
		return strings.Join(value.EnabledTools, ",")
	}
	if got := tools(); got != "context_for_task,source_evidence" {
		t.Fatalf("tools without a reader: %s", got)
	}
	h.app.runtime.DirectFacts = newChaos7073Reader(t, admitAllGraph{}, chaos7073Provider{})
	if got := tools(); got != "context_for_task,read_facts,source_evidence" && got != "context_for_task,source_evidence,read_facts" {
		t.Fatalf("tools with a reader: %s", got)
	}
}
