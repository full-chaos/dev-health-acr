package directread_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// homeAnswer is a synthetic home answer shaped like the served one: eleven
// deltas with sparkPoints points each, signals ordered by severity, and the
// objects and lists a cut must never touch. caveatBytes pads the caveat lists.
func homeAnswer(sparkPoints, signals, caveatBytes int) map[string]any {
	deltas := make([]any, 11)
	for i := range deltas {
		spark := make([]any, sparkPoints)
		for p := range spark {
			spark[p] = map[string]any{"__typename": "SparkPoint", "ts": fmt.Sprintf("2026-10-%02dT00:00:00", p%28+1), "value": 12.345678901234567 + float64(p)}
		}
		deltas[i] = map[string]any{
			"__typename": "MetricDelta", "metric": fmt.Sprintf("metric_%d", i), "label": fmt.Sprintf("Metric %d", i),
			"value": 2.6251225490196073, "unit": "days", "deltaPct": -50.85783297416423, "hasData": true, "hasPriorData": true,
			"rateCoverage": nil, "rateState": nil, "repoFilterApplied": nil, "repoLinkBasis": nil, "repoLinkCoverage": nil,
			"repoLinkMultiRepoItems": nil, "repoLinkState": nil, "spark": spark,
		}
	}
	severities := []string{"critical", "high", "medium", "low"}
	rows := make([]any, signals)
	for i := range rows {
		rows[i] = map[string]any{
			"__typename": "HomeSignal", "id": fmt.Sprintf("signal:%02d", i), "title": fmt.Sprintf("Signal %d appears up", i),
			"metric": fmt.Sprintf("metric_%d", i), "currentValue": "2,671 hours", "priorValue": "498 hours", "delta": "+436%",
			"direction": "up", "severity": severities[i*len(severities)/signals], "confidence": "high", "affectedScope": "org",
			"evidenceCount": 14, "category": "delivery", "coverage": nil, "repoFilterApplied": nil, "scopeEntity": nil,
			"whyItMatters":      strings.Repeat("w", 160),
			"recommendedAction": strings.Repeat("a", 120),
			"evidenceRef":       "/api/v1/explain?metric=m&scope_type=org&scope_id=&range_days=14&compare_days=14",
			"attribution": map[string]any{
				"__typename": "SignalAttribution", "items": 60,
				"sources":    []any{map[string]any{"__typename": "SignalAttributionSourceCount", "source": "REPO_OWNERSHIP", "items": 25, "share": 0.4166666666666667}},
				"confidence": []any{map[string]any{"__typename": "SignalAttributionConfidenceCount", "confidence": "HIGH", "items": 53, "share": 0.8833333333333333}},
			},
		}
	}
	events := make([]any, 9)
	for i := range events {
		events[i] = map[string]any{"__typename": "EventItem", "ts": "2026-10-15T19:44:36.449711Z", "type": "regression",
			"text": "Metric shifted 82% over the last 14 days.", "link": "/api/v1/explain?metric=m&scope_type=org&scope_id=&range_days=14&compare_days=14"}
	}
	caveat := "caveat " + strings.Repeat("c", caveatBytes)
	return map[string]any{"home": map[string]any{
		"__typename":            "HomeResult",
		"deltas":                deltas,
		"signals":               rows,
		"events":                events,
		"summary":               []any{map[string]any{"__typename": "SummarySentence", "id": "s1", "text": "Metric rose 436%.", "evidenceLink": "/x"}},
		"tiles":                 []any{map[string]any{"__typename": "HomeTileEntry", "key": "measure", "value": map[string]any{"__typename": "HomeTile", "title": "Measure", "subtitle": "Coverage", "link": "/explore"}}},
		"freshness":             map[string]any{"__typename": "Freshness", "lastIngestedAt": "2026-10-15T15:29:07", "latestSuccessfulSyncAt": nil, "coverage": nil, "sources": []any{map[string]any{"__typename": "HomeFreshnessSource", "provider": "jira", "status": "error"}}},
		"constraint":            map[string]any{"__typename": "ConstraintCard", "title": "This week's constraint", "claim": "Blocked work rose.", "evidence": []any{map[string]any{"__typename": "ConstraintEvidence", "label": "Drill in", "link": "/x"}}, "experiments": []any{"Set WIP limits.", "Rebalance reviewers."}},
		"healthState":           map[string]any{"__typename": "HealthState", "status": "critical", "headline": "Blocked work appears up", "summary": "The strongest signal suggests blocked work.", "asOf": "2026-10-15T15:29:07"},
		"limitingFactor":        map[string]any{"__typename": "HomeLimitingFactor", "claim": "Blocked work appears to limit.", "whyItMatters": "w", "recommendedAction": "a", "confidence": "high", "evidenceRef": "/x"},
		"dataConfidence":        map[string]any{"__typename": "HomeDataConfidence", "level": "low", "coveragePct": 40, "connectedSources": []any{"github"}, "missingSources": []any{"jira"}, "caveats": []any{caveat}},
		"scopeDataConfidence":   map[string]any{"__typename": "HomeScopeDataConfidence", "level": "low", "coveragePct": 40, "lastIngestedAt": "2026-10-15T15:29:07", "caveats": []any{caveat}},
		"reworkThemeAllocation": []any{map[string]any{"__typename": "ReworkThemeAllocation", "theme": "quality", "label": "Quality", "allocation": 15.25, "allocationPct": 31.13, "prsMerged": 18, "churnLoc": 2624}},
		"filterEmptyReason":     nil,
	}}
}

