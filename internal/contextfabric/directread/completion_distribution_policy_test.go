package directread_test

import (
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
