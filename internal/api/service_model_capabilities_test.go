package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestCapabilitiesListInvestigateQuestionOnlyWhereTheServiceModelServes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		open    bool
		grants  *serviceModelGrants
		configs ownModelConfigs
		listed  bool
	}{
		{name: "no entitlement, no own model", grants: &serviceModelGrants{}, listed: false},
		{name: "deployment open", open: true, grants: &serviceModelGrants{}, listed: true},
		{name: "feature and fallback grant", grants: &serviceModelGrants{feature: true, fallback: true}, listed: true},
		{name: "feature only", grants: &serviceModelGrants{feature: true}, listed: false},
		{name: "fallback grant only", grants: &serviceModelGrants{fallback: true}, listed: false},
		{name: "own model with the feature off", grants: &serviceModelGrants{fallback: true}, configs: ownModelConfigs{found: true}, listed: false},
		{name: "own model behind a closed gate", grants: &serviceModelGrants{}, configs: ownModelConfigs{err: entitlements.ErrLLMConfigGateClosed}, listed: true},
		{name: "own model", grants: &serviceModelGrants{feature: true}, configs: ownModelConfigs{found: true}, listed: true},
		{name: "entitlement unreadable", grants: &serviceModelGrants{err: errors.New("down")}, listed: true},
		{name: "own model unreadable", grants: &serviceModelGrants{}, configs: ownModelConfigs{err: errors.New("down")}, listed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := gatedClientRouteRig(t, tc.grants, tc.configs)
			rig.app.runtime.ServiceModelOpen = tc.open
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
			request.Header.Set("Authorization", "Bearer "+rig.token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			recorder := httptest.NewRecorder()
			rig.app.Handler().ServeHTTP(recorder, request)
			var caps contractsv1.Capabilities
			if err := json.Unmarshal(recorder.Body.Bytes(), &caps); err != nil {
				t.Fatalf("%v: %s", err, recorder.Body.String())
			}
			if got := slices.Contains(caps.EnabledTools, "investigate_question"); got != tc.listed {
				t.Fatalf("investigate_question listed = %v, want %v: %v", got, tc.listed, caps.EnabledTools)
			}
			for _, always := range []string{"investigate_with_interpretation", "investigation_result"} {
				if !slices.Contains(caps.EnabledTools, always) {
					t.Fatalf("%s must stay listed: %v", always, caps.EnabledTools)
				}
			}
		})
	}
}
