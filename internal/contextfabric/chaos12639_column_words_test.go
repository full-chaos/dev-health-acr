package contextfabric

import (
	"context"
	"slices"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos12639_column_words_test.go pins where a claimed table's cell words come
// from (CHAOS-12639): the producer's own column declaration, copied by the
// server, and nothing else.

func columnWordsCapability() FactCapability {
	return FactCapability{Kind: FactInvestment, Fields: []FactFieldDeclaration{{
		Name: "repository_theme_breakdown", Type: FactFieldTable,
		SubjectKinds: []SubjectKind{contractsv1.ContextFabricSubjectTeam},
		Columns: []FactColumnDeclaration{
			{Name: "repository", Type: FactFieldString, WordColumn: "repository_label"},
			{Name: "repository_label", Type: FactFieldString},
			{Name: "theme", Type: FactFieldString, Vocabulary: contractsv1.ContextFabricVocabularyInvestmentTheme},
			{Name: "weighted_effort", Type: FactFieldNumber},
			{Name: "direct_only_id", Type: FactFieldString, WordColumn: "direct_only_name"},
			{Name: "direct_only_name", Type: FactFieldString},
		},
	}}}
}

func columnWordsClaimTable(vocabularies []contractsv1.ContextFabricColumnVocabularyDeclaration, words []contractsv1.ContextFabricWordColumnDeclaration) *contractsv1.ContextFabricClaimedFactTable {
	return &contractsv1.ContextFabricClaimedFactTable{
		Field: "repository_theme_breakdown", Shape: contractsv1.ContextFabricFactTableShapeBreakdown,
		Key: []string{"repository", "theme"}, Measures: []string{"weighted_effort"}, Observations: []string{"repository_label"},
		ColumnVocabularies: vocabularies, WordColumns: words,
	}
}

func columnWordsClaim(subject SubjectKind, table *contractsv1.ContextFabricClaimedFactTable) ClaimedFact {
	return ClaimedFact{
		ClaimID: "claim_words", Kind: FactInvestment, Field: "repository_theme_breakdown",
		Subject: SubjectRef{Kind: subject, CanonicalID: string(subject) + ":one"},
		Table:   table, TimeSeriesTable: table,
	}
}

var (
	declaredVocabularies = []contractsv1.ContextFabricColumnVocabularyDeclaration{{Column: "theme", Vocabulary: contractsv1.ContextFabricVocabularyInvestmentTheme}}
	declaredWords        = []contractsv1.ContextFabricWordColumnDeclaration{{Column: "repository", WordColumn: "repository_label"}}
)

// Only the columns the claimed table carries are named: the direct-only pair is
// declared, but the answer does not carry it.
func TestCHAOS12639AColumnGetsTheWordsItsDeclarationNames(t *testing.T) {
	t.Parallel()
	vocabularies, words := DeclaredColumnWords(columnWordsCapability(), contractsv1.ContextFabricSubjectTeam, *columnWordsClaimTable(nil, nil))
	if !slices.Equal(vocabularies, declaredVocabularies) || !slices.Equal(words, declaredWords) {
		t.Fatalf("DeclaredColumnWords = %+v, %+v; want %+v, %+v", vocabularies, words, declaredVocabularies, declaredWords)
	}
	// A word column the table does not carry names nothing: the reader would be
	// sent to a column that is not there.
	cut := columnWordsClaimTable(nil, nil)
	cut.Observations = nil
	if _, words := DeclaredColumnWords(columnWordsCapability(), contractsv1.ContextFabricSubjectTeam, *cut); words != nil {
		t.Fatalf("with no repository_label observation: words = %+v, want none", words)
	}
	// Another subject kind, or another field, states nothing.
	if v, w := DeclaredColumnWords(columnWordsCapability(), contractsv1.ContextFabricSubjectRepository, *columnWordsClaimTable(nil, nil)); v != nil || w != nil {
		t.Fatalf("repository subject: %+v, %+v; want none", v, w)
	}
}

// The words replace whatever the table carried, on both tables, and the table
// the claim pointed at is not rewritten.
func TestCHAOS12639TheDeclarationReplacesAnyWordsATableCarried(t *testing.T) {
	t.Parallel()
	forgedVocabularies := []contractsv1.ContextFabricColumnVocabularyDeclaration{{Column: "repository", Vocabulary: contractsv1.ContextFabricVocabularySubcategory}}
	forgedWords := []contractsv1.ContextFabricWordColumnDeclaration{{Column: "theme", WordColumn: "repository_label"}}
	claim := columnWordsClaim(contractsv1.ContextFabricSubjectTeam, columnWordsClaimTable(forgedVocabularies, forgedWords))
	wordsOf := declaredColumnWordsOf([]FactCapability{columnWordsCapability()})
	stated := withDeclaredColumnWords([]ClaimedFact{claim}, wordsOf)
	for name, table := range map[string]*contractsv1.ContextFabricClaimedFactTable{"table": stated[0].Table, "time_series_table": stated[0].TimeSeriesTable} {
		if !slices.Equal(table.ColumnVocabularies, declaredVocabularies) || !slices.Equal(table.WordColumns, declaredWords) {
			t.Errorf("%s = %+v, %+v; want the declared %+v, %+v", name, table.ColumnVocabularies, table.WordColumns, declaredVocabularies, declaredWords)
		}
		if err := table.Validate(); err != nil {
			t.Errorf("%s: the stated table does not validate: %v", name, err)
		}
	}
	if !slices.Equal(claim.Table.ColumnVocabularies, forgedVocabularies) || !slices.Equal(claim.Table.WordColumns, forgedWords) {
		t.Fatal("the source table was rewritten")
	}
	cleared := withDeclaredColumnWords([]ClaimedFact{claim}, declaredColumnWordsOf(nil))
	if cleared[0].Table.ColumnVocabularies != nil || cleared[0].Table.WordColumns != nil {
		t.Fatalf("with no declarations: %+v, %+v; want none", cleared[0].Table.ColumnVocabularies, cleared[0].Table.WordColumns)
	}
	again := withDeclaredColumnWords(stated, wordsOf)
	if again[0].Table != stated[0].Table {
		t.Fatal("a second pass copied a table it did not change")
	}
}

// finalizeResult states the words, from the engine's own declarations.
func TestCHAOS12639TheEngineStatesTheColumnWordsWhenItFinalizes(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(tableEngineDependencies(), EngineOptions{ServiceVersion: "words", NewResultID: resultIDSequence(), FactCapabilities: []FactCapability{columnWordsCapability()}})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	result := InvestigationResult{
		Status: InvestigationComplete, ResultID: "result_12639000",
		Coverage:     Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		ClaimedFacts: []ClaimedFact{columnWordsClaim(contractsv1.ContextFabricSubjectTeam, columnWordsClaimTable(nil, nil))},
	}
	plan := AnswerPlan{Family: QuestionFamilyScopedCohortStatus, FamilyVersion: QuestionFamilyTableVersion}
	finalized := engine.finalizeResult(context.Background(), storage.Principal{}, result, plan, tableFrame(), CanonicalFactBundle{}, &assemblyTelemetry{}, answerPassFirst, MembershipCardinality{})
	table := finalized.ClaimedFacts[0].Table
	if !slices.Equal(table.ColumnVocabularies, declaredVocabularies) || !slices.Equal(table.WordColumns, declaredWords) {
		t.Fatalf("finalized = %+v, %+v; want %+v, %+v", table.ColumnVocabularies, table.WordColumns, declaredVocabularies, declaredWords)
	}
}

// A declaration a consumer could not trust is refused when the registry is
// built, before any answer carries it.
func TestCHAOS12639AColumnWordDeclarationIsHeldToItsTable(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		change func([]FactColumnDeclaration)
		want   string
	}{
		"vocabulary on a number": {func(c []FactColumnDeclaration) {
			c[3].Vocabulary = contractsv1.ContextFabricVocabularyInvestmentTheme
		}, "is not a string column"},
		"vocabulary no column may hold": {func(c []FactColumnDeclaration) {
			c[2].Vocabulary = contractsv1.ContextFabricVocabularyFactField
		}, "not one a column may be declared in"},
		"word column on a number": {func(c []FactColumnDeclaration) {
			c[3].WordColumn = "repository_label"
		}, "is not a string column"},
		"word column that is not declared": {func(c []FactColumnDeclaration) {
			c[0].WordColumn = "missing"
		}, "is not another string column"},
		"word column that is the column itself": {func(c []FactColumnDeclaration) {
			c[0].WordColumn = "repository"
		}, "is not another string column"},
		"word column that is a number": {func(c []FactColumnDeclaration) {
			c[0].WordColumn = "weighted_effort"
		}, "is not another string column"},
		"word column with a word column": {func(c []FactColumnDeclaration) {
			c[1].WordColumn = "theme"
		}, "declares a word column of its own"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			capability := columnWordsCapability()
			capability.SupportedSubjectKinds = []SubjectKind{contractsv1.ContextFabricSubjectTeam}
			capability.Fields[0].Columns = slices.Clone(capability.Fields[0].Columns)
			if err := validateFieldDeclarations(capability); err != nil {
				t.Fatalf("baseline: %v", err)
			}
			tc.change(capability.Fields[0].Columns)
			err := validateFieldDeclarations(capability)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validateFieldDeclarations() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
