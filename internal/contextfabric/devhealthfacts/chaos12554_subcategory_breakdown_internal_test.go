package devhealthfacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12554: the sunburst's subcategory_breakdown table, read from the ops
// investment breakdowns through the operation holder. Every guard here was seen
// failing on a planted defect (see the report of the change).

const sunburstRepoID = "8d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a11"

var sunburstRepo = contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repositoryPrefix + sunburstRepoID, Label: "acme/widgets"}

// breakdownCall is one call the fake caller saw.
type breakdownCall struct {
	operation string
	variables string
}

// breakdownCaller answers each dimension with its own outcome.
type breakdownCaller struct {
	theme, subcategory OperationOutcome
	err                error
	calls              []breakdownCall
}

func (c *breakdownCaller) CallOperation(_ context.Context, _ storage.Principal, operation string, variables json.RawMessage) (OperationOutcome, error) {
	c.calls = append(c.calls, breakdownCall{operation: operation, variables: string(variables)})
	if c.err != nil {
		return OperationOutcome{}, c.err
	}
	if strings.Contains(string(variables), `"dimension":"THEME"`) {
		return c.theme, nil
	}
	return c.subcategory, nil
}

// breakdownAnswer is an ops analytics answer of one breakdown, as the runner
// serves it: items by value, highest first, with the typenames.
func breakdownAnswer(dimension string, items map[string]float64) OperationOutcome {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if items[keys[i]] != items[keys[j]] {
			return items[keys[i]] > items[keys[j]]
		}
		return keys[i] < keys[j]
	})
	type item struct {
		Key      string  `json:"key"`
		Value    float64 `json:"value"`
		TypeName string  `json:"__typename"`
	}
	list := make([]item, 0, len(keys))
	for _, key := range keys {
		list = append(list, item{Key: key, Value: items[key], TypeName: "BreakdownItem"})
	}
	raw, _ := json.Marshal(map[string]any{"analytics": map[string]any{
		"breakdowns":                  []any{map[string]any{"dimension": strings.ToLower(dimension), "measure": "issue_size", "items": list, "__typename": "BreakdownResult"}},
		"evidenceQualityDistribution": nil, "evidenceQualityStats": nil, "__typename": "AnalyticsResult",
	}})
	return OperationOutcome{Served: true, Complete: true, Data: raw}
}

// sunburstSubcategories is a split with measured zeros: risk.security is served
// as 0, and the subcategories ops leaves out are measured 0 too.
func sunburstSubcategories() map[string]float64 {
	return map[string]float64{
		"feature_delivery.customer": 12.5, "feature_delivery.roadmap": 7.25, "maintenance.debt": 3,
		"operational.support": 4.75, "quality.bugfix": 2.5, "risk.security": 0,
	}
}

func sunburstThemes() map[string]float64 {
	return map[string]float64{"feature_delivery": 19.75, "maintenance": 3, "operational": 4.75, "quality": 2.5}
}

func servingCaller() *breakdownCaller {
	return &breakdownCaller{theme: breakdownAnswer("THEME", sunburstThemes()), subcategory: breakdownAnswer("SUBCATEGORY", sunburstSubcategories())}
}

// sunburstWindow is a committed 30-day window of whole UTC days.
func sunburstWindow() *contractsv1.ContextFabricRequestedEvidenceWindow {
	start, end := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	return &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end, RelativeID: contractsv1.ContextFabricRelativeWindowTrailing30D}
}

func sunburstClient(repoIDs ...string) *routeClient {
	rows := make([][]any, 0, len(repoIDs))
	for _, id := range repoIDs {
		rows = append(rows, uncategorizedRepoMixRow(id, map[string]float64{"feature_delivery": 2, "quality": 1}, 3, []uint64{1, 2, 3}, []uint64{}))
	}
	return &routeClient{routes: []route{{marker: "AS sized_unit_hashes", rows: rows}}}
}

