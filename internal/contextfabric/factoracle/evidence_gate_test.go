package factoracle

import (
	"encoding/json"
	"math"
	"testing"
)

// recordedOrgThemeReply is the recorded ops reply of the ORG investmentBreakdown
// case on THEME and COUNT: the theme values and the V2 uncategorizedCount.
func recordedOrgThemeReply(t *testing.T) (map[string]float64, int64) {
	t.Helper()
	manifest, recording, _, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	shapes := mustShapes(t)
	for _, c := range manifest.ShapeCases {
		if c.ShapeID != "analytics/investmentBreakdown/all" {
			continue
		}
		batch, _ := c.Variables["batch"].(map[string]any)
		breakdowns, _ := batch["breakdowns"].([]any)
		if len(breakdowns) != 1 {
			continue
		}
		breakdown, _ := breakdowns[0].(map[string]any)
		if breakdown["dimension"] != "THEME" || breakdown["measure"] != "COUNT" {
			continue
		}
		shape, ok := ShapeByID(shapes, c.ShapeID)
		if !ok {
			t.Fatalf("no shape %s", c.ShapeID)
		}
		reply, ok := recording.Replies[CaseKey(shape, c.Variables)]
		if !ok {
			t.Fatalf("the THEME COUNT case has no recorded reply")
		}
		var data struct {
			Analytics struct {
				Breakdowns []struct {
					Items []struct {
						Key   string  `json:"key"`
						Value float64 `json:"value"`
					} `json:"items"`
				} `json:"breakdowns"`
				EvidenceQualityStats struct {
					UncategorizedCount *int64 `json:"uncategorizedCount"`
				} `json:"evidenceQualityStats"`
			} `json:"analytics"`
		}
		if err := json.Unmarshal(reply.Data, &data); err != nil {
			t.Fatal(err)
		}
		if len(data.Analytics.Breakdowns) != 1 || data.Analytics.EvidenceQualityStats.UncategorizedCount == nil {
			t.Fatalf("the recorded reply holds %d breakdowns and uncategorizedCount %v, want one breakdown and the V2 count", len(data.Analytics.Breakdowns), data.Analytics.EvidenceQualityStats.UncategorizedCount)
		}
		themes := map[string]float64{}
		for _, item := range data.Analytics.Breakdowns[0].Items {
			themes[item.Key] = item.Value
		}
		return themes, *data.Analytics.EvidenceQualityStats.UncategorizedCount
	}
	t.Fatal("the capture holds no ORG investmentBreakdown case on THEME and COUNT")
	return nil, 0
}

// CHAOS-12808: ops's V2 investmentBreakdown (0b763a85) and acr's investment
// reader (#1666) both leave evidence-gated work units out of the theme mix
// (status insufficient_evidence or no_text_sources: a fixed uniform prior, not
// a measured split), and ops counts them in uncategorizedCount. The store
// reading follows the same gate, so on the recorded capture its ORG theme
// effort is the recorded ops answer, theme by theme, and the units it gates
// are the units ops counts as uncategorized.
func TestTheStoreReadingLeavesEvidenceGatedUnitsOutAsOpsDoes(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	ops, uncategorized := recordedOrgThemeReply(t)
	fromStore := store.OrgThemeEffort(manifest.Window)
	if len(ops) == 0 || len(fromStore) != len(ops) {
		t.Fatalf("ops answered %d themes, the store gives %d", len(ops), len(fromStore))
	}
	for theme, want := range ops {
		if got := fromStore[theme]; math.Abs(got-want) > themeTolerance(got, want) {
			t.Errorf("theme %s: the store rows give %v, ops answered %v (off by %v)", theme, got, want, got-want)
		}
	}
	if uncategorized == 0 {
		t.Fatal("ops counts no uncategorized unit in the capture: this test measures nothing")
	}
	if got := store.GatedUnits(manifest.Window); got != uncategorized {
		t.Errorf("the store gates %d units in scope and window, ops counts %d uncategorized", got, uncategorized)
	}
}

