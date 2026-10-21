package mcp

import (
	"errors"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// Every 403 feature_not_enabled reason of the service-model gate has a fixed
// refusal text. The gate refuses the caller-model path (a supplied
// interpretation with synthesis "client") for every reason except
// service_model_not_entitled, because acr's own phrasing still binds a model on
// it. A text must therefore point to the caller-model path only where that
// path is served.
func TestServiceModelRefusalTextsNameTheCallerModelPathOnlyWhereItIsServed(t *testing.T) {
	for _, tc := range []struct {
		reason          string
		namesCallerPath bool
	}{
		{reason: contractsv1.ContextFabricServiceModelNotEntitledReason, namesCallerPath: true},
		{reason: contractsv1.ContextFabricOwnModelGateClosedReason},
		{reason: contractsv1.ContextFabricAIFeaturesTurnedOffReason},
		{reason: contractsv1.ContextFabricNoInvestigateConfigReason},
		{reason: contractsv1.ContextFabricOwnModelTurnedOffReason},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			ce := classify(errors.Join(sidecar.ErrFeatureNotEnabled, &sidecar.APIError{Code: "feature_not_enabled", Reason: tc.reason}))
			if ce.category != "entitlement" {
				t.Fatalf("category = %q, want entitlement", ce.category)
			}
			if !strings.HasPrefix(ce.message, "reason="+tc.reason+":") {
				t.Fatalf("message = %q, want it to start with reason=%s:", ce.message, tc.reason)
			}
			for _, name := range []string{toolInvestigateWithInterpretation, promptInterpretQuestion, "prompts/get"} {
				if got := strings.Contains(ce.message, name); got != tc.namesCallerPath {
					t.Fatalf("message names %q = %t, want %t: %s", name, got, tc.namesCallerPath, ce.message)
				}
			}
		})
	}
}
