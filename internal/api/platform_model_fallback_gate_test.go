package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// opsStandIn answers the two ops routes acr reads for the model decision: the
// entitlement route and the org llm-config route, as ops serves them.
type opsStandIn struct {
	serviceModel bool
	// fallback is the platform_model_fallback field; nil leaves it out of the
	// body, as an ops build without the field sends it.
	fallback *bool
	// llmConfig is "saved", "none" (404 + X-Org-Llm-Config: none),
	// "gate_closed" (that 404 plus the byo_llm gate-closed reason header) or
	// "no_investigate" (that 404 plus the reason that the org's saved BYO
	// keys are for other purposes only).
	llmConfig string
}

func (s opsStandIn) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/internal/acr/entitlements/"):
		orgID, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/acr/entitlements/"))
		body := map[string]any{"schema_version": "acr_entitlement.v1", "org_id": orgID, "agent_context_runtime": true, "acr_service_model": s.serviceModel}
		if s.fallback != nil {
			body["platform_model_fallback"] = *s.fallback
		}
		_ = json.NewEncoder(w).Encode(body)
	case strings.HasPrefix(r.URL.Path, "/api/v1/internal/acr/orgs/") && strings.HasSuffix(r.URL.Path, "/llm-config"):
		orgID, _ := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/internal/acr/orgs/"), "/llm-config"))
		switch s.llmConfig {
		case "saved":
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": "acr_llm_config.v1", "org_id": orgID, "provider": "openai", "model": "org-model", "base_url": "", "api_key": "sk-org-standin", "version": "v1"})
		case "gate_closed":
			w.Header().Set("X-Org-Llm-Config", "none")
			w.Header().Set("X-Org-Llm-Config-Reason", "byo_llm_gate_closed")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		case "no_investigate":
			w.Header().Set("X-Org-Llm-Config", "none")
			w.Header().Set("X-Org-Llm-Config-Reason", "byo_no_investigate_config")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		case "scope_refused":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"Forbidden"}`))
		case "credential_refused":
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"Unauthorized"}`))
		case "bare_404":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		case "unknown_reason":
			w.Header().Set("X-Org-Llm-Config", "none")
			w.Header().Set("X-Org-Llm-Config-Reason", "some_future_reason")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		case "reason_without_marker":
			w.Header().Set("X-Org-Llm-Config-Reason", "byo_llm_gate_closed")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		default:
			w.Header().Set("X-Org-Llm-Config", "none")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not found"}`))
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// opsClientConfigs is the hosted ops config source over the real entitlement
// client: a served config is the org's own model, a 404 is none, any error
// passes through.
type opsClientConfigs struct{ client *entitlements.Client }

func (o opsClientConfigs) ResolveOrgModelConfig(ctx context.Context, orgID string) (contextfabric.ResolvedOrgModelConfig, bool, error) {
	config, found, err := o.client.OrgLLMConfig(ctx, orgID)
	if err != nil || !found {
		return contextfabric.ResolvedOrgModelConfig{}, false, err
	}
	return contextfabric.ResolvedOrgModelConfig{Provider: config.Provider, BaseURL: config.BaseURL, Model: config.Model, Credential: config.APIKey, Generation: 1}, true, nil
}

// modelSides counts the model calls served by the organization's own model
// and by the deployment model.
type modelSides struct {
	mu    sync.Mutex
	calls map[string]int
}

func (m *modelSides) count(side string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[side]
}

type countedModelSide struct {
	side  string
	sides *modelSides
	inner contextfabric.ModelRuntime
}

func (c countedModelSide) note() {
	c.sides.mu.Lock()
	defer c.sides.mu.Unlock()
	c.sides.calls[c.side]++
}

func (c countedModelSide) InterpretQuestion(ctx context.Context, principal storage.Principal, request contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	c.note()
	return c.inner.InterpretQuestion(ctx, principal, request)
}

func (c countedModelSide) SynthesizeAnswer(ctx context.Context, principal storage.Principal, input contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	c.note()
	return c.inner.SynthesizeAnswer(ctx, principal, input)
}

