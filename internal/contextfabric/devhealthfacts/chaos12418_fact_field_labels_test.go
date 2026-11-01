package devhealthfacts_test

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12418. The label of a fact field stands under the catalogue-truth test
// (CHAOS-7073): that test holds emitted and declared to one set, and these hold
// declared and labeled to one set. contracts/v1 cannot import the declarations,
// so its own totality tests read only its table. This file is the proof.

// declaredFactField is one declared name, over every kind that declares it.
type declaredFactField struct {
	kinds []string
	units []string
}

func declaredFactFields(providers []contextfabric.FactProvider) map[string]*declaredFactField {
	declared := map[string]*declaredFactField{}
	note := func(name, unit string, kind contextfabric.FactKind) {
		field := declared[name]
		if field == nil {
			field = &declaredFactField{}
			declared[name] = field
		}
		field.kinds = append(field.kinds, string(kind))
		if !slices.Contains(field.units, unit) {
			field.units = append(field.units, unit)
		}
	}
	for _, provider := range providers {
		capability := provider.Capability()
		for _, field := range capability.Fields {
			note(field.Name, field.Unit, capability.Kind)
			for _, column := range field.Columns {
				note(column.Name, column.Unit, capability.Kind)
			}
		}
	}
	return declared
}

// factFieldCensus reads the whole catalogue, and fails when it reads less, so a
// broken census cannot pass the tests below by finding nothing.
func factFieldCensus(t *testing.T) map[string]*declaredFactField {
	t.Helper()
	declared := declaredFactFields(devhealthfacts.NewProviders(&fakeClient{}))
	if len(declared) < 300 {
		t.Fatalf("census saw %d declared names, want the whole catalogue", len(declared))
	}
	return declared
}

// factFieldUnitWords is the word each declared unit reads in a label. A count
// reads no word, because the noun of the label is its unit.
var factFieldUnitWords = map[string]string{
	"":        "",
	"count":   "",
	"axis":    "coordinate",
	"days":    "days",
	"hours":   "hours",
	"lines":   "lines",
	"loc":     "lines",
	"minutes": "minutes",
	"points":  "points",
	"ratio":   "ratio",
	"score":   "score",
	"seconds": "seconds",
	"share":   "share",
}

func TestCHAOS12418EveryDeclaredFactFieldHasALabel(t *testing.T) {
	for name, field := range factFieldCensus(t) {
		if _, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, name); !ok {
			t.Errorf("declared field %q (kinds %v) has no label: add it to contextFabricFactFieldLabels", name, field.kinds)
		}
	}
}

func TestCHAOS12418NoFactFieldLabelOutlivesItsDeclaration(t *testing.T) {
	declared := factFieldCensus(t)
	for name, label := range contractsv1.ContextFabricFactFieldLabels() {
		if _, ok := declared[name]; !ok {
			t.Errorf("label %q names %q, which no provider declares", label, name)
		}
	}
}

var factFieldLabelTrailingUnit = regexp.MustCompile(`\(([a-z]+)\)$`)

// Each way: the word of the declared unit stands in the label, and a unit the
// label states at its end is the declared one, under every kind that declares
// the name.
func TestCHAOS12418EveryLabelStatesItsDeclaredUnit(t *testing.T) {
	for name, field := range factFieldCensus(t) {
		label, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, name)
		if !ok {
			continue
		}
		var words []string
		for _, unit := range field.units {
			word, known := factFieldUnitWords[unit]
			if !known {
				t.Errorf("field %q: unit %q has no word here: say how a label states it", name, unit)
				continue
			}
			words = append(words, word)
			if !strings.Contains(strings.ToLower(label), word) {
				t.Errorf("field %q has unit %q, but its label %q does not say %q", name, unit, label, word)
			}
		}
		if match := factFieldLabelTrailingUnit.FindStringSubmatch(label); match != nil && !slices.Contains(words, match[1]) {
			t.Errorf("field %q: label %q states the unit %q, but the declared units are %v", name, label, match[1], field.units)
		}
	}
}

// factFieldNameWords holds a metric's meaning to its name: each part of a name
// has one word its label must say. A wrong metric label is worse than a raw name,
// and two labels swapped by hand pass every other test here.
var factFieldNameWords = []struct{ part, word string }{
	{"p50", "median"},
	{"median", "median"},
	{"p90", "90th percentile"},
	{"avg", "average"},
	{"_mean", "mean"},
	{"_stddev", "standard deviation"},
	{"_max_", "maximum"},
	{"_30d", "last 30 days"},
	{"latest_", "latest"},
	{"_current", "now"},
	{"_window", "window"},
	{"prior_", "prior window"},
	{"_basis", "basis"},
	{"_omitted_count", "left out"},
	{"_unavailable_reason", "unavailable"},
	{"theme_feature_delivery", "feature delivery theme share"},
	{"theme_maintenance", "maintenance theme share"},
	{"theme_operational", "operational theme share"},
	{"theme_quality", "quality theme"},
	{"theme_risk", "risk theme share"},
}

func TestCHAOS12418EveryLabelKeepsTheWordsOfItsName(t *testing.T) {
	for name := range factFieldCensus(t) {
		label, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, name)
		if !ok {
			continue
		}
		for _, rule := range factFieldNameWords {
			if strings.Contains(name, rule.part) && !strings.Contains(strings.ToLower(label), rule.word) {
				t.Errorf("field %q has %q in its name, but its label %q does not say %q", name, rule.part, label, rule.word)
			}
		}
	}
}

var factFieldLabelShape = regexp.MustCompile(`^[A-Z0-9][A-Za-z0-9 ,/()-]*$`)

func TestCHAOS12418EveryLabelReadsAsAHeading(t *testing.T) {
	labels := contractsv1.ContextFabricFactFieldLabels()
	if len(labels) == 0 {
		t.Fatal("the table is empty: this measured nothing")
	}
	for name, label := range labels {
		if !factFieldLabelShape.MatchString(label) {
			t.Errorf("field %q: label %q is not a heading", name, label)
		}
		if utf8.RuneCountInString(label) > 64 {
			t.Errorf("field %q: label %q is too long for a column", name, label)
		}
	}
}

// An answer holds only the fields it claims, so the measure that matters is a
// kind's names, not the table's: every name of the widest kind stays a small share
// of the budget. Kinds in one answer add up, to the table's size at most.
func TestCHAOS12418OneKindsWordsAreASmallShareOfTheAnswerBudget(t *testing.T) {
	const budget = 65536
	labels := contractsv1.ContextFabricFactFieldLabels()
	byKind := map[string]map[string]string{}
	for name, field := range factFieldCensus(t) {
		for _, kind := range field.kinds {
			if byKind[kind] == nil {
				byKind[kind] = map[string]string{}
			}
			byKind[kind][contractsv1.ContextFabricVocabularyLabelKey(contractsv1.ContextFabricVocabularyFactField, name)] = labels[name]
		}
	}
	widest, widestKind := 0, ""
	for kind, words := range byKind {
		encoded, err := json.Marshal(words)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) > widest {
			widest, widestKind = len(encoded), kind
		}
	}
	if widest == 0 {
		t.Fatal("no kind has a word: this measured nothing")
	}
	if widest > budget/8 {
		t.Errorf("kind %q is %d bytes of words, over an eighth of the %d-byte answer budget", widestKind, widest, budget)
	}
	t.Logf("widest kind %s: %d bytes, %.1f%% of %d", widestKind, widest, float64(widest)*100/budget, budget)
}
