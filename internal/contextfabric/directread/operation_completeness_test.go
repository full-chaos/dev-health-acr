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
		{"forecast ok", "throughputForecast", `{"throughputForecast":{"insufficientHistory":false}}`, `{}`, CompletenessVerdict{State: CompletenessDeclaredComplete}},
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