// opsBackedRouteRig is the investigation route with the real entitlement
// client and the real per-organization model resolver in front of the model:
// the organization's own model and the deployment model are counted apart.
func opsBackedRouteRig(t *testing.T, ops opsStandIn) (*clientRouteRig, *modelSides) {
	t.Helper()
	server := httptest.NewServer(ops)
	t.Cleanup(server.Close)
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("svc_acr_standin_token_0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := entitlements.New(entitlements.Config{
		BaseURL: baseURL, TokenFile: tokenFile, Timeout: time.Second, MaxResponseBytes: 4096,
		PositiveCacheTTL: time.Second, NegativeCacheTTL: time.Second, CacheCapacity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	sides := &modelSides{calls: map[string]int{}}
	configs := opsClientConfigs{client: client}
	fixture := singleSubjectFixture()
	fixture.runtime = func(model contextfabric.ModelRuntime) contextfabric.ModelRuntime {
		return modelruntimeresolver.New(countedModelSide{side: "deployment", sides: sides, inner: model}, configs,
			func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
				return countedModelSide{side: "own", sides: sides, inner: model}, nil
			})
	}
	rig := newClientRouteRig(t, fixture)
	rig.app.runtime.Entitlements = client
	rig.app.runtime.OrgModelConfigs = configs
	return rig, sides
}

func boolRef(value bool) *bool { return &value }

// The platform model is drawn only by an organization with no saved model
// configuration that holds both acr_service_model (AI features on) and the
// platform-admin grant platform_model_fallback. A saved configuration runs on
// the organization's own model whatever the flags; one behind a closed byo_llm
// gate is refused with its own reason, even with both flags held.
func TestPlatformModelIsDrawnOnlyWithTheFeatureAndTheFallbackGrant(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ops        opsStandIn
		status     int
		reason     string
		basis      string
		own        int
		deployment int
	}{
		{name: "saved config with acr_service_model runs on the own model", ops: opsStandIn{serviceModel: true, llmConfig: "saved"}, status: http.StatusOK, basis: "own_model", own: 2},
		{name: "saved config with acr_service_model off is refused as turned off", ops: opsStandIn{llmConfig: "saved"}, status: http.StatusForbidden, reason: "ai_features_turned_off", basis: "ai_features_turned_off"},
		{name: "saved config with acr_service_model off is refused even with the fallback grant", ops: opsStandIn{fallback: boolRef(true), llmConfig: "saved"}, status: http.StatusForbidden, reason: "ai_features_turned_off", basis: "ai_features_turned_off"},
		{name: "saved config with both flags still runs on the own model", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "saved"}, status: http.StatusOK, basis: "own_model", own: 2},
		{name: "acr_service_model alone with no config is refused", ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "none"}, status: http.StatusForbidden, reason: "service_model_not_entitled", basis: "refused"},
		{name: "acr_service_model alone from an ops build without the field is refused", ops: opsStandIn{serviceModel: true, llmConfig: "none"}, status: http.StatusForbidden, reason: "service_model_not_entitled", basis: "refused"},
		{name: "acr_service_model and the fallback grant with no config run on the platform model", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "none"}, status: http.StatusOK, basis: "platform_model_fallback", deployment: 2},
		{name: "the fallback grant alone with no config is refused", ops: opsStandIn{fallback: boolRef(true), llmConfig: "none"}, status: http.StatusForbidden, reason: "service_model_not_entitled", basis: "refused"},
		{name: "nothing held and no config is refused", ops: opsStandIn{llmConfig: "none"}, status: http.StatusForbidden, reason: "service_model_not_entitled", basis: "refused"},
		{name: "BYO keys for other purposes only are refused with their reason", ops: opsStandIn{serviceModel: true, llmConfig: "no_investigate"}, status: http.StatusForbidden, reason: "byo_no_investigate_config", basis: "byo_no_investigate_config"},
		{name: "BYO keys for other purposes only are refused even with both flags", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "no_investigate"}, status: http.StatusForbidden, reason: "byo_no_investigate_config", basis: "byo_no_investigate_config"},
		{name: "saved config behind a closed gate is refused with its reason", ops: opsStandIn{serviceModel: true, llmConfig: "gate_closed"}, status: http.StatusForbidden, reason: "own_model_gate_closed", basis: "own_model_gate_closed"},
		{name: "saved config behind a closed gate is refused even with the fallback grant", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "gate_closed"}, status: http.StatusForbidden, reason: "own_model_gate_closed", basis: "own_model_gate_closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, sides := opsBackedRouteRig(t, tc.ops)
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			if recorder.Code != tc.status {
				t.Fatalf("status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), tc.status)
			}
			if tc.status == http.StatusForbidden {
				body := decodeServiceModelRefusal(t, recorder, tc.status)
				if body.Error.Code != "feature_not_enabled" || body.Error.Retryable || body.Error.Details["reason"] != tc.reason {
					t.Fatalf("error = %+v, want feature_not_enabled, not retryable, reason %s", body.Error, tc.reason)
				}
				assertNoModelCallAndNoResult(t, rig)
			}
			if got := sides.count("own"); got != tc.own {
				t.Fatalf("own model calls = %d, want %d", got, tc.own)
			}
			if got := sides.count("deployment"); got != tc.deployment {
				t.Fatalf("deployment model calls = %d, want %d", got, tc.deployment)
			}
			if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != tc.basis {
				t.Fatalf("decision log bases = %v, want [%s]", got, tc.basis)
			}
		})
	}
}

