package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

// serviceModelGrants answers agent_context_runtime as granted and
// acr_service_model as configured, and records every entitlement asked for.
type serviceModelGrants struct {
	mu      sync.Mutex
	feature bool
	err     error
	asked   []string
}

func (g *serviceModelGrants) HasEntitlement(_ context.Context, _ string, entitlement string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, entitlement)
	switch entitlement {
	case agentContextRuntimeEntitlement:
		return true, nil
	case serviceModelEntitlement:
		return g.feature, g.err
	default:
		return false, errors.New("unexpected entitlement")
	}
}

func (g *serviceModelGrants) askedFor(entitlement string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	count := 0
	for _, asked := range g.asked {
		if asked == entitlement {
			count++
		}
	}
	return count
}

// ownModelConfigs is an organization model configuration source that holds a
// configuration, holds none, or cannot be read.
type ownModelConfigs struct {
	found bool
	err   error
}

func (c ownModelConfigs) ResolveOrgModelConfig(context.Context, string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	return contextfabric.ResolvedOrgModelConfig{}, c.found, c.err
}

func gatedClientRouteRig(t *testing.T, grants *serviceModelGrants, configs contextfabric.OrgModelConfigResolver) *clientRouteRig {
	t.Helper()
	rig := newClientRouteRig(t, singleSubjectFixture())
	rig.app.runtime.Entitlements = grants
	rig.app.runtime.OrgModelConfigs = configs
	return rig
}

type serviceModelRefusal struct {
	Error struct {
		Code      string         `json:"code"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

func decodeServiceModelRefusal(t *testing.T, recorder *httptest.ResponseRecorder, status int) serviceModelRefusal {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), status)
	}
	var body serviceModelRefusal
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func serviceModelDecisionBasis(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var bases []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry struct {
			Msg   string `json:"msg"`
			Basis string `json:"basis"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "context fabric service model decision" {
			bases = append(bases, entry.Basis)
		}
	}
	return bases
}

func assertNoModelCallAndNoResult(t *testing.T, rig *clientRouteRig) {
	t.Helper()
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if rig.nextID != 0 {
		t.Fatalf("results minted = %d, want 0", rig.nextID)
	}
}

func TestServiceModelTurnWithoutTheFeatureOrOwnModelIsRefusedBeforeAnyModelCall(t *testing.T) {
	rig := gatedClientRouteRig(t, &serviceModelGrants{}, ownModelConfigs{})
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
	assertNoModelCallAndNoResult(t, rig)
	body := decodeServiceModelRefusal(t, recorder, http.StatusForbidden)
	if body.Error.Code != "feature_not_enabled" || body.Error.Retryable || body.Error.Details["reason"] != "service_model_not_entitled" {
		t.Fatalf("error = %+v, want feature_not_enabled, not retryable, reason service_model_not_entitled", body.Error)
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "refused" {
		t.Fatalf("decision log bases = %v, want [refused]", got)
	}
	denied := 0
	for _, event := range rig.app.runtime.Audit.(*memory.AuditStore).Events() {
		if event.Action == "entitlement_denied" && event.ResourceID == "acr_service_model" && event.Status == "denied" {
			denied++
		}
	}
	if denied != 1 {
		t.Fatalf("acr_service_model denial audit events = %d, want 1", denied)
	}
}

func TestServiceInterpretationWithClientSynthesisIsRefusedWithoutTheFeatureOrOwnModel(t *testing.T) {
	rig := gatedClientRouteRig(t, &serviceModelGrants{}, nil)
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeClient, nil)
	assertNoModelCallAndNoResult(t, rig)
	if body := decodeServiceModelRefusal(t, recorder, http.StatusForbidden); body.Error.Details["reason"] != "service_model_not_entitled" {
		t.Fatalf("error = %+v, want reason service_model_not_entitled", body.Error)
	}
}

func TestServiceModelTurnIsServedOnTheFeatureOwnModelOrOpenDeployment(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grants  *serviceModelGrants
		configs contextfabric.OrgModelConfigResolver
		open    bool
		basis   string
		asked   int
	}{
		{name: "feature", grants: &serviceModelGrants{feature: true}, configs: ownModelConfigs{}, basis: "entitlement", asked: 1},
		{name: "own model", grants: &serviceModelGrants{}, configs: ownModelConfigs{found: true}, basis: "own_model", asked: 1},
		{name: "open deployment", grants: &serviceModelGrants{}, configs: ownModelConfigs{}, open: true, basis: "deployment_open", asked: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := gatedClientRouteRig(t, tc.grants, tc.configs)
			rig.app.runtime.ServiceModelOpen = tc.open
			envelope := decodeEnvelope(t, rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil))
			if interprets, synthesizes := rig.model.counts(); interprets != 1 || synthesizes != 1 {
				t.Fatalf("model calls = interpret %d synthesize %d, want 1 and 1", interprets, synthesizes)
			}
			if envelope.ResultID == "" {
				t.Fatal("served answer carries no result_id")
			}
			if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != tc.basis {
				t.Fatalf("decision log bases = %v, want [%s]", got, tc.basis)
			}
			if got := tc.grants.askedFor(serviceModelEntitlement); got != tc.asked {
				t.Fatalf("acr_service_model asked %d times, want %d", got, tc.asked)
			}
		})
	}
}