// readSunburst reads the repository's investment with the switch as given and
// the caller bound.
func readSunburst(t *testing.T, ctx context.Context, enabled bool, caller OperationCaller, window *contractsv1.ContextFabricRequestedEvidenceWindow, subjects ...contextfabric.SubjectRef) contextfabric.FactProviderResult {
	t.Helper()
	holder := NewOperationHolder()
	holder.Set(caller)
	provider := newInvestmentProvider(sunburstClient(sunburstRepoID, "9d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a12"))
	provider.operations = holder
	provider.subcategoryBreakdown = enabled
	if len(subjects) == 0 {
		subjects = []contextfabric.SubjectRef{sunburstRepo}
	}
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: window},
		Kind: contextfabric.FactInvestment, Subjects: subjects,
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	return result
}

func subcategoryTableOf(t *testing.T, result contextfabric.FactProviderResult) (contextfabric.FactValue, bool) {
	t.Helper()
	fact := investmentFactOf(result, sunburstRepo)
	if fact == nil {
		t.Fatalf("no investment fact for the repository: %+v", result)
	}
	value, ok := fact.Fields[subcategoryBreakdownField]
	return value, ok
}

func TestCHAOS12554TheTwoCallsSendTheRuledVariables(t *testing.T) {
	t.Parallel()
	caller := servingCaller()
	readSunburst(t, context.Background(), true, caller, sunburstWindow())
	want := []breakdownCall{
		{operation: "investmentBreakdownRepository", variables: `{"batch":{"breakdowns":[{"dimension":"THEME","measure":"ISSUE_SIZE","dateRange":{"startDate":"2026-09-01","endDate":"2026-10-01"},"topN":100}],"filters":{"scope":{"level":"REPO","ids":["repository:8d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a11"]}}}}`},
		{operation: "investmentBreakdownRepository", variables: `{"batch":{"breakdowns":[{"dimension":"SUBCATEGORY","measure":"ISSUE_SIZE","dateRange":{"startDate":"2026-09-01","endDate":"2026-10-01"},"topN":100}],"filters":{"scope":{"level":"REPO","ids":["repository:8d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a11"]}}}}`},
	}
	if !slices.Equal(caller.calls, want) {
		t.Fatalf("calls:\n%+v\nwant:\n%+v", caller.calls, want)
	}
	team := string(subcategoryBreakdownVariables("THEME", "TEAM", "team:linear:CHAOS", "2026-09-01", "2026-10-01"))
	if want := `{"batch":{"breakdowns":[{"dimension":"THEME","measure":"ISSUE_SIZE","dateRange":{"startDate":"2026-09-01","endDate":"2026-10-01"},"topN":100}],"filters":{"scope":{"level":"TEAM","ids":["team:linear:CHAOS"]}}}}`; team != want {
		t.Fatalf("team variables:\n%s\nwant\n%s", team, want)
	}
	if operation, level, ok := subcategoryBreakdownScopeOf(contextfabric.SubjectTeam); !ok || operation != "investmentBreakdownTeam" || level != "TEAM" {
		t.Fatalf("team scope = %q %q %v", operation, level, ok)
	}
	if _, _, ok := subcategoryBreakdownScopeOf(contextfabric.SubjectOrganization); ok {
		t.Fatal("an organization is read for a sunburst")
	}
}

