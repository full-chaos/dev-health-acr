package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const completionDistributionAnswer = `{"data":{"capacityForecast":{"completionDistribution":{"days":[{"value":3,"count":2}],"items":[{"value":9,"count":1}],"__typename":"CompletionDistribution"},"__typename":"CapacityForecast"}}}`

func TestCapacityCompletionDistributionServesATeamForUnrestrictedCaller(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, completionDistributionAnswer }, opHarnessOptions{})
	vars := map[string]any{"input": map[string]any{"teamId": opTeamT}}
	resp := h.run(t, opUnrestricted(opOrgA), "capacityCompletionDistribution", vars)
	raw, _ := json.Marshal(resp)
	reqs := h.upstream.requests()
	if resp.Call != directread.CallServed || len(reqs) != 1 {
		t.Fatalf("want one served call, got %s (%d requests)", raw, len(reqs))
	}
	if !strings.Contains(string(raw), `"completionDistribution"`) || !strings.Contains(string(raw), `"__typename"`) {
		t.Fatalf("the distribution or its __typename is missing from the answer: %s", raw)
	}
	if got, _ := opLookup(reqs[0].variables(), "orgId"); got != opOrgA {
		t.Fatalf("orgId variable %v, want the principal org", got)
	}
	if got, _ := opLookup(reqs[0].variables(), "input.teamId"); got != "t1" {
		t.Fatalf("input.teamId reached the upstream as %v, want the ops form of the team", got)
	}
	if !strings.Contains(string(reqs[0].Raw), "capacityForecast(orgId: $orgId, input: $input)") {
		t.Fatalf("the upstream did not receive the registered document: %s", reqs[0].Raw)
	}
}

func TestCapacityCompletionDistributionRefusesATeamTheCallerCannotSeeWithoutALeak(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, completionDistributionAnswer }, opHarnessOptions{})
	vars := map[string]any{"input": map[string]any{"teamId": opTeamT}}
	// team:t1 exists only in org A's graph.
	resp := h.run(t, opUnrestricted(opOrgB), "capacityCompletionDistribution", vars)
	raw, _ := json.Marshal(resp)
	if resp.Call == directread.CallServed || resp.Refusal == nil || len(resp.Data) != 0 || len(h.upstream.requests()) != 0 {
		t.Fatalf("want a refusal with no data and no upstream call, got %s (%d requests)", raw, len(h.upstream.requests()))
	}
	t.Logf("refusal code %s", resp.Refusal.Code)
	if strings.Contains(string(raw), "completionDistribution") || strings.Contains(string(raw), opOrgA) {
		t.Fatalf("the refusal leaks: %s", raw)
	}
	// An unknown team of the same org is refused the same way.
	same := h.run(t, opUnrestricted(opOrgA), "capacityCompletionDistribution", map[string]any{"input": map[string]any{"teamId": "team:nope"}})
	if same.Call == directread.CallServed || same.Refusal == nil || same.Refusal.Code != resp.Refusal.Code || len(h.upstream.requests()) != 0 {
		raw, _ := json.Marshal(same)
		t.Fatalf("an unknown team must be refused as the foreign one was (%s): %s", resp.Refusal.Code, raw)
	}
}

func TestCapacityCompletionDistributionRefusesTheMultiTeamInputAndARestrictedCaller(t *testing.T) {
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, completionDistributionAnswer }, opHarnessOptions{})
	multi := h.run(t, opUnrestricted(opOrgA), "capacityCompletionDistribution", map[string]any{"input": map[string]any{"teamIds": []any{opTeamT, "team:t2"}}})
	if multi.Refusal == nil || multi.Refusal.Code != directread.RefusalVariableNotAllowed || len(h.upstream.requests()) != 0 {
		raw, _ := json.Marshal(multi)
		t.Fatalf("input.teamIds must be refused as variable_not_allowed with zero upstream: %s", raw)
	}
	restricted := h.run(t, opRestrictedA(), "capacityCompletionDistribution", map[string]any{"input": map[string]any{"teamId": opTeamT}})
	if restricted.Refusal == nil || restricted.Refusal.Code != directread.RefusalOperationNotServedForCaller || len(h.upstream.requests()) != 0 {
		raw, _ := json.Marshal(restricted)
		t.Fatalf("a restricted caller must be refused with zero upstream: %s", raw)
	}
}

func TestCapacityCompletionDistributionWithNoTeamIsTheOrgWideForecastAsForCapacityForecast(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	for _, name := range []string{"capacityCompletionDistribution", "capacityForecast"} {
		t.Run(name, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, completionDistributionAnswer }, opHarnessOptions{})
			op, _ := cat.Lookup(name)
			resp := h.run(t, opUnrestricted(opOrgA), name, opMinimalVariables(t, op))
			if resp.Call != directread.CallServed {
				raw, _ := json.Marshal(resp)
				t.Fatalf("%s without a team: %s", name, raw)
			}
			if got, ok := opLookup(h.upstream.requests()[0].variables(), "input.teamId"); ok && got != nil {
				t.Fatalf("%s invented a team: %v", name, got)
			}
		})
	}
}

func TestCapacityCompletionDistributionPassesOnlyTheDistribution(t *testing.T) {
	answer := `{"data":{"capacityForecast":{"forecastId":"f1","teamId":"t1","p50Date":"2026-10-01","completionDistribution":{"days":[{"value":3,"count":2}],"items":null}}}}`
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
	resp := h.run(t, opUnrestricted(opOrgA), "capacityCompletionDistribution", map[string]any{"input": map[string]any{"teamId": opTeamT}})
	raw, _ := json.Marshal(resp)
	if resp.Call != directread.CallServed {
		t.Fatalf("want served, got %s", raw)
	}
	for _, leak := range []string{"forecastId", "p50Date", `"teamId"`} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("the answer carries %s, which the document does not select: %s", leak, raw)
		}
	}
}
