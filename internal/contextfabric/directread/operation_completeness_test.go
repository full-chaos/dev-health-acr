package directread

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVerdictRows(t *testing.T) {
	cat := loadDefault(t)
	get := func(n string) *OperationPolicy { op, _ := cat.Lookup(n); return op }
	vars := func(s string) map[string]any {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, tc := range []struct {
		name string
		op   string
		data string
		vars string
		want CompletenessVerdict
	}{
		{"edges degraded", "workGraphEdges", `{"workGraphEdges":{"degradedReason":"X","edges":[]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"edges clean", "workGraphEdges", `{"workGraphEdges":{"degradedReason":null,"edges":[{}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"edges at cap", "workGraphEdges", `{"workGraphEdges":{"degradedReason":null,"edges":[{},{}]}}`, `{"filters":{"limit":2}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonBounded}},
		{"edges default cap", "workGraphEdges", `{"workGraphEdges":{"degradedReason":null,"edges":[{}]}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"edges field not selected", "workGraphEdges", `{"workGraphEdges":{"edges":[{}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessUnknown, ReasonDisclosureAbsent}},
		{"forecast short history", "throughputForecast", `{"throughputForecast":{"insufficientHistory":true}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonInsufficientHistory}},
		{"forecast ok", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false,"backlogState":"MEASURED","degradedReason":null}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"forecast supplied backlog", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false,"backlogState":"SUPPLIED","degradedReason":null}}`, `{"input":{"backlogSize":40}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"forecast stale backlog", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false,"backlogState":"STALE","degradedReason":"STALE"}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"forecast no backlog rows", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false,"backlogState":"NO_ROWS","degradedReason":"NO_ROWS"}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"forecast short history and stale backlog", "throughputForecast", `{"throughputForecast":{"insufficientHistory":true,"degradedReason":"STALE"}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonInsufficientHistory}},
		{"forecast degradedReason not selected", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false}}`, `{}`, CompletenessVerdict{CompletenessUnknown, ReasonDisclosureAbsent}},
		{"capacity stale backlog", "capacityForecast", `{"capacityForecast":{"backlogState":"STALE"}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"capacity no backlog rows", "capacityForecast", `{"capacityForecast":{"backlogState":"NO_ROWS"}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"capacity measured backlog", "capacityForecast", `{"capacityForecast":{"backlogState":"MEASURED"}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacity null state", "capacityForecast", `{"capacityForecast":{"backlogState":null}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacity null forecast", "capacityForecast", `{"capacityForecast":null}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacity state not selected", "capacityForecast", `{"capacityForecast":{"forecastId":"f"}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacities state not selected", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"forecastId":"f"}}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacities stale first edge", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"backlogState":"STALE"}}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"capacities stale second edge", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"backlogState":"MEASURED"}},{"node":{"backlogState":"STALE"}}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"capacities no rows second edge", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"backlogState":null}},{"node":{"backlogState":"NO_ROWS"}}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"capacities measured and null", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"backlogState":"MEASURED"}},{"node":{"backlogState":null}}]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacities empty edges", "capacityForecasts", `{"capacityForecasts":{"edges":[]}}`, `{"filters":{"limit":5}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"capacities null list", "capacityForecasts", `{"capacityForecasts":null}`, `{"filters":{"limit":5}}`, CompletenessVerdict{CompletenessUnknown, ReasonDisclosureAbsent}},
		{"capacities stale and at cap", "capacityForecasts", `{"capacityForecasts":{"edges":[{"node":{"backlogState":"STALE"}}]}}`, `{"filters":{"limit":1}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonDegraded}},
		{"full coverage low", "investmentFull", `{"analytics":{"sankey":{"coverage":{"teamCoverage":0.4,"repoCoverage":1}},"breakdowns":[]}}`, `{}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonCoverageBelowOne}},
		{"full sankey null", "investmentFull", `{"analytics":{"sankey":null}}`, `{}`, CompletenessVerdict{CompletenessUnknown, ReasonDisclosureAbsent}},
		{"hotspots no limit sent", "hotspots", `{"hotspots":{"rows":[{}]}}`, `{}`, CompletenessVerdict{CompletenessUnknown, ReasonLimitNotSent}},
		{"hotspots below limit", "hotspots", `{"hotspots":{"rows":[{}]}}`, `{"input":{"limit":3}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"hotspots at limit", "hotspots", `{"hotspots":{"rows":[{},{},{}]}}`, `{"input":{"limit":3}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonBounded}},
		{"breakdown below topN", "investmentBreakdown", `{"analytics":{"breakdowns":[{"items":[{}]},{"items":[{}]}]}}`, `{"batch":{"breakdowns":[{"topN":5},{"topN":5}]}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"breakdown second at topN", "investmentBreakdown", `{"analytics":{"breakdowns":[{"items":[{}]},{"items":[{},{}]}]}}`, `{"batch":{"breakdowns":[{"topN":5},{"topN":2}]}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonBounded}},
		{"breakdown default topN", "investmentBreakdown", `{"analytics":{"breakdowns":[{"items":[{}]}]}}`, `{"batch":{"breakdowns":[{}]}}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"breakdown default topN reached", "investmentBreakdown", `{"analytics":{"breakdowns":[{"items":[{},{},{},{},{},{},{},{},{},{}]}]}}`, `{"batch":{"breakdowns":[{}]}}`, CompletenessVerdict{CompletenessDeclaredPartial, ReasonBounded}},
		{"home full", "home", `{"home":{}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
		{"compoundingRisk full", "compoundingRisk", `{"compoundingRisk":{"rows":[]}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := get(tc.op).Verdict([]byte(tc.data), vars(tc.vars)); got != tc.want {
				t.Fatalf("%s: got %+v want %+v", tc.op, got, tc.want)
			}
		})
	}
}

// Every served operation has a completeness basis, and every cap names a real
// client variable and a real output list. A new operation fails here until it
// is classified.
func TestEveryOperationHasACompletenessBasis(t *testing.T) {
	cat := loadDefault(t)
	names := make([]string, 0, len(cat.byName))
	for n := range cat.byName {
		names = append(names, n)
	}
	if len(names) < 19 {
		t.Fatalf("catalogue lists %d operations, want at least 19", len(names))
	}
	seen := map[string]bool{}
	for _, n := range names {
		seen[n] = true
		op, _ := cat.Lookup(n)
		caps, ok := operationCaps[n]
		if !ok {
			t.Errorf("%s has no completeness basis in operationCaps", n)
			continue
		}
		for _, c := range caps {
			rule, ok := op.Variable(c.VarPath)
			if !ok || !rule.Allowed || rule.Source != SourceClient {
				t.Errorf("%s cap %q is not a client variable", n, c.VarPath)
			}
			found := false
			for _, out := range op.Outputs {
				if strings.HasPrefix(out.Path, c.ListPath+"[*]") || strings.HasPrefix(out.Path, c.ListPath+".") {
					found = true
				}
			}
			if !found {
				t.Errorf("%s cap list %q is not an output path", n, c.ListPath)
			}
		}
		if len(caps) == 0 {
			for _, v := range op.Variables {
				if v.Allowed && v.Source == SourceClient && (strings.HasSuffix(v.Path, "limit") || strings.HasSuffix(v.Path, "topN")) {
					t.Errorf("%s accepts %q but declares no cap", n, v.Path)
				}
			}
		}
	}
	for n := range operationCaps {
		if !seen[n] {
			t.Errorf("operationCaps names %q, not a served operation", n)
		}
	}
	if len(CompletenessReasonVocabulary()) != 7 {
		t.Error("reason vocabulary changed")
	}
}

func TestJoinVerdictsPartialBeatsUnknownBeatsComplete(t *testing.T) {
	c := CompletenessVerdict{State: CompletenessDeclaredComplete}
	u := CompletenessVerdict{CompletenessUnknown, ReasonDisclosureAbsent}
	p := CompletenessVerdict{CompletenessDeclaredPartial, ReasonBounded}
	for _, tc := range []struct {
		name string
		seq  []CompletenessVerdict
		want CompletenessVerdict
	}{
		{"complete only", []CompletenessVerdict{c, c}, c},
		{"unknown after complete", []CompletenessVerdict{c, u}, u},
		{"unknown before complete", []CompletenessVerdict{u, c}, u},
		{"partial after unknown", []CompletenessVerdict{u, p}, p},
		{"partial before complete", []CompletenessVerdict{p, c}, p},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var acc *CompletenessVerdict
			for _, v := range tc.seq {
				acc = joinVerdicts(acc, v)
			}
			if *acc != tc.want {
				t.Fatalf("got %+v want %+v", *acc, tc.want)
			}
		})
	}
}

func TestVerdictIndexedCapAndRatioRows(t *testing.T) {
	cat := loadDefault(t)
	ib, _ := cat.Lookup("investmentBreakdown")
	full, _ := cat.Lookup("investmentFull")
	dec := func(s string) map[string]any {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	var got CompletenessVerdict
	got = ib.Verdict([]byte(`{"analytics":{"breakdowns":[{"dimension":"THEME"}]}}`), dec(`{}`))
	if got.State != CompletenessUnknown || got.Reason != ReasonDisclosureAbsent {
		t.Fatalf("items not selected: %+v", got)
	}
	// ratio exactly one is complete, just below one is partial.
	body := func(r string) []byte {
		return []byte(`{"analytics":{"sankey":{"coverage":{"teamCoverage":` + r + `,"repoCoverage":1}},"breakdowns":[]}}`)
	}
	if got := full.Verdict(body("1"), dec(`{}`)); got.State != CompletenessDeclaredComplete {
		t.Fatalf("coverage 1: %+v", got)
	}
	if got := full.Verdict(body("0.99"), dec(`{}`)); got.State != CompletenessDeclaredPartial || got.Reason != ReasonCoverageBelowOne {
		t.Fatalf("coverage 0.99: %+v", got)
	}
}

// Every served operation whose document selects a backlogState field has a verdict rule for it:
// a newestDayStatePaths row (set on its policy row at load), or a named exemption whose own
// disclosure field says the same thing. A document that starts selecting backlogState fails here.
func TestEveryBacklogStateOperationHasAVerdictRule(t *testing.T) {
	exempt := map[string]string{"throughputForecast": "throughputForecast.degradedReason"}
	cat := loadDefault(t)
	carriers := 0
	for name, op := range cat.byName {
		carries := false
		for _, out := range op.Outputs {
			carries = carries || strings.HasSuffix(out.Path, ".backlogState")
		}
		if !carries {
			if op.newestDayStatePath != "" {
				t.Errorf("%s has a newest day state rule but selects no backlogState", name)
			}
			continue
		}
		carriers++
		if disclosure, ok := exempt[name]; ok {
			found := false
			for _, d := range op.Disclosure {
				found = found || d.Path == disclosure
			}
			if !found || op.newestDayStatePath != "" {
				t.Errorf("%s is exempt through %s, which its policy row does not carry", name, disclosure)
			}
			continue
		}
		if op.newestDayStatePath == "" {
			t.Errorf("%s selects backlogState and has neither a newestDayStatePaths row nor a named exemption", name)
		}
	}
	if carriers != 3 {
		t.Errorf("%d operations select backlogState, want capacityForecast, capacityForecasts, throughputForecast", carriers)
	}
}
