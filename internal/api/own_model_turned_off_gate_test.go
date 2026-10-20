package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/entitlements"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// opsStandInClient is the real entitlement client over an ops stand-in.
func opsStandInClient(t *testing.T, ops opsStandIn) *entitlements.Client {
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
	return client
}

// An org admin turned the saved model configuration off: the organization has
// no usable own model. acr_service_model OFF is refused as AI features turned
// off before the toggle is read; with acr_service_model and the fallback grant
// the turn runs on the platform model; with acr_service_model alone it is
// refused with own_model_turned_off. The organization's own model is never
// called.
func TestOwnModelTurnedOffIsAnOrganizationWithNoUsableOwnModel(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ops        opsStandIn
		status     int
		reason     string
		basis      string
		deployment int
	}{
		{name: "acr_service_model alone is refused as turned off", ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "turned_off"}, status: http.StatusForbidden, reason: "own_model_turned_off", basis: "own_model_turned_off"},
		{name: "acr_service_model from an ops build without the fallback field is refused as turned off", ops: opsStandIn{serviceModel: true, llmConfig: "turned_off"}, status: http.StatusForbidden, reason: "own_model_turned_off", basis: "own_model_turned_off"},
		{name: "acr_service_model off is refused as AI features turned off even with the fallback grant", ops: opsStandIn{fallback: boolRef(true), llmConfig: "turned_off"}, status: http.StatusForbidden, reason: "ai_features_turned_off", basis: "ai_features_turned_off"},
		{name: "nothing held is refused as AI features turned off", ops: opsStandIn{llmConfig: "turned_off"}, status: http.StatusForbidden, reason: "ai_features_turned_off", basis: "ai_features_turned_off"},
		{name: "acr_service_model and the fallback grant run on the platform model", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "turned_off"}, status: http.StatusOK, basis: "platform_model_fallback", deployment: 2},
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
			if got := sides.count("own"); got != 0 {
				t.Fatalf("own model calls = %d, want 0", got)
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

// A web assertion caller (Ask Dev) is decided on the same rule.
func TestWebAssertionCallerWithTheOwnModelTurnedOffIsRefused(t *testing.T) {
	client := opsStandInClient(t, opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "turned_off"})
	app := &App{logger: slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)), runtime: &RuntimeDependencies{Entitlements: client, OrgModelConfigs: opsClientConfigs{client: client}}}
	basis, allowed, err := app.serviceModelDecision(context.Background(), storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, OrgID: "org_1"}, contextfabric.InvestigationRequest{})
	if err != nil || allowed || string(basis) != "own_model_turned_off" {
		t.Fatalf("decision = %q %t %v, want own_model_turned_off, not allowed, no error", basis, allowed, err)
	}
}

// investigate_question stays listed for an organization whose own model is
// turned off (the call answers the reason), is unlisted when acr_service_model
// is off, and is listed on the fallback grant.
func TestListingWithTheOwnModelTurnedOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ops    opsStandIn
		listed bool
		reason string
	}{
		{name: "acr_service_model alone", ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "turned_off"}, listed: true, reason: "own_model_turned_off"},
		{name: "acr_service_model off", ops: opsStandIn{fallback: boolRef(true), llmConfig: "turned_off"}, listed: false, reason: "ai_features_turned_off"},
		{name: "both flags", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "turned_off"}, listed: true, reason: "granted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := opsStandInClient(t, tc.ops)
			logs := &bytes.Buffer{}
			app := &App{logger: slog.New(slog.NewJSONHandler(logs, nil)), runtime: &RuntimeDependencies{Entitlements: client, OrgModelConfigs: opsClientConfigs{client: client}}}
			listed := app.serviceModelListable(context.Background(), storage.Principal{OrgID: "org_1"})
			if listed != tc.listed {
				t.Fatalf("listed = %t, want %t; logs=%s", listed, tc.listed, logs.String())
			}
			if got := listingReasons(t, logs); len(got) != 1 || got[0] != tc.reason {
				t.Fatalf("listing reasons = %v, want [%s]", got, tc.reason)
			}
		})
	}
}

func listingReasons(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var reasons []string
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry struct {
			Msg    string `json:"msg"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "context fabric service model listing decision" {
			reasons = append(reasons, entry.Reason)
		}
	}
	return reasons
}

// The caller-model path (its own interpretation and synthesis "client") passes
// the same gate: a turned-off own model with no grant is refused with its
// reason, as a closed gate is, and nothing is phrased; with both flags the turn
// is served on the platform model.
func TestCallerModelTurnWithTheOwnModelTurnedOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ops    opsStandIn
		reason string
	}{
		{name: "acr_service_model alone", ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "turned_off"}, reason: "own_model_turned_off"},
		{name: "acr_service_model from an ops build without the fallback field", ops: opsStandIn{serviceModel: true, llmConfig: "turned_off"}, reason: "own_model_turned_off"},
		{name: "acr_service_model off with the fallback grant", ops: opsStandIn{fallback: boolRef(true), llmConfig: "turned_off"}, reason: "ai_features_turned_off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPhrasingRouteRig(t, &serviceModelGrants{}, false)
			client := opsStandInClient(t, tc.ops)
			rig.app.runtime.Entitlements = client
			rig.app.runtime.OrgModelConfigs = opsClientConfigs{client: client}
			recorder := rig.postRaw(t)
			body := decodeServiceModelRefusal(t, recorder, http.StatusForbidden)
			if body.Error.Code != "feature_not_enabled" || body.Error.Details["reason"] != tc.reason {
				t.Fatalf("error = %+v, want feature_not_enabled with reason %s", body.Error, tc.reason)
			}
			if rig.phrasing.count() != 0 || rig.byo.count() != 0 {
				t.Fatalf("phrasing calls = deployment %d byo %d, want 0 and 0", rig.phrasing.count(), rig.byo.count())
			}
			if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
				t.Fatalf("model calls = interpret %d synthesize %d, want 0 and 0", interprets, synthesizes)
			}
		})
	}
}

func TestCallerModelTurnWithTheOwnModelTurnedOffAndBothFlagsIsServedOnThePlatformModel(t *testing.T) {
	rig := newPhrasingRouteRig(t, &serviceModelGrants{}, false)
	client := opsStandInClient(t, opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "turned_off"})
	rig.app.runtime.Entitlements = client
	rig.app.runtime.OrgModelConfigs = opsClientConfigs{client: client}
	options := kindOptionsOf(t, rig.post(t))
	if got := rig.phrasing.count(); got != 1 || rig.byo.count() != 0 {
		t.Fatalf("phrasing calls = deployment %d byo %d, want 1 and 0", got, rig.byo.count())
	}
	if options[0].Phrasing != phrasedKindLabel {
		t.Fatalf("kind option phrasing = %q, want %q", options[0].Phrasing, phrasedKindLabel)
	}
	if got := serviceModelDecisionBasis(t, rig.logs); len(got) != 1 || got[0] != "platform_model_fallback" {
		t.Fatalf("decision log bases = %v, want [platform_model_fallback]", got)
	}
}
