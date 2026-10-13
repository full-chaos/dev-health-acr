package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type deploymentsRouteProvider struct{}

func (deploymentsRouteProvider) Capability() contextfabric.FactCapability {
	return contextfabric.FactCapability{
		Kind: contextfabric.FactDeployments, Name: "deployments_route_test", Version: "test.v1",
		SupportedSubjectKinds: []contextfabric.SubjectKind{contractsv1.ContextFabricSubjectRepository},
		Dimension:             contextfabric.HealthDimensionCodeOwnershipRisk,
		SubjectRoles:          []contextfabric.FactRole{contextfabric.FactRoleSubject},
		Fields:                []contextfabric.FactFieldDeclaration{{Name: "n", Type: contextfabric.FactFieldInteger}},
	}
}

func (deploymentsRouteProvider) ReadFacts(_ context.Context, _ storage.Principal, _ contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	return contextfabric.FactProviderResult{State: contextfabric.SourceAvailable}, nil
}

func TestFactsRouteNamesUnsupportedWindowOfCurrentOnlyPolicy(t *testing.T) {
	h := newChaos7071Harness(t, 100)
	token := h.issue(t, []string{auth.ScopeContextRead}, nil).Token
	setChaos7073Reader(t, h, reachingGraph{}, deploymentsRouteProvider{})
	const subject = `"subjects":[{"kind":"project","canonical_id":"project:a"}]`
	cases := []struct {
		name, body, wantState string
		wantReason            string
	}{
		{"trailing window", `{"kinds":["deployments"],` + subject + `,"window":{"mode":"trailing","days":30}}`, "window_unsupported", "supports the current window only; ask without a window to read it"},
		{"current window", `{"kinds":["deployments"],` + subject + `}`, directread.KindStateScopeExpansionUnavailable, "no scope expander is wired on this server"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := h.postFacts(token, tc.body)
			if response.Code != http.StatusOK {
				t.Fatalf("status %d: %s", response.Code, response.Body.String())
			}
			var document directread.FactsResponse
			if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
				t.Fatal(err)
			}
			if len(document.Coverage) != 1 {
				t.Fatalf("coverage: %s", response.Body.String())
			}
			row := document.Coverage[0]
			if row.Outcome != directread.OutcomeUnavailable || row.KindState != tc.wantState || !strings.Contains(row.Reason, tc.wantReason) {
				t.Fatalf("row %+v, want kind_state %q reason containing %q", row, tc.wantState, tc.wantReason)
			}
		})
	}
}

type reachingGraph struct{ admitAllGraph }

func (reachingGraph) OwnershipReachedRepositories(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([][]string, error) {
	out := make([][]string, len(subjects))
	for i := range out {
		out[i] = []string{hostedTestRepository}
	}
	return out, nil
}
