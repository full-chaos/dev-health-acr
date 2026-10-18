package directread_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

func TestCapacityForecastServesTeamIdsInTheOpsFormThroughTheOwnershipGate(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, completionDistributionAnswer }, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), "capacityForecast", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT}}})
	raw, _ := json.Marshal(resp)
	reqs := h.upstream.requests()
	if resp.Call != directread.CallServed || len(reqs) != 1 {
		t.Fatalf("want one served call, got %s (%d requests)", raw, len(reqs))
	}
	if got, _ := opLookup(reqs[0].variables(), "input.teamIds"); !reflect.DeepEqual(got, []any{"t1"}) {
		t.Fatalf("input.teamIds reached the upstream as %v, want the ops form [t1]", got)
	}

	foreign := h.run(t, opUnrestricted(opOrgA), "capacityForecast", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT, "team:nope"}}})
	if foreign.Call == directread.CallServed || foreign.Refusal == nil || len(h.upstream.requests()) != 1 {
		raw, _ := json.Marshal(foreign)
		t.Fatalf("one team the caller cannot see must refuse the whole call with no new upstream call: %s", raw)
	}
	otherOrg := h.run(t, opUnrestricted(opOrgB), "capacityForecast", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT}}})
	if otherOrg.Call == directread.CallServed || otherOrg.Refusal == nil || len(h.upstream.requests()) != 1 {
		raw, _ := json.Marshal(otherOrg)
		t.Fatalf("a team of another org must be refused with no new upstream call: %s", raw)
	}
	restricted := h.run(t, opRestrictedA(), "capacityForecast", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT}}})
	if restricted.Refusal == nil || restricted.Refusal.Code != directread.RefusalOperationNotServedForCaller || len(h.upstream.requests()) != 1 {
		raw, _ := json.Marshal(restricted)
		t.Fatalf("a restricted caller must be refused with no new upstream call: %s", raw)
	}
}