func TestCHAOS12554TheTableHoldsEveryCanonicalSubcategoryAndItsThemeShare(t *testing.T) {
	t.Parallel()
	result := readSunburst(t, context.Background(), true, servingCaller(), sunburstWindow())
	value, ok := subcategoryTableOf(t, result)
	if !ok {
		t.Fatalf("no subcategory_breakdown on the fact; reasons %q", servedReasons(result, sunburstRepo))
	}
	if value.Table == nil || !value.Table.Detail || value.Table.Shape != contextfabric.FactTableBreakdown ||
		!slices.Equal(value.Table.Key, []string{"subcategory"}) || !slices.Equal(value.Table.Measures, []string{"share", "theme_share"}) ||
		!slices.Equal(value.Table.Observations, []string{"theme"}) {
		t.Fatalf("table declaration %+v", value.Table)
	}
	if err := value.Table.Validate(); err != nil {
		t.Fatalf("the declared table is not valid: %v", err)
	}
	vocabulary := contractsv1.ContextFabricInvestmentSubcategoryVocabulary()
	if len(value.Rows) != len(vocabulary) {
		t.Fatalf("rows %d, want %d", len(value.Rows), len(vocabulary))
	}
	const total = 30.0
	subcategories := sunburstSubcategories()
	themeShares := map[string]float64{}
	for index, row := range value.Rows {
		key := *row.Fields["subcategory"].String
		theme, _ := contractsv1.ContextFabricInvestmentSubcategoryTheme(key)
		if key != vocabulary[index] || *row.Fields["theme"].String != theme {
			t.Fatalf("row %d = %s under %s, want %s under %s", index, key, *row.Fields["theme"].String, vocabulary[index], theme)
		}
		share := row.Fields["share"].Number
		if share == nil || *share != subcategories[key]/total {
			t.Fatalf("%s share = %v, want %v (a subcategory ops left out is a measured 0)", key, share, subcategories[key]/total)
		}
		themeShares[theme] += *share
	}
	for _, row := range value.Rows {
		theme := *row.Fields["theme"].String
		if got := *row.Fields["theme_share"].Number; got != themeShares[theme] {
			t.Fatalf("theme %s: theme_share %v, want the exact sum of its share cells %v", theme, got, themeShares[theme])
		}
	}
	if got := *value.Rows[0].Fields["theme_share"].Number; got != 19.75/total {
		t.Fatalf("feature_delivery theme_share = %v, want %v", got, 19.75/total)
	}
	if reasons := servedReasons(result, sunburstRepo); strings.Contains(reasons, subcategoryWithheldReasonPrefix) {
		t.Fatalf("a served table carries a withheld reason: %q", reasons)
	}
}

// Lead ruling (CHAOS-12554): the sums run in the sorted key order, so amd64 and
// arm64 give the same bits. The Mac is arm64 and CI amd64; both run this.
func TestCHAOS12554ThemeShareBitsArePinned(t *testing.T) {
	t.Parallel()
	subcategories := map[string]float64{
		"feature_delivery.customer": 0.1, "feature_delivery.enablement": 0.2, "feature_delivery.roadmap": 0.3,
		"maintenance.debt": 0.7, "quality.bugfix": 1.3, "risk.security": 0.07,
	}
	themes := map[string]float64{"feature_delivery": 0.6, "maintenance": 0.7, "quality": 1.3, "risk": 0.07}
	value, withheld := subcategoryBreakdownTable(themes, subcategories, grainExact)
	if withheld != nil {
		t.Fatalf("withheld: %+v", withheld)
	}
	cells := map[string]map[string]float64{}
	for _, row := range value.Rows {
		cells[*row.Fields["subcategory"].String] = map[string]float64{"share": *row.Fields["share"].Number, "theme_share": *row.Fields["theme_share"].Number}
	}
	for _, pin := range []struct {
		key, field string
		bits       uint64
	}{
		// 0.1/2.67 + 0.2/2.67 + 0.3/2.67, the total summed in sorted key order.
		{"feature_delivery.customer", "theme_share", 0x3fccc398730e61cc},
		{"feature_delivery.customer", "share", math.Float64bits(0.1 / 2.67)},
		{"maintenance.debt", "theme_share", math.Float64bits(0.7 / 2.67)},
	} {
		if got := math.Float64bits(cells[pin.key][pin.field]); got != pin.bits {
			t.Errorf("%s %s bits = %#x, want %#x", pin.key, pin.field, got, pin.bits)
		}
	}
}