// Web assertion callers (Ask Dev) are decided on the same rule: with no model
// configuration and neither flag, the turn is refused.
func TestWebAssertionCallerIsDecidedOnTheSameRule(t *testing.T) {
	app := &App{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), runtime: &RuntimeDependencies{Entitlements: &serviceModelGrants{}, OrgModelConfigs: ownModelConfigs{}}}
	basis, allowed, err := app.serviceModelDecision(context.Background(), storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, OrgID: "org_1"}, contextfabric.InvestigationRequest{})
	if err != nil || allowed || basis != serviceModelBasisRefused {
		t.Fatalf("decision = %q %t %v, want refused, not allowed, no error", basis, allowed, err)
	}
}

// Only the marked no-config answer (404 with X-Org-Llm-Config: none and no reason)
// lets the platform model draw. A bare 404 (a stale ops build, a proxy that drops
// the header) or a reason this build does not know leaves the organization's BYO
// state unknown: the turn fails closed and neither model is called, even for an
// organization holding both flags.
func TestAnUnmarkedOrUnknownOpsNotFoundNeverDrawsThePlatformModel(t *testing.T) {
	for _, mode := range []string{"bare_404", "unknown_reason", "reason_without_marker"} {
		t.Run(mode, func(t *testing.T) {
			rig, sides := opsBackedRouteRig(t, opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: mode})
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			if recorder.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d body=%s, want 503", recorder.Code, recorder.Body.String())
			}
			if got := sides.count("deployment"); got != 0 {
				t.Fatalf("deployment model calls = %d, want 0", got)
			}
			if got := sides.count("own"); got != 0 {
				t.Fatalf("own model calls = %d, want 0", got)
			}
			assertNoModelCallAndNoResult(t, rig)
		})
	}
}

// A credential or scope refusal from ops is an outage of the config source, not
// a saved configuration: the caller sees the runtime-outage answer (503
// store_unavailable), never a per-org reason such as ai_features_turned_off, and
// no model is called, even for an organization holding both flags.
func TestAnOpsCredentialOrScopeRefusalIsUnavailableNotASavedConfiguration(t *testing.T) {
	for _, mode := range []string{"scope_refused", "credential_refused"} {
		t.Run(mode, func(t *testing.T) {
			rig, sides := opsBackedRouteRig(t, opsStandIn{fallback: boolRef(true), llmConfig: mode})
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			body := decodeServiceModelRefusal(t, recorder, http.StatusServiceUnavailable)
			if body.Error.Code != "store_unavailable" || !body.Error.Retryable {
				t.Fatalf("error = %+v, want retryable store_unavailable", body.Error)
			}
			if sides.count("deployment") != 0 || sides.count("own") != 0 {
				t.Fatalf("model calls = deployment %d own %d, want 0 and 0", sides.count("deployment"), sides.count("own"))
			}
			assertNoModelCallAndNoResult(t, rig)
		})
	}
}
