package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func teamReadinessScopeRows() [][]any {
	day := "2026-10-03"
	return [][]any{
		{"CHAOS", "scope-a", "linear", day, int64(0), int64(0), int64(0), uint8(0), float64(0)},
		{"CHAOS", "scope-b", "linear", day, int64(0), int64(1), int64(1), uint8(1), float64(0)},
		{"CHAOS", "scope-c", "linear", day, int64(0), int64(5), int64(5), uint8(1), float64(0)},
		{"CHAOS", "scope-d", "github", "2026-10-04", int64(0), int64(5), int64(5), uint8(1), float64(0)},
		{"CHAOS", "scope-e", "github", "2026-08-16", int64(0), int64(0), int64(0), uint8(0), float64(0)},
		{"CHAOS", "scope-f", "linear", "2026-08-07", int64(0), int64(8), int64(8), uint8(1), float64(0)},
	}
}

func permuted[T any](rows []T, order []int) []T {
	out := make([]T, 0, len(rows))
	for _, index := range order {
		out = append(out, rows[index])
	}
	return out
}

func teamReadinessClient(order []int) *fakeClient {
	return &fakeClient{tables: []fakeTable{
		{match: readinessOriginalQueryMatch, rows: permuted(teamReadinessScopeRows(), order)},
		{match: readinessDailySeriesMatch, rows: [][]any{
			readinessDailySeriesRow("CHAOS", "2026-10-03", 0, 6, 6),
			readinessDailySeriesRow("CHAOS", "2026-08-07", 0, 8, 8),
		}},
	}}
}

// teamMixClient owns four repositories whose efforts sum to a different last
// digit in a different order (0.1+0.2+0.3 != 0.3+0.2+0.1), and scales every
// float by (1+noise): the last-digit difference two ClickHouse sums of the
// same rows can carry.
func teamMixClient(order []int, noise float64) *fakeClient {
	repos := []string{"repo-a", "repo-b", "repo-c", "repo-d"}
	efforts := []float64{0.1, 0.2, 0.3, 0.7}
	owned := make([][]any, 0, len(repos))
	mix := make([][]any, 0, len(repos))
	for index, repo := range repos {
		owned = append(owned, []any{"CHAOS", repo})
		scale := efforts[index] * (1 + noise)
		mix = append(mix, []any{uint8(0), repo, map[string]float64{
			"feature_delivery": 0.48898883728007437 * scale, "operational": 0.047865254810142 * scale,
			"maintenance": 0.2304889002988043 * scale, "quality": 0.1388265562681117 * scale, "risk": 0.0936 * scale,
		}, 0.0864588925764 * scale, uint64(3)})
	}
	return &fakeClient{tables: []fakeTable{
		{match: "FROM team_repo_ownership", rows: permuted(owned, order)},
		{match: "FROM work_unit_investments", rows: permuted(mix, order)},
	}}
}

// TestTeamFactClientInputIsStableOverRowOrderAndAggregationNoise is the prod
// shape: two reads of the same team question's facts over the same static
// rows, served in different orders, must carry byte-equal client input.
func TestTeamFactClientInputIsStableOverRowOrderAndAggregationNoise(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	subject := teamSubject("CHAOS")
	tc := clockTestCases()[1]
	orders := [][]int{{0, 1, 2, 3, 4, 5}, {5, 4, 3, 2, 1, 0}, {2, 0, 5, 1, 4, 3}}
	cases := []struct {
		kind contextfabric.FactKind
		make func(order []int, noise float64) *fakeClient
	}{
		{contextfabric.FactReadiness, func(order []int, _ float64) *fakeClient { return teamReadinessClient(order) }},
		{contextfabric.FactInvestment, func(order []int, noise float64) *fakeClient {
			return teamMixClient(order[:4:4], noise)
		}},
	}
	for _, c := range cases {
		var first []byte
		for index, order := range orders {
			for _, noise := range []float64{0, 3e-16} {
				if c.kind == contextfabric.FactInvestment {
					order = permutedFour(order)
				}
				provider := findProvider(t, devhealthfacts.NewProviders(c.make(order, noise)), c.kind)
				payload, facts, err := clockTestPayload(t, provider, subject, c.kind, tc, now)
				if err != nil || facts == 0 {
					t.Fatalf("%s: facts=%d err=%v", c.kind, facts, err)
				}
				if first == nil {
					first = payload
					continue
				}
				if !bytes.Equal(first, payload) {
					var diffs []string
					diffJSON("$", jsonAny(t, first), jsonAny(t, payload), &diffs)
					t.Errorf("%s: order %d noise %g: client input differs from the first read:\n  %v", c.kind, index, noise, diffs)
				}
			}
		}
	}
}

