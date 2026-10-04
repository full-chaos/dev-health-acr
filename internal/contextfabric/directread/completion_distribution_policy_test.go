package directread_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

const distributionSelection = `completionDistribution { days { value count } items { value count } }`

var completionDistributionQueries = map[string]string{
	"capacityForecast":  `{ capacityForecast(input: {teamId: "team:t1"}) { forecastId ` + distributionSelection + ` } }`,
	"capacityForecasts": `{ capacityForecasts(filters: {teamId: "team:t1", fromDate: "2026-09-01", toDate: "2026-09-30"}) { edges { node { forecastId ` + distributionSelection + ` } } } }`,
}

func TestGraphQLCompletionDistributionIsAnAllowedRead(t *testing.T) {
	for root, query := range completionDistributionQueries {
		t.Run(root, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{})
			resp := h.run(t, opUnrestricted(opOrgA), query, nil)
			h.wantServed(t, resp)
		})
	}
}

func TestGraphQLCompletionDistributionIsRefusedForARestrictedCaller(t *testing.T) {
	for root, query := range completionDistributionQueries {
		t.Run(root, func(t *testing.T) {
			h := newGQLHarness(t, gqlHarnessOptions{})
			resp := h.run(t, opRestrictedA(), query, nil)
			h.wantRefused(t, resp, directread.RefusalOperationNotServedForCaller)
		})
	}
}

func TestGraphQLCompletionDistributionOutputPathsAreExactlyTheDistributionLeaves(t *testing.T) {
	policy, err := directread.DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	ops := gqlRootOps(t, policy)
	for root, prefix := range map[string]string{"capacityForecast": "capacityForecast", "capacityForecasts": "capacityForecasts.edges[*].node"} {
		want := []string{
			prefix + ".completionDistribution.days[*].count",
			prefix + ".completionDistribution.days[*].value",
			prefix + ".completionDistribution.items[*].count",
			prefix + ".completionDistribution.items[*].value",
		}
		var got []string
		for _, op := range ops[root] {
			for _, p := range outputPaths(op) {
				if len(p) > len(prefix) && p[:len(prefix)] == prefix && containsText(p, ".completionDistribution") {
					got = append(got, p)
				}
			}
		}
		if g, w := sortedStrings(got), sortedStrings(want); len(g) != len(w) || g[0] != w[0] || g[1] != w[1] || g[2] != w[2] || g[3] != w[3] {
			t.Fatalf("%s distribution output paths = %v, want %v", root, g, w)
		}
	}
}

func containsText(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestRunOperationDoesNotPassAnUnrequestedCompletionDistribution(t *testing.T) {
	answer := `{"data":{"capacityForecast":{"forecastId":"f1","teamId":"team:t1","completionDistribution":{"days":[{"value":3,"count":2}],"items":null},"__typename":"CapacityForecast"}}}`
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	op, _ := cat.Lookup("capacityForecast")
	resp := h.run(t, opUnrestricted(opOrgA), "capacityForecast", opMinimalVariables(t, op))
	raw, _ := json.Marshal(resp)
	if resp.Call != directread.CallServed {
		t.Fatalf("want served, got %s", raw)
	}
	if strings.Contains(string(raw), "completionDistribution") {
		t.Fatalf("run_operation passed a field its registered document does not select: %s", raw)
	}
}
