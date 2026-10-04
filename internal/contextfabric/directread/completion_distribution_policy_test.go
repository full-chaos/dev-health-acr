package directread_test

import (
	"encoding/json"
	"slices"
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
	distributionPaths := func(op *directread.OperationPolicy, prefix string) []string {
		var got []string
		for _, p := range outputPaths(op) {
			if strings.HasPrefix(p, prefix) && containsText(p, ".completionDistribution") && !strings.HasSuffix(p, "__typename") {
				got = append(got, p)
			}
		}
		return sortedStrings(got)
	}
	leaves := func(prefix string) []string {
		return sortedStrings([]string{
			prefix + ".completionDistribution.days[*].count",
			prefix + ".completionDistribution.days[*].value",
			prefix + ".completionDistribution.items[*].count",
			prefix + ".completionDistribution.items[*].value",
		})
	}
	for _, op := range ops["capacityForecast"] {
		got := distributionPaths(op, "capacityForecast")
		want := leaves("capacityForecast")
		if op.Name == "capacityForecast" {
			want = sortedStrings(append(want,
				"capacityForecast.completionDistribution.days[*].cumulativeShare",
				"capacityForecast.completionDistribution.horizonDays",
				"capacityForecast.completionDistribution.items[*].cumulativeShare",
				"capacityForecast.completionDistribution.runs",
				"capacityForecast.completionDistribution.unfinishedRuns"))
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s distribution output paths = %v, want %v", op.Name, got, want)
		}
	}
	if len(ops["capacityForecast"]) != 2 {
		t.Fatalf("root capacityForecast has %d operations, want capacityCompletionDistribution and capacityForecast", len(ops["capacityForecast"]))
	}
	for _, op := range ops["capacityForecasts"] {
		prefix := "capacityForecasts.edges[*].node"
		if got, want := distributionPaths(op, prefix), leaves(prefix); !slices.Equal(got, want) {
			t.Fatalf("%s distribution output paths = %v, want %v", op.Name, got, want)
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

func TestRunOperationPassesOnlyWhatTheRegisteredDocumentSelects(t *testing.T) {
	answer := `{"data":{"capacityForecast":{"forecastId":"f1","teamId":"team:t1","completionDistribution":{"runs":500,"unfinishedRuns":7,"horizonDays":365,"days":[{"value":3,"count":2,"cumulativeShare":0.4}],"items":[{"value":9,"count":1,"cumulativeShare":1}]},"__typename":"CapacityForecast"}}}`
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	for operation, wantFields := range map[string]struct{ present, absent []string }{
		"capacityCompletionDistribution": {present: []string{"completionDistribution"}, absent: []string{"forecastId", "teamId"}},
		"capacityForecast":               {present: []string{"completionDistribution", "forecastId", `"runs":500`, `"unfinishedRuns":7`, `"horizonDays":365`, `"cumulativeShare":0.4`, `"cumulativeShare":1`}},
	} {
		t.Run(operation, func(t *testing.T) {
			h := newOpHarness(t, func(opRecorded) (int, string) { return 200, answer }, opHarnessOptions{})
			op, _ := cat.Lookup(operation)
			resp := h.run(t, opUnrestricted(opOrgA), operation, opMinimalVariables(t, op))
			raw, _ := json.Marshal(resp)
			if resp.Call != directread.CallServed {
				t.Fatalf("want served, got %s", raw)
			}
			for _, f := range wantFields.present {
				if !strings.Contains(string(raw), f) {
					t.Fatalf("answer lacks %s: %s", f, raw)
				}
			}
			for _, f := range wantFields.absent {
				if strings.Contains(string(raw), f) {
					t.Fatalf("run_operation passed %s, which its registered document does not select: %s", f, raw)
				}
			}
		})
	}
}
