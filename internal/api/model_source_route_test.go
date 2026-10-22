package api

import (
	"encoding/json"
	"net/http"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A served and a stored result name whose model served the turn: the
// organization's own saved model, or the platform model. The turn's model
// calls are counted on the side they ran on, so the label is held to what ran.
func TestAServedResultNamesWhoseModelServedTheTurn(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ops        opsStandIn
		want       contractsv1.ContextFabricModelSource
		own        int
		deployment int
	}{
		{name: "a saved own model", ops: opsStandIn{serviceModel: true, llmConfig: "saved"}, want: contractsv1.ContextFabricModelSourceOrganization, own: 2},
		{name: "the platform model on both grants", ops: opsStandIn{serviceModel: true, fallback: boolRef(true), llmConfig: "none"}, want: contractsv1.ContextFabricModelSourcePlatform, deployment: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig, sides := opsBackedRouteRig(t, tc.ops)
			served := decodeEnvelope(t, rig.post(t, contractsv1.ContextFabricSynthesisModeServer, nil))
			if sides.count("own") != tc.own || sides.count("deployment") != tc.deployment {
				t.Fatalf("model calls own=%d deployment=%d, want own=%d deployment=%d", sides.count("own"), sides.count("deployment"), tc.own, tc.deployment)
			}
			if served.ModelSource != tc.want {
				t.Fatalf("served model_source = %q, want %q", served.ModelSource, tc.want)
			}
			read := rig.get(served.ResultID)
			if read.Code != http.StatusOK {
				t.Fatalf("read by id: status = %d body=%s", read.Code, read.Body.String())
			}
			var stored contractsv1.ContextFabricInvestigationResult
			if err := json.Unmarshal(read.Body.Bytes(), &stored); err != nil {
				t.Fatal(err)
			}
			if stored.ModelSource != tc.want {
				t.Fatalf("stored model_source = %q, want %q", stored.ModelSource, tc.want)
			}
		})
	}
}
