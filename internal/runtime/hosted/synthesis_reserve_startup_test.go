package hosted

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

// A request timeout the composition reserve does not fit in refuses the start
// of an investigator that would use it, and only that: a deployment with
// investigations off starts on the same timeout.
func TestTheCompositionReserveIsCheckedOnlyWhereInvestigationsRun(t *testing.T) {
	t.Setenv("ACR_CONTEXT_FABRIC_FALKOR_ADDR", "falkor.invalid:6379")
	cases := []struct {
		name    string
		enabled bool
		timeout time.Duration
		wantErr bool
	}{
		{"investigations off, short timeout", false, 5 * time.Second, false},
		{"investigations on, timeout equal to the reserve", true, 5 * time.Second, true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := buildRequest{config: config.Config{EnableContextFabricInvestigations: testCase.enabled, RequestTimeout: testCase.timeout}}
			if !testCase.enabled {
				t.Setenv("ACR_CONTEXT_FABRIC_FALKOR_ADDR", "")
			}
			_, _, _, _, _, _, err := buildContextFabricInvestigator(context.Background(), request, postgresComponents{}, clickHouseComponents{}, nil)
			if testCase.wantErr != (err != nil && strings.Contains(err.Error(), "synthesis composition reserve")) || (!testCase.wantErr && err != nil) {
				t.Fatalf("err = %v, want the reserve refusal: %v", err, testCase.wantErr)
			}
		})
	}
}