func TestCHAOS12554TheThemeCrossCheckRefusesAGap(t *testing.T) {
	buf := captureLinkLines(t)
	ctx := observability.WithRequestID(context.Background(), linkLineRequestID)
	themes := sunburstThemes()
	themes["quality"] *= 1 + 1e-3
	caller := &breakdownCaller{theme: breakdownAnswer("THEME", themes), subcategory: breakdownAnswer("SUBCATEGORY", sunburstSubcategories())}
	result := readSunburst(t, ctx, true, caller, sunburstWindow())
	if _, ok := subcategoryTableOf(t, result); ok {
		t.Fatal("a theme 1e-3 off its subcategories served a table")
	}
	if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, subcategoryWithheldReasonPrefix) || !strings.Contains(reasons, "(theme_cross_check)") {
		t.Fatalf("reasons %q, want the closed cross-check reason", reasons)
	}
	certifyLinkLine(t, buf, eventspec.InvestmentSubcategoryBreakdownWithheld, map[string]any{
		"org_id": "org-1", "subject_kind": "repository", "reason": "theme_cross_check", "theme": "quality",
		"relative_gap": relativeGap(themes["quality"], 2.5),
	})
	// Within the tolerance it is served: Float32 noise is not a disagreement.
	within := sunburstThemes()
	within["quality"] *= 1 + 5e-7
	served := readSunburst(t, context.Background(), true, &breakdownCaller{theme: breakdownAnswer("THEME", within), subcategory: breakdownAnswer("SUBCATEGORY", sunburstSubcategories())}, sunburstWindow())
	if _, ok := subcategoryTableOf(t, served); !ok {
		t.Fatalf("a gap of 5e-7 refused the table: %q", servedReasons(served, sunburstRepo))
	}
}

func TestCHAOS12554AWithheldTableIsAClosedReasonNeverZeros(t *testing.T) {
	t.Parallel()
	unknown := sunburstSubcategories()
	unknown["quality.flaky"] = 0.5
	unknownZero := sunburstSubcategories()
	unknownZero["quality.flaky"] = 0
	for name, tc := range map[string]struct {
		caller *breakdownCaller
		window *contractsv1.ContextFabricRequestedEvidenceWindow
		want   string
		calls  int
	}{
		"ops refused the call": {
			caller: &breakdownCaller{theme: OperationOutcome{Reason: "refused:variable_not_allowed"}}, want: "(not_served, call refused:variable_not_allowed)", calls: 1,
		},
		"a call status that is not a closed token": {
			caller: &breakdownCaller{theme: OperationOutcome{Reason: "Refused: org secret"}}, want: "(not_served, call unknown)", calls: 1,
		},
		"the call failed": {caller: &breakdownCaller{err: errors.New("boom")}, want: "(call_failed)", calls: 1},
		"an incomplete answer": {
			caller: &breakdownCaller{theme: breakdownAnswer("THEME", sunburstThemes()), subcategory: OperationOutcome{Served: true, Complete: false, Data: breakdownAnswer("SUBCATEGORY", sunburstSubcategories()).Data}},
			want:   "(incomplete)", calls: 2,
		},
		"an unknown key above 0": {
			caller: &breakdownCaller{theme: breakdownAnswer("THEME", sunburstThemes()), subcategory: breakdownAnswer("SUBCATEGORY", unknown)}, want: "(unknown_key)", calls: 2,
		},
		"both breakdowns empty": {
			caller: &breakdownCaller{theme: breakdownAnswer("THEME", nil), subcategory: breakdownAnswer("SUBCATEGORY", nil)}, want: "(no_data)", calls: 2,
		},
		"an answer of another dimension": {
			caller: &breakdownCaller{theme: breakdownAnswer("SUBCATEGORY", sunburstSubcategories())}, want: "(unreadable)", calls: 1,
		},
		"a window that is not whole days": {
			caller: servingCaller(), window: func() *contractsv1.ContextFabricRequestedEvidenceWindow {
				start, end := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
				return &contractsv1.ContextFabricRequestedEvidenceWindow{Start: &start, End: &end}
			}(), want: "(window_not_whole_days)", calls: 0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			window := tc.window
			if window == nil {
				window = sunburstWindow()
			}
			result := readSunburst(t, context.Background(), true, tc.caller, window)
			if _, ok := subcategoryTableOf(t, result); ok {
				t.Fatal("a table was served")
			}
			if reasons := servedReasons(result, sunburstRepo); !strings.Contains(reasons, subcategoryWithheldReasonPrefix) || !strings.Contains(reasons, tc.want) {
				t.Fatalf("reasons %q, want %q", reasons, tc.want)
			}
			if len(tc.caller.calls) != tc.calls {
				t.Fatalf("calls %d, want %d", len(tc.caller.calls), tc.calls)
			}
		})
	}
	// A key outside the list at 0 places nothing: the table is served.
	served := readSunburst(t, context.Background(), true, &breakdownCaller{theme: breakdownAnswer("THEME", sunburstThemes()), subcategory: breakdownAnswer("SUBCATEGORY", unknownZero)}, sunburstWindow())
	if _, ok := subcategoryTableOf(t, served); !ok {
		t.Fatalf("an unknown key at 0 refused the table: %q", servedReasons(served, sunburstRepo))
	}
}

