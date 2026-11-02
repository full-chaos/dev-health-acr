package v1

import (
	"strings"
	"testing"
)

// chaos12639_column_words_test.go pins the column word declarations of a
// claimed table (CHAOS-12639): where each cell's word comes from, held to the
// table's own columns and to the closed column vocabularies.

func columnWordsTable() ContextFabricClaimedFactTable {
	return ContextFabricClaimedFactTable{
		Field: "repository_theme_breakdown", Shape: ContextFabricFactTableShapeBreakdown,
		Key:                []string{"repository", "theme"},
		Measures:           []string{"weighted_effort"},
		Observations:       []string{"repository_label", "source"},
		ColumnVocabularies: map[string]ContextFabricVocabulary{"theme": ContextFabricVocabularyInvestmentTheme},
		WordColumns:        map[string]string{"repository": "repository_label"},
	}
}

func TestCHAOS12639AWellFormedColumnWordDeclarationValidates(t *testing.T) {
	t.Parallel()
	table := columnWordsTable()
	if err := table.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if vocabulary, ok := table.ColumnVocabulary("theme"); !ok || vocabulary != ContextFabricVocabularyInvestmentTheme {
		t.Fatalf("ColumnVocabulary(theme) = %q, %v", vocabulary, ok)
	}
	if word, ok := table.WordColumn("repository"); !ok || word != "repository_label" {
		t.Fatalf("WordColumn(repository) = %q, %v", word, ok)
	}
	if _, ok := table.WordColumn("theme"); ok {
		t.Fatal("WordColumn(theme) is stated, and the table declares none")
	}
}

// Each clause refuses the one declaration it exists for: a word or vocabulary
// a consumer would read that the producer never declared.
func TestCHAOS12639AColumnWordDeclarationOutsideTheTableIsRefused(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		change func(*ContextFabricClaimedFactTable)
		want   string
	}{
		"vocabulary on an undeclared column": {func(t *ContextFabricClaimedFactTable) {
			t.ColumnVocabularies = map[string]ContextFabricVocabulary{"missing": ContextFabricVocabularyInvestmentTheme}
		}, "not one of its key or observation columns"},
		"vocabulary on a measure": {func(t *ContextFabricClaimedFactTable) {
			t.ColumnVocabularies = map[string]ContextFabricVocabulary{"weighted_effort": ContextFabricVocabularyInvestmentTheme}
		}, "not one of its key or observation columns"},
		"vocabulary no column may hold": {func(t *ContextFabricClaimedFactTable) {
			t.ColumnVocabularies = map[string]ContextFabricVocabulary{"theme": ContextFabricVocabularyFactField}
		}, "not one a column may be declared in"},
		"word column for an undeclared column": {func(t *ContextFabricClaimedFactTable) {
			t.WordColumns = map[string]string{"missing": "repository_label"}
		}, "not one of its key or observation columns"},
		"word column that is no observation": {func(t *ContextFabricClaimedFactTable) {
			t.WordColumns = map[string]string{"repository": "theme"}
		}, "is not another of its observations"},
		"word column that is the column itself": {func(t *ContextFabricClaimedFactTable) {
			t.WordColumns = map[string]string{"repository_label": "repository_label"}
		}, "is not another of its observations"},
		"word column that has a word column": {func(t *ContextFabricClaimedFactTable) {
			t.WordColumns = map[string]string{"repository": "repository_label", "repository_label": "source"}
		}, "is itself an id column"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			table := columnWordsTable()
			tc.change(&table)
			err := table.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// The oracle: the served investment_theme words ARE the theme words the render
// shapes use, for every canonical theme, read from the one map.
func TestCHAOS12639TheServedThemeWordsAreTheShapesThemeWords(t *testing.T) {
	t.Parallel()
	themes := subcategoryThemes(func() []string {
		subcategories := ContextFabricInvestmentSubcategoryVocabulary()
		return subcategories[:]
	}())
	if len(themes) != ContextFabricInvestmentThemeCount() {
		t.Fatalf("%d themes from the subcategories, %d theme words", len(themes), ContextFabricInvestmentThemeCount())
	}
	for _, theme := range themes {
		served, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyInvestmentTheme, theme)
		shape, known := ContextFabricInvestmentThemeLabel(theme)
		if !ok || !known || served != shape {
			t.Errorf("theme %q: served %q (%v), shapes %q (%v)", theme, served, ok, shape, known)
		}
	}
	if _, ok := ContextFabricVocabularyLabel(ContextFabricVocabularyInvestmentTheme, "unknown"); ok {
		t.Fatal("a token outside the canonical themes has a served word")
	}
}

func TestCHAOS12639OnlyCellVocabulariesMayBeDeclaredForAColumn(t *testing.T) {
	t.Parallel()
	for _, vocabulary := range ContextFabricColumnVocabularyNames() {
		if _, ok := contextFabricVocabularyLabels[vocabulary]; !ok {
			t.Errorf("column vocabulary %q has no served words", vocabulary)
		}
	}
	for _, vocabulary := range []ContextFabricVocabulary{ContextFabricVocabularyFactField, ContextFabricVocabularyAnswerStatus, ContextFabricVocabularyMeasureUnit} {
		if ValidContextFabricColumnVocabulary(vocabulary) {
			t.Errorf("%q is a column vocabulary, and it names no cell", vocabulary)
		}
	}
}
