package devhealthfacts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
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
func teamMixClient(order []int, noise float64, windows uint8) *fakeClient {
	repos := []string{"repo-a", "repo-b", "repo-c", "repo-d"}
	efforts := []float64{0.1, 0.2, 0.3, 0.7}
	owned := make([][]any, 0, len(repos))
	mix := make([][]any, 0, len(repos))
	for index, repo := range repos {
		owned = append(owned, []any{"CHAOS", repo})
		scale := efforts[index] * (1 + noise)
		for window := uint8(0); window < windows; window++ {
			mix = append(mix, []any{window, repo, map[string]float64{
				"feature_delivery": 0.48898883728007437 * scale * (1 + 0.1*float64(window)), "operational": 0.047865254810142 * scale * (1 - 0.2*float64(window)),
				"maintenance": 0.2304889002988043 * scale, "quality": 0.1388265562681117 * scale, "risk": 0.0936 * scale * (1 + 0.3*float64(window)),
			}, 0.0864588925764 * efforts[index] * (1 - 7*noise), uint64(3), "2026-01-01 00:00:00.000000"})
		}
	}
	mixOrder := make([]int, len(mix))
	for i := range mixOrder {
		mixOrder[i] = i
	}
	sort.SliceStable(mixOrder, func(a, b int) bool { return order[mixOrder[a]%len(order)] < order[mixOrder[b]%len(order)] })
	ownedOrder := make([]int, 0, len(owned))
	for _, index := range order {
		if index < len(owned) {
			ownedOrder = append(ownedOrder, index)
		}
	}
	return &fakeClient{tables: []fakeTable{
		{match: "FROM team_repo_ownership", rows: permuted(owned, ownedOrder)},
		{match: "FROM work_unit_investments", rows: permuted(mix, mixOrder)},
	}}
}

// TestTeamFactClientInputIsStableOverRowOrderAndAggregationNoise is the prod
// shape: two reads of the same team question's facts over the same static
// rows, served in different orders, must carry byte-equal client input.
func TestTeamFactClientInputIsStableOverRowOrderAndAggregationNoise(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	subject := teamSubject("CHAOS")
	orders_tc := clockTestCases()
	orders := [][]int{{0, 1, 2, 3, 4, 5}, {5, 4, 3, 2, 1, 0}, {2, 0, 5, 1, 4, 3}}
	cases := []struct {
		kind contextfabric.FactKind
		make func(order []int, noise float64) *fakeClient
	}{
		{contextfabric.FactReadiness, func(order []int, _ float64) *fakeClient { return teamReadinessClient(order) }},
		{contextfabric.FactInvestment, func(order []int, noise float64) *fakeClient {
			return teamMixClient(order, noise, 1)
		}},
	}
	for _, c := range cases {
		var first []byte
		for index, order := range orders {
			for _, noise := range []float64{0, 3e-16} {
				window := 1
				provider := findProvider(t, devhealthfacts.NewProviders(c.make(order, noise)), c.kind)
				payload, facts, err := clockTestPayload(t, provider, subject, c.kind, orders_tc[window], now)
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
		}, 0.0864588925764 * (1 - 7*noise), uint64(3), "2026-01-01 00:00:00.000000"}}}}}
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

// TestTeamMixPriorWindowSharesAreStableOverRowOrderAndNoise reads the team
// mix over an explicit range, which also reads the prior window and serves
// its shares as prior_theme_* fields.
func TestTeamMixPriorWindowSharesAreStableOverRowOrderAndNoise(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	var first []byte
	for _, order := range [][]int{{0, 1, 2, 3, 4, 5}, {5, 4, 3, 2, 1, 0}} {
		for _, noise := range []float64{0, 3e-16, -4e-16} {
			provider := findProvider(t, devhealthfacts.NewProviders(teamMixClient(order, noise, 2)), contextfabric.FactInvestment)
			result, err := provider.ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
				Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
				Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("CHAOS")},
			})
			if err != nil || len(result.Facts) != 1 {
				t.Fatalf("facts=%d err=%v", len(result.Facts), err)
			}
			if _, ok := result.Facts[0].Fields[contextfabric.FactFieldPriorTheme(contextfabric.ThemeFeatureDelivery)]; !ok {
				t.Fatalf("the fact carries no prior-window share: %v", result.Facts[0].Fields)
			}
			fields := map[string]contextfabric.FactValue{}
			for name, value := range result.Facts[0].Fields {
				if value.Table == nil {
					fields[name] = value
				}
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if first == nil {
				first = encoded
				continue
			}
			if !bytes.Equal(first, encoded) {
				t.Fatalf("order %v noise %g: scalar fields differ:\n%s\n%s", order, noise, first, encoded)
			}
		}
	}
}

