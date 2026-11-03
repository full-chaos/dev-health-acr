package contextfabric

import (
	"maps"
	"slices"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// DeclaredColumnWords states where the words of a claimed table's cells come
// from (CHAOS-12639), from capability's own declaration of field on a fact about
// subject: the vocabulary of each token column, and the word column of each id
// column. The producer that owns a column declares both there, and that
// declaration is the only source of a stated one. Only columns the claimed table
// carries are named (a direct-only column a declaration names is not in the
// answer), a word column only when the table carries it as an observation, and a
// vocabulary only from the closed column set. Each map is nil when the table
// states none.
func DeclaredColumnWords(capability FactCapability, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) (map[string]contractsv1.ContextFabricVocabulary, map[string]string) {
	declaration, ok := capability.FieldDeclaration(table.Field, subject)
	if !ok {
		return nil, nil
	}
	var vocabularies map[string]contractsv1.ContextFabricVocabulary
	var words map[string]string
	for _, name := range slices.Concat(table.Key, table.Observations) {
		column, ok := declaration.Column(name)
		if !ok {
			continue
		}
		if column.Vocabulary != "" && contractsv1.ValidContextFabricColumnVocabulary(column.Vocabulary) {
			if vocabularies == nil {
				vocabularies = map[string]contractsv1.ContextFabricVocabulary{}
			}
			vocabularies[name] = column.Vocabulary
		}
		if column.WordColumn != "" && column.WordColumn != name && table.HasObservation(column.WordColumn) {
			if words == nil {
				words = map[string]string{}
			}
			words[name] = column.WordColumn
		}
	}
	return vocabularies, words
}

// factColumnWords states the column words of one claimed table. See
// DeclaredColumnWords.
type factColumnWords func(kind FactKind, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) (map[string]contractsv1.ContextFabricVocabulary, map[string]string)

// declaredColumnWordsOf reads column words off the fact declarations of every
// kind. With no declarations no table states any.
func declaredColumnWordsOf(capabilities []FactCapability) factColumnWords {
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(kind FactKind, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) (map[string]contractsv1.ContextFabricVocabulary, map[string]string) {
		return DeclaredColumnWords(byKind[kind], subject, table)
	}
}

// withDeclaredColumnWords states the column words of every claimed table
// (CHAOS-12639). It REPLACES what a table carries, as withDeclaredMeasureUnits
// does: the words come from the declarations alone, so a vocabulary or word
// column no producer declared never reaches the wire, whoever wrote it.
// finalizeResult runs it on every pass, and it gives the same answer each time.
//
// A table is copied before it changes, so a table another document shares is
// never rewritten.
func withDeclaredColumnWords(claims []ClaimedFact, wordsOf factColumnWords) []ClaimedFact {
	if len(claims) == 0 {
		return claims
	}
	out := make([]ClaimedFact, len(claims))
	for index, claim := range claims {
		claim.Table = tableWithDeclaredColumnWords(claim.Table, claim, wordsOf)
		claim.TimeSeriesTable = tableWithDeclaredColumnWords(claim.TimeSeriesTable, claim, wordsOf)
		out[index] = claim
	}
	return out
}

func tableWithDeclaredColumnWords(table *contractsv1.ContextFabricClaimedFactTable, claim ClaimedFact, wordsOf factColumnWords) *contractsv1.ContextFabricClaimedFactTable {
	if table == nil {
		return nil
	}
	var vocabularies map[string]contractsv1.ContextFabricVocabulary
	var words map[string]string
	if wordsOf != nil {
		vocabularies, words = wordsOf(claim.Kind, claim.Subject.Kind, *table)
	}
	if maps.Equal(vocabularies, table.ColumnVocabularies) && maps.Equal(words, table.WordColumns) &&
		(vocabularies == nil) == (table.ColumnVocabularies == nil) && (words == nil) == (table.WordColumns == nil) {
		return table
	}
	stated := *table
	stated.ColumnVocabularies = vocabularies
	stated.WordColumns = words
	return &stated
}