func TestCHAOS12554AnAnswerWithANullOrRepeatedValueIsUnreadable(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"null value":   `{"analytics":{"breakdowns":[{"dimension":"theme","items":[{"key":"quality","value":null}]}]}}`,
		"repeated key": `{"analytics":{"breakdowns":[{"dimension":"theme","items":[{"key":"quality","value":1},{"key":"quality","value":2}]}]}}`,
		"negative":     `{"analytics":{"breakdowns":[{"dimension":"theme","items":[{"key":"quality","value":-1}]}]}}`,
		"two":          `{"analytics":{"breakdowns":[{"dimension":"theme","items":[]},{"dimension":"theme","items":[]}]}}`,
		"no analytics": `{"analytics":null}`,
	} {
		if _, ok := parseInvestmentBreakdown(json.RawMessage(body), "THEME"); ok {
			t.Errorf("%s: read as an answer", name)
		}
	}
	if values, ok := parseInvestmentBreakdown(json.RawMessage(`{"analytics":{"breakdowns":[{"dimension":"theme","items":[{"key":"quality","value":1.5}]}]}}`), "THEME"); !ok || values["quality"] != 1.5 {
		t.Fatalf("a plain answer: %v %v", values, ok)
	}
}

// Lead ruling (CHAOS-12554): a read of several subjects makes no call and
// serves no table, and says so with a closed reason on every subject. It never
// drops the table silently, and it never serves zero rows or an empty table.
func TestCHAOS12554AReadOfSeveralSubjectsMakesNoCallAndSaysWhy(t *testing.T) {
	t.Parallel()
	caller := servingCaller()
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectRepository, CanonicalID: repositoryPrefix + "9d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a12"}
	result := readSunburst(t, context.Background(), true, caller, sunburstWindow(), sunburstRepo, other)
	if len(caller.calls) != 0 {
		t.Fatalf("calls %d, want none: a sunburst plots one breakdown", len(caller.calls))
	}
	for _, subject := range []contextfabric.SubjectRef{sunburstRepo, other} {
		fact := investmentFactOf(result, subject)
		if fact == nil {
			t.Fatalf("%s: no investment fact", subject.CanonicalID)
		}
		if value, ok := fact.Fields[subcategoryBreakdownField]; ok {
			t.Fatalf("%s: a subcategory table was served on a read of two subjects: %+v", subject.CanonicalID, value)
		}
		if reasons := servedReasons(result, subject); !strings.Contains(reasons, subcategoryWithheldReasonPrefix) || !strings.Contains(reasons, "(several_subjects)") {
			t.Fatalf("%s: reasons %q, want the closed reason several_subjects", subject.CanonicalID, reasons)
		}
	}
	if slices.Contains(SubcategoryWithheldReasonVocabulary(), SubcategoryWithheldSeveralSubjects) {
		t.Fatal("several_subjects is on the Warn line's vocabulary: a cohort read is no failure")
	}
}