func payloadInvestmentNumbers(t *testing.T, payload []byte) (map[string]float64, string) {
	t.Helper()
	var decoded struct {
		Facts []struct {
			Kind   string `json:"kind"`
			Fields map[string]struct {
				Number *float64 `json:"number"`
			} `json:"fields"`
		} `json:"canonical_facts"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, fact := range decoded.Facts {
		if fact.Kind != string(contextfabric.FactInvestment) {
			continue
		}
		for name, value := range fact.Fields {
			if value.Number != nil {
				out[name] = *value.Number
			}
		}
	}
	return out, string(payload)
}

// TestInvestmentClientInputCarriesOnlyRoundedSharesAndNoTables pins what the
// rounding relies on: the theme_breakdown table (weighted_effort, exact) is
// not in the client input, and every float the input does carry for an
// investment fact is already at the declared precision. A float field added
// to the client input later without the rounding fails here.
func TestInvestmentClientInputCarriesOnlyRoundedSharesAndNoTables(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tc := clockTestCases()[1]
	canonicalID, rowID := clockTestSubjectID(contextfabric.SubjectRepository)
	repository := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: canonicalID, Label: "r1"}
	subjects := map[string]struct {
		subject contextfabric.SubjectRef
		client  *fakeClient
	}{
		"repository": {repository, &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: [][]any{{uint8(0), rowID, map[string]float64{
			"feature_delivery": 0.48898883728007437, "operational": 0.047865254810142, "maintenance": 0.2304889002988043, "quality": 0.1388265562681117, "risk": 0.0936,
		}, 0.0864588925764, uint64(3), "2026-01-01 00:00:00.000000"}}}}}},
		"team": {teamSubject("CHAOS"), teamMixClient([]int{0, 1, 2, 3, 4, 5}, 0, 1)},
	}
	for name, c := range subjects {
		provider := findProvider(t, devhealthfacts.NewProviders(c.client), contextfabric.FactInvestment)
		payload, facts, err := clockTestPayload(t, provider, c.subject, contextfabric.FactInvestment, tc, now)
		if err != nil || facts == 0 {
			t.Fatalf("%s: facts=%d err=%v", name, facts, err)
		}
		numbers, raw := payloadInvestmentNumbers(t, payload)
		if strings.Contains(raw, "theme_breakdown") || strings.Contains(raw, "weighted_effort") {
			t.Fatalf("%s: the client input carries the theme breakdown table", name)
		}
		if len(numbers) == 0 {
			t.Fatalf("%s: the client input carries no investment number", name)
		}
		for field, value := range numbers {
			rounded, perr := strconv.ParseFloat(strconv.FormatFloat(value, 'g', 11, 64), 64)
			if perr != nil || rounded != value {
				t.Errorf("%s: %s = %v is not at the declared precision of 11 significant digits", name, field, value)
			}
		}
	}
}

// TestAShareAtARoundingEdgeCanStillFlipTheDigest names the residual: a
// repository value that sits exactly at a rounding edge changes its served
// digits when the aggregate noise crosses the edge, so two calls then carry
// two inputs (the write-back answers 409 input_changed once, and the client
// repeats call 1). The value served stays within 5e-11 relative of the exact.
func TestAShareAtARoundingEdgeCanStillFlipTheDigest(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	tc := clockTestCases()[1]
	canonicalID, rowID := clockTestSubjectID(contextfabric.SubjectRepository)
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: canonicalID, Label: "r1"}
	// feature_delivery share = 0.5 + 5e-11 * 0.5 exactly at the eleventh digit
	// edge when the total is 1: efforts 0.50000000000500 versus the same plus
	// one part in 1e15.
	read := func(edge float64) []byte {
		client := &fakeClient{tables: []fakeTable{{match: "FROM work_unit_investments", rows: [][]any{{uint8(0), rowID, map[string]float64{
			"feature_delivery": edge, "operational": 1 - edge, "maintenance": 0, "quality": 0, "risk": 0,
		}, 0.0, uint64(3), "2026-01-01 00:00:00.000000"}}}}}
		provider := findProvider(t, devhealthfacts.NewProviders(client), contextfabric.FactInvestment)
		payload, _, err := clockTestPayload(t, provider, subject, contextfabric.FactInvestment, tc, now)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	below, above := 0.500000000005-1e-15, 0.500000000005+1e-15
	if bytes.Equal(read(below), read(above)) {
		t.Fatal("a value on either side of a rounding edge gave one digest: the residual the change names is not reachable")
	}
	numbersBelow, _ := payloadInvestmentNumbers(t, read(below))
	exact := numbersBelow["theme_feature_delivery"]
	if math.Abs(exact-below)/below > 5.0001e-11 {
		t.Fatalf("served share %v is not within 5e-11 relative of %v", exact, below)
	}
}
