package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-7148: the data_catalog route and the read_facts route agree. With no
// reader composed the catalog says facts are unavailable and read_facts is
// 503; once the reader is composed the catalog lists the kind read_facts
// serves and read_facts answers 200 for it.
func TestChaos7148CatalogAgreesWithReadFactsRoute(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, nil).Token
	facts := func() map[string]any {
		request := httptest.NewRequest(http.MethodGet, ContextFabricDataCatalogPath+"?sections=facts", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		response := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("catalog status %d: %s", response.Code, response.Body.String())
		}
		body := map[string]any{}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body["facts"].(map[string]any)
	}

	before := facts()
	if before["served"] != false {
		t.Fatalf("no reader composed but catalog says served: %v", before)
	}
	if h.postFacts(token, chaos7073ValidBody).Code != http.StatusServiceUnavailable {
		t.Fatal("read_facts must be 503 with no reader")
	}

	// A composed reader object with no fact source: read_facts answers 503,
	// so the catalog must not say served.
	h.app.runtime.DirectReadGate = directread.NewSubjectGate(admitAllGraph{}, nil)
	h.app.runtime.DirectFactReader = directread.NewFactReader(nil)
	if got := facts(); got["served"] != false || got["kinds"] != nil {
		t.Fatalf("reader without a source but catalog says served: %v", got)
	}
	if h.postFacts(token, chaos7073ValidBody).Code != http.StatusServiceUnavailable {
		t.Fatal("read_facts must be 503 for a reader without a source")
	}

	setChaos7073Reader(t, h, admitAllGraph{}, chaos7073Provider{})

	after := facts()
	kinds, _ := after["kinds"].([]any)
	if after["served"] != true || len(kinds) != 1 || kinds[0].(map[string]any)["kind"] != "health" {
		encoded, _ := json.Marshal(after)
		t.Fatalf("reader composed but catalog does not list its kinds: %s", encoded)
	}
	if h.postFacts(token, chaos7073ValidBody).Code != http.StatusOK {
		t.Fatal("read_facts must answer 200 for the kind the catalog lists")
	}
}