func TestServiceModelDecisionThatCannotBeMadeFailsClosedBeforeAnyModelCall(t *testing.T) {
	for _, tc := range []struct {
		name    string
		grants  *serviceModelGrants
		configs contextfabric.OrgModelConfigResolver
		code    string
	}{
		{name: "entitlement service down and no own model", grants: &serviceModelGrants{err: errors.New("entitlement down")}, configs: ownModelConfigs{}, code: "upstream_unavailable"},
		{name: "model configuration store down", grants: &serviceModelGrants{}, configs: ownModelConfigs{err: errors.New("store down")}, code: "store_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := gatedClientRouteRig(t, tc.grants, tc.configs)
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			assertNoModelCallAndNoResult(t, rig)
			if body := decodeServiceModelRefusal(t, recorder, http.StatusServiceUnavailable); body.Error.Code != tc.code || !body.Error.Retryable {
				t.Fatalf("error = %+v, want retryable %s", body.Error, tc.code)
			}
		})
	}
}

func TestServiceModelDecisionForAnUnknownOrganizationIs401BeforeAnyModelCall(t *testing.T) {
	rig := gatedClientRouteRig(t, &serviceModelGrants{err: entitlements.ErrOrganizationUnknown}, ownModelConfigs{found: true})
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
	assertNoModelCallAndNoResult(t, rig)
	if body := decodeServiceModelRefusal(t, recorder, http.StatusUnauthorized); body.Error.Code != "invalid_token" || body.Error.Retryable {
		t.Fatalf("error = %+v, want non-retryable invalid_token", body.Error)
	}
}

// The caller's own interpretation and own synthesis, through both calls of the
// write-back and the read by id, stay served with no model call and without
// reading the organization model configuration.
func TestClientModelPathIsServedWithoutTheFeatureOrOwnModel(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	rig.app.runtime.Entitlements = &serviceModelGrants{}
	rig.app.runtime.OrgModelConfigs = ownModelConfigs{err: errors.New("must not be read on the client path")}
	first := rig.firstCall(t)
	draft := writeBackDraft(rig.project, writeBackMarker)
	served := decodeEnvelope(t, rig.writeBack(t, first.SynthesisInput, writeBackOutputJSON(t, draft), nil))
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if served.Versions.SynthesisSource != contractsv1.ContextFabricSynthesisSourceClient {
		t.Fatalf("synthesis source = %q, want client", served.Versions.SynthesisSource)
	}
	if read := rig.get(served.ResultID); read.Code != http.StatusOK {
		t.Fatalf("read by id: status = %d body=%s", read.Code, read.Body.String())
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 2 || got[0] != "client_model" || got[1] != "client_model" {
		t.Fatalf("decision log bases = %v, want [client_model client_model]", got)
	}
}

// Only the investigation route decides the service model and the capabilities
// route advertises it: every other route of the hosted API never asks for
// acr_service_model and is never refused for it. Capabilities is never refused
// for it either; see TestCapabilitiesNeverRefuseForTheServiceModel.
func TestNoRouteOtherThanTheInvestigationAsksForTheServiceModel(t *testing.T) {
	grants := &serviceModelGrants{}
	rig := gatedClientRouteRig(t, grants, nil)
	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/agent-context/context-packets", `{}`},
		{http.MethodGet, "/api/v1/agent-context/evidence/ev_missing", ""},
		{http.MethodGet, ContextFabricDataCatalogPath, ""},
		{http.MethodPost, ContextFabricDataSubjectsPath, `{}`},
		{http.MethodPost, ContextFabricDataFactsPath, `{}`},
		{http.MethodPost, ContextFabricDataRelationshipsPath, `{}`},
		{http.MethodPost, ContextFabricDataOperationsPath, `{}`},
		{http.MethodPost, ContextFabricDataGraphQLPath, `{}`},
		{http.MethodGet, ContextFabricInvestigationsPath + "/result_missing", ""},
	} {
		request := httptest.NewRequest(call.method, call.path, strings.NewReader(call.body))
		request.Header.Set("Authorization", "Bearer "+rig.token)
		request.Header.Set("X-ACR-Client-Version", "1.0.0")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		rig.app.Handler().ServeHTTP(recorder, request)
		if strings.Contains(recorder.Body.String(), "service_model_not_entitled") {
			t.Fatalf("%s %s refused for the service model: %s", call.method, call.path, recorder.Body.String())
		}
	}
	if got := grants.askedFor(serviceModelEntitlement); got != 0 {
		t.Fatalf("acr_service_model asked %d times by routes that run no model, want 0", got)
	}
	if got := grants.askedFor(agentContextRuntimeEntitlement); got == 0 {
		t.Fatal("no route reached the entitlement check; the probe proves nothing")
	}
	assertNoModelCallAndNoResult(t, rig)
}

