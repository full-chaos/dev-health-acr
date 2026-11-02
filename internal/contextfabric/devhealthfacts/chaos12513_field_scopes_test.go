package devhealthfacts_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12513: the scope of a field stands under the catalogue. A comparison reads
// these two kinds, so a new numeric scalar of either cannot ship without saying
// what span it speaks for.
func TestEveryNumericScalarOfAComparedKindStatesItsScope(t *testing.T) {
	compared := map[contextfabric.FactKind]int{contextfabric.FactMetrics: 0, contextfabric.FactPullRequests: 0}
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		capability := provider.Capability()
		if _, read := compared[capability.Kind]; !read {
			for _, field := range capability.Fields {
				if field.Scope != "" {
					t.Errorf("%s.%s states scope %q on a kind no comparison reads", capability.Kind, field.Name, field.Scope)
				}
			}
			continue
		}
		for _, field := range capability.Fields {
			numeric := field.Type == contextfabric.FactFieldInteger || field.Type == contextfabric.FactFieldNumber
			if !numeric {
				if field.Scope != "" {
					t.Errorf("%s.%s is not a number and states scope %q", capability.Kind, field.Name, field.Scope)
				}
				continue
			}
			compared[capability.Kind]++
			switch field.Scope {
			case contextfabric.FactScopeWindow, contextfabric.FactScopeLatestDay, contextfabric.FactScopeNotCompared:
			default:
				t.Errorf("%s.%s states no scope: add it to factFieldScopes", capability.Kind, field.Name)
			}
		}
	}
	for kind, count := range compared {
		if count == 0 {
			t.Fatalf("no numeric scalar of %s was read, so this test proves nothing", kind)
		}
	}
}

// The table cannot outlive a declaration, or a renamed field would silently lose
// its scope and leave every comparison.
func TestNoFieldScopeOutlivesItsDeclaration(t *testing.T) {
	declared := map[contextfabric.FactKind]map[string]struct{}{}
	for _, provider := range devhealthfacts.NewProviders(&fakeClient{}) {
		capability := provider.Capability()
		names := map[string]struct{}{}
		for _, field := range capability.Fields {
			if field.Scope != "" {
				names[field.Name] = struct{}{}
			}
		}
		declared[capability.Kind] = names
	}
	scopes := devhealthfacts.FactFieldScopes()
	if len(scopes) == 0 {
		t.Fatal("no scope was read, so this test proves nothing")
	}
	for kind, fields := range scopes {
		for name := range fields {
			if _, ok := declared[kind][name]; !ok {
				t.Errorf("%s.%s has a scope and no numeric declaration", kind, name)
			}
		}
	}
}

// A field is window only where its provider computes it over the window. This
// pins the list, so moving a field to window is a decision and not an edit.
func TestOnlyCheckedFieldsReadAsWindow(t *testing.T) {
	want := map[contextfabric.FactKind]int{contextfabric.FactMetrics: 9, contextfabric.FactPullRequests: 3}
	for kind, fields := range devhealthfacts.FactFieldScopes() {
		got := 0
		for _, scope := range fields {
			if scope == contextfabric.FactScopeWindow {
				got++
			}
		}
		if got != want[kind] {
			t.Errorf("%s holds %d window fields, want %d", kind, got, want[kind])
		}
	}
}

// factFieldStatistics names the statistics a word can claim, and the name parts
// that hold each one.
var factFieldStatistics = []struct {
	word  *regexp.Regexp
	parts []string
}{
	{regexp.MustCompile(`\bmedian\b`), []string{"p50", "median"}},
	{regexp.MustCompile(`\b90th percentile\b`), []string{"p90"}},
	{regexp.MustCompile(`\baverage\b`), []string{"avg", "average"}},
	{regexp.MustCompile(`\bmean\b`), []string{"_mean"}},
	{regexp.MustCompile(`\bstandard deviation\b`), []string{"_stddev"}},
	{regexp.MustCompile(`\bmaximum\b`), []string{"_max"}},
	{regexp.MustCompile(`\bratio\b`), []string{"ratio"}},
	{regexp.MustCompile(`\brate\b`), []string{"rate"}},
}

// TestCHAOS12418EveryLabelKeepsTheWordsOfItsName runs one way: a name part needs
// its word. So a count could read as a median with no p50 in its name, and a table
// row labelled that way states a statistic the cell does not hold.
func TestACountFieldReadsNoStatisticItsNameLacks(t *testing.T) {
	checked := 0
	for name, field := range factFieldCensus(t) {
		if !slices.Contains(field.units, "count") {
			continue
		}
		label, ok := contractsv1.ContextFabricVocabularyLabel(contractsv1.ContextFabricVocabularyFactField, name)
		if !ok {
			continue
		}
		checked++
		for _, statistic := range factFieldStatistics {
			if !statistic.word.MatchString(strings.ToLower(label)) {
				continue
			}
			if !slices.ContainsFunc(statistic.parts, func(part string) bool { return strings.Contains(name, part) }) {
				t.Errorf("count field %q reads %q, a statistic its name does not hold", name, label)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no count field was read, so this test proves nothing")
	}
}
