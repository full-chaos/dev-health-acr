package api

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The feature_not_enabled description of the investigation route says which
// refusal reasons the caller-model path (its own interpretation and synthesis
// "client") still serves, and why the others refuse it. Each of the five
// reasons is reproduced on the server path; the same organization then sends a
// caller-model turn. The reasons the description names as served are exactly
// the ones the gate serves, and the refusal is put on the gate, not on a model
// call: the refused turns make none.
func TestFeatureNotEnabledDescriptionNamesTheReasonsTheCallerModelServes(t *testing.T) {
	cases := []struct {
		reason string
		ops    opsStandIn
	}{
		{reason: contractsv1.ContextFabricServiceModelNotEntitledReason, ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "none"}},
		{reason: contractsv1.ContextFabricOwnModelGateClosedReason, ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "gate_closed"}},
		{reason: contractsv1.ContextFabricAIFeaturesTurnedOffReason, ops: opsStandIn{fallback: boolRef(true), llmConfig: "saved"}},
		{reason: contractsv1.ContextFabricNoInvestigateConfigReason, ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "no_investigate"}},
		{reason: contractsv1.ContextFabricOwnModelTurnedOffReason, ops: opsStandIn{serviceModel: true, fallback: boolRef(false), llmConfig: "turned_off"}},
	}
	var served, refused []string
	for _, tc := range cases {
		serverRig, _ := opsBackedRouteRig(t, tc.ops)
		serverBody := decodeServiceModelRefusal(t, serverRig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil), http.StatusForbidden)
		if serverBody.Error.Code != "feature_not_enabled" || serverBody.Error.Details["reason"] != tc.reason {
			t.Fatalf("%s: server-path error = %+v, want feature_not_enabled with that reason", tc.reason, serverBody.Error)
		}

		rig := newPhrasingRouteRig(t, &serviceModelGrants{}, false)
		client := opsStandInClient(t, tc.ops)
		rig.app.runtime.Entitlements = client
		rig.app.runtime.OrgModelConfigs = opsClientConfigs{client: client}
		recorder := rig.postRaw(t)
		switch recorder.Code {
		case http.StatusOK:
			served = append(served, tc.reason)
		case http.StatusForbidden:
			body := decodeServiceModelRefusal(t, recorder, http.StatusForbidden)
			if body.Error.Code != "feature_not_enabled" || body.Error.Details["reason"] != tc.reason {
				t.Fatalf("%s: caller-model error = %+v, want feature_not_enabled with that reason", tc.reason, body.Error)
			}
			if rig.phrasing.count() != 0 || rig.byo.count() != 0 {
				t.Fatalf("%s: phrasing calls = deployment %d byo %d, want 0 and 0", tc.reason, rig.phrasing.count(), rig.byo.count())
			}
			if interprets, synthesizes := rig.model.counts(); interprets != 0 || synthesizes != 0 {
				t.Fatalf("%s: model calls = interpret %d synthesize %d, want 0 and 0", tc.reason, interprets, synthesizes)
			}
			refused = append(refused, tc.reason)
		default:
			t.Fatalf("%s: caller-model status = %d body=%s, want 200 or 403", tc.reason, recorder.Code, recorder.Body.String())
		}
	}
	if len(served) == 0 || len(refused) == 0 {
		t.Fatalf("served = %v refused = %v, want both non-empty", served, refused)
	}

	description := investigationForbiddenDescription(t)
	for _, tc := range cases {
		if !strings.Contains(description, "feature_not_enabled with details.reason "+tc.reason+":") {
			t.Errorf("description does not describe reason %s", tc.reason)
		}
	}
	servedSentence := sentenceWith(t, description, "is served on the caller's own model")
	if !strings.HasPrefix(servedSentence, "Only ") {
		t.Errorf("served sentence = %q, want it to start with \"Only \"", servedSentence)
	}
	if got := reasonsNamedIn(servedSentence, cases); !equalSorted(got, served) {
		t.Errorf("description serves the caller-model path for %v, the gate serves it for %v; sentence %q", got, served, servedSentence)
	}
	refusedSentence := sentenceWith(t, description, "refuse that path too")
	if !strings.Contains(refusedSentence, "before the caller's model is read") {
		t.Errorf("refused sentence = %q, want the gate (before the caller's model is read) as its cause", refusedSentence)
	}
	if strings.Contains(refusedSentence, "binds a model") {
		t.Errorf("refused sentence = %q puts the refusal on a model binding; the refused turns make no model call", refusedSentence)
	}
}

func investigationForbiddenDescription(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../contracts/openapi/acr-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Components struct {
			Responses map[string]struct {
				Description string `json:"description"`
			} `json:"responses"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	response, ok := document.Components.Responses["ContextFabricInvestigationForbidden"]
	if !ok || response.Description == "" {
		t.Fatal("components.responses.ContextFabricInvestigationForbidden has no description")
	}
	return response.Description
}

func sentenceWith(t *testing.T, text, phrase string) string {
	t.Helper()
	var found []string
	for _, sentence := range strings.SplitAfter(text, ". ") {
		if strings.Contains(sentence, phrase) {
			found = append(found, strings.TrimSpace(sentence))
		}
	}
	if len(found) != 1 {
		t.Fatalf("sentences with %q = %q, want exactly one", phrase, found)
	}
	return found[0]
}

func reasonsNamedIn(sentence string, cases []struct {
	reason string
	ops    opsStandIn
}) []string {
	var named []string
	for _, tc := range cases {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(tc.reason) + `\b`).MatchString(sentence) {
			named = append(named, tc.reason)
		}
	}
	return named
}

func equalSorted(a, b []string) bool {
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}