func TestCapabilitiesNeverRefuseForTheServiceModel(t *testing.T) {
	rig := gatedClientRouteRig(t, &serviceModelGrants{}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+rig.token)
	request.Header.Set("X-ACR-Client-Version", "1.0.0")
	recorder := httptest.NewRecorder()
	rig.app.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "service_model_not_entitled") {
		t.Fatalf("capabilities = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestSuppliedInterpretationWithServiceSynthesisIsRefusedWithoutTheFeatureOrOwnModel(t *testing.T) {
	rig := newWriteBackRouteRig(t)
	rig.app.runtime.Entitlements = &serviceModelGrants{}
	recorder := rig.post(t, func(body *contractsv1.ContextFabricInvestigationRequest) {
		body.SynthesisMode = contractsv1.ContextFabricSynthesisModeServer
	})
	if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
		t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
	}
	if rig.store.count() != 0 {
		t.Fatalf("saved results = %d, want 0", rig.store.count())
	}
	if body := decodeServiceModelRefusal(t, recorder, http.StatusForbidden); body.Error.Details["reason"] != "service_model_not_entitled" {
		t.Fatalf("error = %+v, want reason service_model_not_entitled", body.Error)
	}
}

// The engine runs with the deployment model withheld exactly when the caller
// may not use it, whatever else serves the turn.
func TestEngineRunsWithThePlatformModelWithheldOnlyWhenTheCallerMayNotUseIt(t *testing.T) {
	for _, tc := range []struct {
		name     string
		grants   *serviceModelGrants
		configs  contextfabric.OrgModelConfigResolver
		open     bool
		supplied bool
		withheld bool
	}{
		{name: "feature", grants: &serviceModelGrants{feature: true}, withheld: false},
		{name: "open deployment", grants: &serviceModelGrants{}, open: true, withheld: false},
		{name: "own model", grants: &serviceModelGrants{}, configs: ownModelConfigs{found: true}, withheld: true},
		{name: "own model whose configuration is unusable", grants: &serviceModelGrants{}, configs: ownModelConfigs{err: entitlements.ErrLLMConfigUnusable}, withheld: true},
		{name: "client model", grants: &serviceModelGrants{}, supplied: true, withheld: true},
		{name: "client model with the feature", grants: &serviceModelGrants{feature: true}, supplied: true, withheld: false},
		{name: "own model with the entitlement unreadable", grants: &serviceModelGrants{err: errors.New("entitlement down")}, configs: ownModelConfigs{found: true}, withheld: true},
		{name: "client model with the entitlement unreadable", grants: &serviceModelGrants{err: errors.New("entitlement down")}, supplied: true, withheld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var withheld bool
			app, token := newContextFabricTestApp(t, investigatorFunc(func(ctx context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
				calls++
				withheld = contextfabric.PlatformModelWithheld(ctx)
				return contextfabric.InvestigationResult{}, errors.New("stop after the decision")
			}))
			app.runtime.Entitlements = tc.grants
			app.runtime.OrgModelConfigs = tc.configs
			app.runtime.ServiceModelOpen = tc.open
			body := investigationRequestBody()
			if tc.supplied {
				body.SynthesisMode = contractsv1.ContextFabricSynthesisModeClient
				body.SuppliedInterpretation = writeBackInterpretation(t)
			}
			encoded, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, ContextFabricInvestigationsPath, bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("X-ACR-Client-Version", "1.0.0")
			request.Header.Set("Content-Type", "application/json")
			app.Handler().ServeHTTP(httptest.NewRecorder(), request)
			if calls != 1 {
				t.Fatalf("investigator calls = %d, want 1", calls)
			}
			if withheld != tc.withheld {
				t.Fatalf("platform model withheld = %t, want %t", withheld, tc.withheld)
			}
		})
	}
}

// An unreadable acr_service_model closes only the deployment model: a turn the
// organization's own model or the caller's own model can serve is still
// served, with the deployment model withheld.
func TestUnreadableServiceModelEntitlementStillServesTheOwnAndClientModelPaths(t *testing.T) {
	t.Run("own model", func(t *testing.T) {
		rig := gatedClientRouteRig(t, &serviceModelGrants{err: errors.New("entitlement down")}, ownModelConfigs{found: true})
		decodeEnvelope(t, rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil))
		if interprets, synthesizes := rig.model.counts(); interprets != 1 || synthesizes != 1 {
			t.Fatalf("model calls = interpret %d synthesize %d, want 1 and 1 on the organization's model", interprets, synthesizes)
		}
		if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "own_model" {
			t.Fatalf("decision log bases = %v, want [own_model]", got)
		}
	})
	t.Run("client model", func(t *testing.T) {
		rig := newWriteBackRouteRig(t)
		rig.app.runtime.Entitlements = &serviceModelGrants{err: errors.New("entitlement down")}
		rig.app.runtime.OrgModelConfigs = ownModelConfigs{err: errors.New("must not be read on the client path")}
		first := rig.firstCall(t)
		if first.SynthesisInput == nil {
			t.Fatal("the client turn returned no synthesis input")
		}
		if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
			t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
		}
		if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "client_model" {
			t.Fatalf("decision log bases = %v, want [client_model]", got)
		}
	})
}
