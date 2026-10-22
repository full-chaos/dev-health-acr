package api

import (
	"net/http"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A saved configuration whose base_url carries the organization's own api key
// (or userinfo) is refused before any model is built: the organization's own
// model is never called, the platform model never stands in for it, the turn
// answers the runtime-outage answer, and neither the url nor the key reaches
// the log. The same org with a clean base_url (a provider's api-version query)
// still runs on its own model, so the counts can tell a refusal from silence.
func TestABaseURLThatCarriesTheKeyNeverReachesTheOrgModel(t *testing.T) {
	for _, mode := range []string{"saved_key_in_base_url", "saved_userinfo_in_base_url"} {
		t.Run(mode, func(t *testing.T) {
			rig, sides := opsBackedRouteRig(t, opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: mode})
			recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
			body := decodeServiceModelRefusal(t, recorder, http.StatusServiceUnavailable)
			if body.Error.Code != "upstream_unavailable" {
				t.Fatalf("error = %+v, want the runtime-outage answer upstream_unavailable", body.Error)
			}
			if sides.count("own") != 0 || sides.count("deployment") != 0 {
				t.Fatalf("model calls = own %d deployment %d, want 0 and 0", sides.count("own"), sides.count("deployment"))
			}
			assertNoModelCallAndNoResult(t, rig)
			for _, text := range []string{recorder.Body.String(), rig.logs.String()} {
				for _, secret := range []string{"sk-org-standin", "own.example.invalid", "user:pass"} {
					if strings.Contains(text, secret) {
						t.Fatalf("%q reached the response or the log", secret)
					}
				}
			}
		})
	}
	rig, sides := opsBackedRouteRig(t, opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "saved_api_version_base_url"})
	recorder := rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil)
	if recorder.Code != http.StatusOK || sides.count("own") != 2 {
		t.Fatalf("clean base_url: status = %d own calls = %d body=%s, want 200 and 2", recorder.Code, sides.count("own"), recorder.Body.String())
	}
}