// permutedFour maps a six-row order onto the four repository rows of the mix
// fixture, keeping its relative order.
func permutedFour(order []int) []int {
	out := make([]int, 0, 4)
	for _, index := range order {
		if index < 4 {
			out = append(out, index)
		}
	}
	return out
}

func jsonAny(t *testing.T, raw []byte) any {
	t.Helper()
	var out any
	if err := jsonUnmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", fmt.Sprint(err))
	}
	return out
}

func jsonUnmarshal(raw []byte, out any) error { return json.Unmarshal(raw, out) }

// TestProjectReadinessBreakdownIsStableOverRowOrder serves the rows of a
// project rollup (one per owning team and work scope) in three orders: the
// breakdown table and the evidence list follow the rows, so they must not.
// The table is not part of the client input, so the fact itself is compared.
func TestProjectReadinessBreakdownIsStableOverRowOrder(t *testing.T) {
	subject := projectSubject("linear", "proj-1")
	rows := [][]any{
		readinessProjectRollupRow("linear", "proj-1", "team-1", "Team One", "scope-d", "linear", 18, 2, 20, 0.9),
		readinessProjectRollupRow("linear", "proj-1", "team-2", "Team Two", "scope-c", "gitlab", 5, 15, 20, 0.25),
		readinessProjectRollupRow("linear", "proj-1", "team-2", "Team Two", "scope-b", "gitlab", 1, 9, 10, 0.1),
		readinessProjectRollupRow("linear", "proj-1", "team-3", "Team Three", "scope-a", "github", 0, 4, 4, 0),
	}
	var first []byte
	for index, order := range [][]int{{0, 1, 2, 3}, {3, 2, 1, 0}, {2, 0, 3, 1}} {
		client := &fakeClient{tables: []fakeTable{{match: readinessOriginalQueryMatch, rows: permuted(rows, order)}}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactReadiness)
		result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
			Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
			Kind: contextfabric.FactReadiness, Subjects: []contextfabric.SubjectRef{subject},
		})
		if err != nil || len(result.Facts) != 1 {
			t.Fatalf("facts=%d err=%v", len(result.Facts), err)
		}
		encoded, err := json.Marshal(result.Facts)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = encoded
			continue
		}
		if !bytes.Equal(first, encoded) {
			t.Fatalf("order %d: the project readiness fact differs from the first read:\n%s\n%s", index, first, encoded)
		}
	}
}

// TestRepositoryMixClientInputIsStableOverAggregationNoise serves one
// repository's effort sums with the last-digit difference two ClickHouse
// sums of the same rows can carry.
func TestRepositoryMixClientInputIsStableOverAggregationNoise(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	canonicalID, rowID := clockTestSubjectID(contextfabric.SubjectRepository)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: canonicalID, Label: "r1"}
	tc := clockTestCases()[1]
	var first []byte
	for _, noise := range []float64{0, 3e-16, -3e-16, 1e-15} {
		client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: [][]any{{uint8(0), rowID, map[string]float64{
			"feature_delivery": 0.48898883728007437 * (1 + noise), "operational": 0.047865254810142 * (1 - 2*noise),
			"maintenance": 0.2304889002988043 * (1 + 3*noise), "quality": 0.1388265562681117 * (1 - 4*noise), "risk": 0.0936 * (1 + 5*noise),
		}, 0.0864588925764 * (1 - 7*noise), uint64(3)}}}}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		payload, facts, err := clockTestPayload(t, provider, subject, contextfabric.FactInvestment, tc, now)
		if err != nil || facts == 0 {
			t.Fatalf("facts=%d err=%v", facts, err)
		}
		if first == nil {
			first = payload
			continue
		}
		if !bytes.Equal(first, payload) {
			var diffs []string
			diffJSON("$", jsonAny(t, first), jsonAny(t, payload), &diffs)
			t.Fatalf("noise %g: client input differs from the noise-free read:\n  %v", noise, diffs)
		}
	}
}
