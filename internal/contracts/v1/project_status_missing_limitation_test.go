package v1

import "testing"

func TestEveryProjectStatusMissingLimitationIsServiceAuthored(t *testing.T) {
	states := []string{"no_data", "conflicted", "unavailable", "no_value", "no_evidence", "unconfigured", "unauthorized", "pruned", "not_applicable", "not_read", "other_state"}
	for _, kind := range []string{"workload", "readiness", "health"} {
		for _, state := range states {
			sentence := ContextFabricProjectStatusMissingLimitation(kind, state)
			if !IsContextFabricServiceAuthoredLimitation(sentence) {
				t.Errorf("%q is not recognized as a service-authored limitation", sentence)
			}
		}
	}
	if IsContextFabricServiceAuthoredLimitation("readiness: looks fine to me.") {
		t.Error("a model caveat shaped like a status sentence was recognized as service-authored")
	}
}
