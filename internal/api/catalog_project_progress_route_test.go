package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestDataCatalogRouteNamesActualCompletionAsProjectProgress(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issueFor(t, []string{auth.ScopeContextRead, auth.ScopeDataRead}, []string{"*"}, nil).Token
	registry, err := contextfabric.NewFactCapabilityRegistry(devhealthfacts.NewProviders(projectRollupRowsClient{}), contextfabric.FactRegistryOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h.app.runtime.DirectReadGate = directread.NewSubjectGate(admitAllGraph{}, nil)
	h.app.runtime.DirectFactReader = directread.NewFactReader(registry.WithoutScopeExpansion())

	request := httptest.NewRequest(http.MethodGet, ContextFabricDataCatalogPath+"?sections=facts", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	catalog := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(catalog, request)
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status %d: %s", catalog.Code, catalog.Body.String())
	}
	var document struct {
		Facts directread.CatalogFacts `json:"facts"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project progress", "share of counted work items completed", "not windowed"} {
		if !strings.Contains(document.Facts.Note, want) {
			t.Fatalf("served catalogue note lacks %q: %q", want, document.Facts.Note)
		}
	}
	found := false
	for _, entry := range document.Facts.Kinds {
		if entry.Kind == "actual_completion" {
			found = slices.Contains(entry.SubjectKinds, "project") && slices.Contains(entry.Fields, "completion_ratio")
		}
	}
	if !found {
		t.Fatalf("served catalogue lacks actual_completion over project with completion_ratio: %s", catalog.Body.String())
	}
}
