package factoracle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

// orgThemeAnswer is an ORG investmentBreakdown THEME/COUNT reply with the
// given theme values and V2 uncategorizedCount.
func orgThemeAnswer(t *testing.T, themes map[string]float64, uncategorized any) json.RawMessage {
	t.Helper()
	items := make([]any, 0, len(themes))
	for _, key := range sortedFloatKeys(themes) {
		items = append(items, map[string]any{"__typename": "BreakdownItem", "key": key, "value": themes[key]})
	}
	stats := map[string]any{"__typename": "EvidenceQualityStats", "mean": 0.5, "stddev": 0.2, "total": 6790, "bandCounts": map[string]any{"low": 1}}
	if uncategorized != absent {
		stats["uncategorizedCount"] = uncategorized
	}
	data, err := json.Marshal(map[string]any{"analytics": map[string]any{
		"__typename":           "AnalyticsResult",
		"breakdowns":           []any{map[string]any{"__typename": "Breakdown", "dimension": "THEME", "measure": "COUNT", "items": items}},
		"evidenceQualityStats": stats,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return servedAnswer(t, "analytics", "investmentBreakdown", string(data), len(data), 32768)
}

// absent leaves uncategorizedCount out of the reply.
const absent = "<absent>"

func sortedFloatKeys(m map[string]float64) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	return sortedKeys(set)
}

// compareOrgReply runs root analytics' ORG comparison on the committed
// capture's store with the given reply. The acr plane is stopped before it
// answers: only the ops reply against the store is measured here.
func compareOrgReply(t *testing.T, reply json.RawMessage) *RootReport {
	t.Helper()
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("acr plane not called in this test")
	// The answer caches Run sets up (oracle.go: Run).
	oracle := &Oracle{Policy: mustPolicy(t), Store: store, Window: manifest.Window, shapes: mustShapes(t),
		answers: map[string]json.RawMessage{}, opAnswers: map[string]json.RawMessage{},
		Planes: fakePlanes{
			graphQL: func(Shape, map[string]any) (json.RawMessage, error) { return reply, nil },
			facts:   func(FactsRequest) (json.RawMessage, error) { return nil, stop },
		}}
	rr := &RootReport{Root: "analytics"}
	if err := compareInvestment(context.Background(), oracle, rr); err != nil && !errors.Is(err, stop) {
		t.Fatalf("compareInvestment: %v", err)
	}
	return rr
}

// CHAOS-12836: the O4 run compares ops V2's uncategorizedCount with the
// units the store reading gates (CHAOS-12808). A reply whose theme values are
// right but whose count is off is a finding; the committed capture's own
// count (1083) is a match, and the path is compared, not left unmeasured.
func TestTheRunComparesTheUncategorizedCountWithTheStoreGate(t *testing.T) {
	ops, uncategorized := recordedOrgThemeReply(t)
	if uncategorized == 0 {
		t.Fatal("the capture counts no uncategorized unit: this test measures nothing")
	}
	finding := func(rr *RootReport) *Finding {
		for i := range rr.Findings {
			if rr.Findings[i].Pair == "ops_reply_vs_store" && rr.Findings[i].Key == "uncategorizedCount" {
				return &rr.Findings[i]
			}
		}
		return nil
	}

	same := compareOrgReply(t, orgThemeAnswer(t, ops, uncategorized))
	if f := finding(same); f != nil {
		t.Fatalf("the capture's own count is a finding: %s", f.Detail)
	}
	if same.touched["analytics.evidenceQualityStats.uncategorizedCount"] == 0 {
		t.Fatal("the run did not compare analytics.evidenceQualityStats.uncategorizedCount")
	}

	off := compareOrgReply(t, orgThemeAnswer(t, ops, uncategorized+1))
	f := finding(off)
	if f == nil {
		t.Fatalf("a count one above the store's gated units is no finding: findings %+v", off.Findings)
	}
	if want := fmt.Sprintf("ops counts %d uncategorized units, the store gates %d", uncategorized+1, uncategorized); f.Detail != want {
		t.Fatalf("finding detail %q, want %q", f.Detail, want)
	}

	// No count in the reply is a finding, never a silent pass.
	for name, c := range map[string]struct {
		value any
		want  string
	}{
		"absent":     {absent, "the ops answer holds no uncategorizedCount"},
		"null":       {nil, "the ops answer holds no uncategorizedCount"},
		"a string":   {"1083", "the ops uncategorizedCount is not an integer"},
		"a fraction": {1083.5, "the ops uncategorizedCount is not an integer"},
	} {
		rr := compareOrgReply(t, orgThemeAnswer(t, ops, c.value))
		if f := finding(rr); f == nil || f.Detail != c.want {
			t.Errorf("count %s: finding %+v, want %q", name, f, c.want)
		}
	}
}
