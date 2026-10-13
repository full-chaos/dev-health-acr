package answerprojection

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestProjectionCarriesTheDriverSourceState(t *testing.T) {
	result := richResult()
	for index := range result.Drivers {
		if result.Drivers[index].DriverID == "driver_principal_1" {
			result.Drivers[index].SourceState = contractsv1.ContextFabricSourceTruncated
		}
	}
	projection := Project(result, Budget{})
	served := map[string]contractsv1.ContextFabricSourceState{}
	for _, projected := range projection.PrincipalDrivers {
		served[projected.DriverID] = projected.SourceState
	}
	if served["driver_principal_1"] != contractsv1.ContextFabricSourceTruncated {
		t.Errorf("projected source_state = %q, want truncated", served["driver_principal_1"])
	}
	if served["driver_principal_2"] != "" {
		t.Errorf("projected source_state of an untouched driver = %q, want empty", served["driver_principal_2"])
	}
}