// The gate statuses are ops's CategorizedOnlySQL list exactly: the two
// evidence-gated statuses, and no other (a model failure is not the gate).
func TestTheEvidenceGateIsOpsCategorizedOnlyList(t *testing.T) {
	for status, want := range map[string]bool{
		"insufficient_evidence": true, "no_text_sources": true,
		"ok": false, "repaired": false, "invalid_llm_output": false, "": false,
	} {
		if got := (unitGeneration{status: status}).gated(); got != want {
			t.Errorf("status %q gated = %v, want %v", status, got, want)
		}
	}
}

// A gated unit is out of every reading: taking the gated units' rows out of
// the capture changes no reading of the store, while taking out as many
// categorized units does.
func TestAnEvidenceGatedUnitIsOutOfEveryReading(t *testing.T) {
	manifest, _, extract, err := LoadCapture(captureDir)
	if err != nil {
		t.Fatal(err)
	}
	full, err := NewStore(extract)
	if err != nil {
		t.Fatal(err)
	}
	gated, categorized := map[string]bool{}, map[string]bool{}
	// Every gated unit of the window, in scope or not: the witnesses read the
	// units outside the scope too.
	for id, unit := range full.units {
		if !manifest.Window.holds(unit.latest) {
			continue
		}
		if unit.latest.gated() {
			gated[id] = true
		} else if full.inScope(id) && len(categorized) < 50 {
			categorized[id] = true
		}
	}
	if len(gated) == 0 || len(categorized) == 0 {
		t.Fatalf("the capture holds %d gated and %d categorized units in scope: this test measures nothing", len(gated), len(categorized))
	}
	without := func(drop map[string]bool) *Store {
		t.Helper()
		trimmed := *extract
		trimmed.Tables = map[string][]Row{}
		for table, rows := range extract.Tables {
			trimmed.Tables[table] = rows
		}
		var kept []Row
		for _, row := range extract.Tables[tableWorkUnitInvestments] {
			if id, _ := rowString(row, "work_unit_id"); !drop[id] {
				kept = append(kept, row)
			}
		}
		trimmed.Tables[tableWorkUnitInvestments] = kept
		store, err := NewStore(&trimmed)
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	readings := func(s *Store) map[string]any {
		return map[string]any{
			"org":        s.OrgThemeEffort(manifest.Window),
			"repository": s.ExpectedRepositoryEffort(manifest.Window),
			"witnesses":  s.Witnesses(manifest.Window),
		}
	}
	base := readings(full)
	noGated := readings(without(gated))
	noCategorized := readings(without(categorized))
	for name := range base {
		if !sameReading(base[name], noGated[name]) {
			t.Errorf("reading %s changes when the %d gated units are taken out: a gated unit reached it", name, len(gated))
		}
	}
	if sameReading(base["org"], noCategorized["org"]) || sameReading(base["repository"], noCategorized["repository"]) {
		t.Fatal("taking out categorized units changes no reading: the comparison cannot see a unit")
	}
}

// sameReading compares two readings value by value, within the sum tolerance.
func sameReading(a, b any) bool {
	flat := func(v any) map[string]float64 {
		out := map[string]float64{}
		switch x := v.(type) {
		case map[string]float64:
			for k, f := range x {
				out[k] = f
			}
		case map[string]map[string]float64:
			for k, m := range x {
				for k2, f := range m {
					out[k+"/"+k2] = f
				}
			}
		case Witnesses:
			for c, m := range x {
				for k2, f := range m {
					out[string(c)+"/"+k2] = f
				}
			}
		}
		return out
	}
	fa, fb := flat(a), flat(b)
	for k, va := range fa {
		if math.Abs(va-fb[k]) > sumTolerance(va, fb[k]) {
			return false
		}
	}
	for k, vb := range fb {
		if math.Abs(vb-fa[k]) > sumTolerance(vb, fa[k]) {
			return false
		}
	}
	return true
}