func runHome(t *testing.T, answer map[string]any, maxBytes int) (directread.OperationResponse, int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"data": answer})
	if err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(answer)
	h := newOpHarness(t, func(opRecorded) (int, string) { return 200, string(body) }, opHarnessOptions{})
	resp, err := h.runner.Run(t.Context(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "home", Variables: json.RawMessage(`{}`), MaxBytes: maxBytes})
	if err != nil {
		t.Fatalf("Run(home): %v", err)
	}
	return resp, len(full)
}

// homePart is one field of the served home answer, re-marshalled canonically.
func homePart(t *testing.T, data json.RawMessage, field string) string {
	t.Helper()
	var root struct {
		Home map[string]json.RawMessage `json:"home"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode home: %v", err)
	}
	var v any
	if err := json.Unmarshal(root.Home[field], &v); err != nil {
		t.Fatalf("decode home.%s: %v", field, err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func homeSignalIDs(t *testing.T, data json.RawMessage) []string {
	t.Helper()
	var root struct {
		Home struct {
			Signals []struct {
				ID string `json:"id"`
			} `json:"signals"`
		} `json:"home"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("decode signals: %v", err)
	}
	ids := make([]string, len(root.Home.Signals))
	for i, s := range root.Home.Signals {
		ids[i] = s.ID
	}
	return ids
}

// The parts a cut must never touch: the objects, and the lists that say what
// the answer cannot see. A cut answer must never read healthier than the full one.
var homeKeptParts = []string{"healthState", "limitingFactor", "constraint", "dataConfidence", "scopeDataConfidence", "freshness"}

// Over the default budget, home is served cut on its signals: the strongest
// signals in served order, the rest left out and stated; deltas and every
// kept part whole.
func TestHomeOverTheDefaultBudgetIsCutOnItsSignals(t *testing.T) {
	answer := homeAnswer(14, 15, 0)
	full, _ := json.Marshal(answer)
	resp, fullBytes := runHome(t, answer, 0)
	if fullBytes <= directread.DefaultOperationMaxBytes {
		t.Fatalf("fixture is %d bytes, not over the default budget", fullBytes)
	}
	if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredPartial || resp.CompletenessReason != directread.ReasonPageCut {
		out, _ := json.Marshal(resp)
		t.Fatalf("want served, declared_partial, page_cut; got %.400s", out)
	}
	if !strings.Contains(resp.Page.Cut, "(home.signals)") || resp.Page.RowsRead != 15 || resp.Page.RowsReturned < 1 || resp.Page.RowsReturned >= 15 {
		t.Fatalf("page %+v", resp.Page)
	}
	if len(resp.Data) > directread.DefaultOperationMaxBytes {
		t.Fatalf("served %d bytes over the default budget", len(resp.Data))
	}
	got := homeSignalIDs(t, resp.Data)
	want := homeSignalIDs(t, full)[:resp.Page.RowsReturned]
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("signals %v, want the first %d in served order %v", got, len(want), want)
	}
	for _, part := range append([]string{"deltas", "events"}, homeKeptParts...) {
		if homePart(t, resp.Data, part) != homePart(t, full, part) {
			t.Errorf("home.%s changed by the cut", part)
		}
	}
}

