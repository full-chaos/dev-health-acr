package directread_test

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-12690 I1a step 2: a repository-restricted caller reads the repository
// investment breakdown of a repository it is granted, and only that one. The
// breakdown is an aggregate over the one forced repository id, so no foreign
// repository can reach a row.
func TestRestrictedCallerReadsGrantedRepositoryBreakdownOnly(t *testing.T) {
	cat := defaultCatalogueOf(t)
	op, refusal := cat.Lookup("investmentBreakdownRepository")
	if refusal != nil {
		t.Fatal(refusal)
	}
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, opNullAnswer(op) }, opHarnessOptions{})
	vars := func(id string) map[string]any {
		return scopedBreakdownVars(map[string]any{"level": "REPO", "ids": []any{id}})
	}

	t.Run("granted_repository_is_served_with_the_forced_scope", func(t *testing.T) {
		h.upstream.reset()
		resp := h.run(t, opRestrictedA(), "investmentBreakdownRepository", vars(opRepo(opRepoA)))
		raw, _ := json.Marshal(resp)
		if resp.Call != directread.CallServed || len(h.upstream.requests()) != 1 {
			t.Fatalf("want served with one upstream request, got %s", raw)
		}
		sent := h.upstream.requests()[0].variables()
		if got, _ := opLookup(sent, "batch.filters.scope.ids"); !jsonEqual(got, []any{opRepoA}) {
			t.Fatalf("sent scope ids %v, want [%s]", got, opRepoA)
		}
	})
	for label, id := range map[string]string{"ungranted_repository": opRepo(opRepoB), "other_org_repository": opRepo(opRepoD)} {
		t.Run(label+"_is_refused_before_dispatch", func(t *testing.T) {
			h.upstream.reset()
			resp := h.run(t, opRestrictedA(), "investmentBreakdownRepository", vars(id))
			raw, _ := json.Marshal(resp)
			if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalDeniedOrNotFound || len(h.upstream.requests()) != 0 {
				t.Fatalf("want denied_or_not_found with zero upstream, got %s (%d requests)", raw, len(h.upstream.requests()))
			}
		})
	}
	t.Run("team_entry_stays_refused", func(t *testing.T) {
		h.upstream.reset()
		resp := h.run(t, opRestrictedA(), "investmentBreakdownTeam", scopedBreakdownVars(map[string]any{"level": "TEAM", "ids": []any{opTeamT}}))
		if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalOperationNotServedForCaller || len(h.upstream.requests()) != 0 {
			t.Fatalf("want operation_not_served_for_caller with zero upstream, got %+v", resp)
		}
	})
}