// The switch off: no call, no field, no reason. The same read with it on
// changes the facts, so the comparison can fail.
func TestCHAOS12554OffTheReadIsAsBefore(t *testing.T) {
	t.Parallel()
	caller := servingCaller()
	off := readSunburst(t, context.Background(), false, caller, sunburstWindow())
	if len(caller.calls) != 0 {
		t.Fatalf("the switch off made %d calls", len(caller.calls))
	}
	baseline, err := newInvestmentProvider(sunburstClient(sunburstRepoID, "9d0f8f5c-0b55-4f43-9f7e-3b0f6f0d1a12")).ReadFacts(context.Background(), storage.Principal{OrgID: "org-1"}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent, EvidenceWindow: sunburstWindow()}, Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{sunburstRepo},
	})
	if err != nil {
		t.Fatal(err)
	}
	offJSON, _ := json.Marshal(off)
	baselineJSON, _ := json.Marshal(baseline)
	if !bytes.Equal(offJSON, baselineJSON) {
		t.Fatalf("the switch off changed the read:\n%s\n%s", offJSON, baselineJSON)
	}
	on := readSunburst(t, context.Background(), true, servingCaller(), sunburstWindow())
	if onJSON, _ := json.Marshal(on); bytes.Equal(onJSON, baselineJSON) {
		t.Fatal("the switch on did not change the read: this comparison cannot fail")
	}
}

// CHAOS-12639: the table's token columns name their vocabularies, and every
// token the table serves has a registry word, so a reader sees words.
func TestCHAOS12554TheTableNamesItsColumnVocabularies(t *testing.T) {
	t.Parallel()
	result := readSunburst(t, context.Background(), true, servingCaller(), sunburstWindow())
	value, ok := subcategoryTableOf(t, result)
	if !ok {
		t.Fatal("no table")
	}
	capability := newInvestmentProvider(nil).Capability()
	table := contractsv1.ContextFabricClaimedFactTable{Field: subcategoryBreakdownField, Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
		Key: value.Table.Key, Measures: value.Table.Measures, Observations: value.Table.Observations}
	for _, kind := range []contextfabric.SubjectKind{contextfabric.SubjectRepository, contextfabric.SubjectTeam} {
		vocabularies, _ := contextfabric.DeclaredColumnWords(capability, kind, table)
		want := map[string]contractsv1.ContextFabricVocabulary{"subcategory": contractsv1.ContextFabricVocabularySubcategory, "theme": contractsv1.ContextFabricVocabularyInvestmentTheme}
		if len(vocabularies) != len(want) || vocabularies["subcategory"] != want["subcategory"] || vocabularies["theme"] != want["theme"] {
			t.Fatalf("%s: column vocabularies %v, want %v", kind, vocabularies, want)
		}
	}
	for _, row := range value.Rows {
		for column, vocabulary := range map[string]contractsv1.ContextFabricVocabulary{"subcategory": contractsv1.ContextFabricVocabularySubcategory, "theme": contractsv1.ContextFabricVocabularyInvestmentTheme} {
			token := *row.Fields[column].String
			if word, worded := contractsv1.ContextFabricVocabularyLabel(vocabulary, token); !worded || word == "" || word == token {
				t.Fatalf("%s %q has no registry word (%q)", column, token, word)
			}
		}
	}
}

func TestCHAOS12554TheWithheldReasonsAreTheLinesVocabulary(t *testing.T) {
	t.Parallel()
	for _, field := range eventspec.InvestmentSubcategoryBreakdownWithheld.Fields {
		switch field.Key {
		case "reason":
			if !slices.Equal(field.ClosedVocabulary, SubcategoryWithheldReasonVocabulary()) {
				t.Fatalf("line reasons %v, provider reasons %v", field.ClosedVocabulary, SubcategoryWithheldReasonVocabulary())
			}
		case "theme":
			want := append([]string{""}, slices.Sorted(slices.Values(canonicalInvestmentThemes[:]))...)
			if !slices.Equal(field.ClosedVocabulary, want) {
				t.Fatalf("line themes %v, want %v", field.ClosedVocabulary, want)
			}
		}
	}
}
