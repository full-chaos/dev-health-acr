package contextfabric

import (
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
// vocabulary only from the closed column set. The results are in table column
// order (key, then observations) and nil when the table states none.
func DeclaredColumnWords(capability FactCapability, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) ([]contractsv1.ContextFabricColumnVocabularyDeclaration, []contractsv1.ContextFabricWordColumnDeclaration) {
	declaration, ok := capability.FieldDeclaration(table.Field, subject)
	if !ok {
		return nil, nil
	}
	var vocabularies []contractsv1.ContextFabricColumnVocabularyDeclaration
	var words []contractsv1.ContextFabricWordColumnDeclaration
	for _, name := range slices.Concat(table.Key, table.Observations) {
		column, ok := declaration.Column(name)
		if !ok {
			continue
		}
		if column.Vocabulary != "" && contractsv1.ValidContextFabricColumnVocabulary(column.Vocabulary) {
			vocabularies = append(vocabularies, contractsv1.ContextFabricColumnVocabularyDeclaration{Column: name, Vocabulary: column.Vocabulary})
		}
		if column.WordColumn != "" && column.WordColumn != name && table.HasObservation(column.WordColumn) {
			words = append(words, contractsv1.ContextFabricWordColumnDeclaration{Column: name, WordColumn: column.WordColumn})
		}
	}
	return vocabularies, words
}

// factColumnWords states the column words of one claimed table. See
// DeclaredColumnWords.
type factColumnWords func(kind FactKind, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) ([]contractsv1.ContextFabricColumnVocabularyDeclaration, []contractsv1.ContextFabricWordColumnDeclaration)

// declaredColumnWordsOf reads column words off the fact declarations of every
// kind. With no declarations no table states any.
func declaredColumnWordsOf(capabilities []FactCapability) factColumnWords {
	byKind := make(map[FactKind]FactCapability, len(capabilities))
	for _, capability := range capabilities {
		byKind[capability.Kind] = capability
	}
	return func(kind FactKind, subject SubjectKind, table contractsv1.ContextFabricClaimedFactTable) ([]contractsv1.ContextFabricColumnVocabularyDeclaration, []contractsv1.ContextFabricWordColumnDeclaration) {
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
	var vocabularies []contractsv1.ContextFabricColumnVocabularyDeclaration
	var words []contractsv1.ContextFabricWordColumnDeclaration
	if wordsOf != nil {
		vocabularies, words = wordsOf(claim.Kind, claim.Subject.Kind, *table)
	}
	if slices.Equal(vocabularies, table.ColumnVocabularies) && slices.Equal(words, table.WordColumns) {
		return table
	}
	stated := *table
	stated.ColumnVocabularies = vocabularies
	stated.WordColumns = words
	return &stated
}