// When not even one signal fits beside the other lists, the lists the
// catalogue does not keep (deltas here) are emptied and named; the kept parts
// stay whole.
func TestHomeEmptiesItsDeltasButNeverItsKeptLists(t *testing.T) {
	answer := homeAnswer(90, 15, 0)
	full, _ := json.Marshal(answer)
	resp, _ := runHome(t, answer, 0)
	if resp.Call != directread.CallServed || resp.Completeness != directread.CompletenessDeclaredPartial {
		out, _ := json.Marshal(resp)
		t.Fatalf("want served and declared_partial; got %.400s", out)
	}
	if !strings.Contains(resp.Page.Cut, "home.deltas") || !strings.Contains(resp.Page.Cut, "were left out") {
		t.Fatalf("cut %q does not name the emptied deltas", resp.Page.Cut)
	}
	if homePart(t, resp.Data, "deltas") != "[]" {
		t.Fatalf("deltas = %s, want emptied", homePart(t, resp.Data, "deltas"))
	}
	for _, part := range homeKeptParts {
		if homePart(t, resp.Data, part) != homePart(t, full, part) {
			t.Errorf("home.%s changed by the cut", part)
		}
	}
	for _, kept := range []string{"home.constraint", "home.dataConfidence", "home.scopeDataConfidence", "home.freshness"} {
		if strings.Contains(resp.Page.Cut, kept) {
			t.Errorf("cut %q names a kept list under %s", resp.Page.Cut, kept)
		}
	}
}

// A kept list is never emptied: when the kept lists alone overflow the
// budget, home is refused, never served without them.
func TestHomeIsRefusedWhenItsKeptListsAloneOverflow(t *testing.T) {
	resp, _ := runHome(t, homeAnswer(14, 15, 20000), 0)
	if resp.Call != directread.CallRefused || resp.Refusal == nil || resp.Refusal.Code != directread.RefusalResponseBudget {
		out, _ := json.Marshal(resp)
		t.Fatalf("want response_budget; got %.400s", out)
	}
}

// Blast radius of a declared primary list without a row-limit variable: such
// an operation is cut, where it was refused before. home is the only one.
func TestOnlyHomeDeclaresAPrimaryListWithoutALimitVariable(t *testing.T) {
	cat, err := directread.DefaultCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		if op.PrimaryListPath == "" {
			continue
		}
		limited := false
		for _, v := range op.Variables {
			if v.Allowed && v.Source == directread.SourceClient && v.Kind != "object" && (v.Path == "limit" || strings.HasSuffix(v.Path, ".limit")) {
				limited = true
			}
		}
		if !limited {
			got = append(got, op.Name+"="+op.PrimaryListPath)
		}
	}
	if strings.Join(got, ",") != "home=home.signals" {
		t.Fatalf("declared primary lists without a limit variable: %v, want [home=home.signals]", got)
	}
}

// homeCatalogueWith loads the shipped catalogue with home's primary and kept
// lists replaced.
func homeCatalogueWith(t *testing.T, primary string, kept []string) error {
	t.Helper()
	raw, err := os.ReadFile("operations.v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var file directread.CatalogueFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for i := range file.Operations {
		if file.Operations[i].Name == "home" {
			file.Operations[i].PrimaryListPath = primary
			file.Operations[i].KeptLists = kept
		}
	}
	out, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	_, err = directread.LoadCatalogue(out)
	return err
}

func TestAKeptListMustBeAnotherTopLevelListOfADeclaredPrimaryListOperation(t *testing.T) {
	for name, tc := range map[string]struct {
		primary string
		kept    []string
	}{
		"no primary list":   {"", []string{"home.constraint.evidence"}},
		"the primary list":  {"home.signals", []string{"home.signals"}},
		"not a list":        {"home.signals", []string{"home.healthState"}},
		"a list inside row": {"home.signals", []string{"home.deltas[*].spark"}},
		"listed twice":      {"home.signals", []string{"home.constraint.evidence", "home.constraint.evidence"}},
	} {
		if err := homeCatalogueWith(t, tc.primary, tc.kept); err == nil || !strings.Contains(err.Error(), "kept") {
			t.Errorf("%s: loaded (%v)", name, err)
		}
	}
	if err := homeCatalogueWith(t, "home.signals", []string{"home.constraint.evidence"}); err != nil {
		t.Fatalf("a valid kept list was refused: %v", err)
	}
}

func TestShippedHomeKeepsEveryListThatSaysWhatTheAnswerCannotSee(t *testing.T) {
	cat, _ := directread.DefaultCatalogue()
	op, _ := cat.Lookup("home")
	want := []string{"home.constraint.evidence", "home.constraint.experiments", "home.dataConfidence.caveats",
		"home.dataConfidence.connectedSources", "home.dataConfidence.missingSources", "home.freshness.sources",
		"home.scopeDataConfidence.caveats"}
	if got, ok := op.PrimaryList(); !ok || got != "home.signals" || !slices.Equal(op.KeptLists, want) {
		t.Fatalf("primary %q %v kept %v", got, ok, op.KeptLists)
	}
	for _, sibling := range op.SiblingLists() {
		if slices.Contains(want, sibling) {
			t.Fatalf("kept list %s is droppable", sibling)
		}
	}
}
