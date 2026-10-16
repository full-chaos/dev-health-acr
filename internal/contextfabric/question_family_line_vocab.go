package contextfabric

// The closed vocabularies of the question family resolution line, exported so
// the event specification reads them from the producer's own constants.

// FamilyPrecedenceRowVocabulary is the closed set of precedence rows a sample row carries, with the empty row of a sample that fired none.
func FamilyPrecedenceRowVocabulary() []string {
	return []string{
		"", string(FamilyPrecedenceRowGroupKind), string(FamilyPrecedenceRowScopeAnchor), string(FamilyPrecedenceRowComparison),
		string(FamilyPrecedenceRowCohortShape), string(FamilyPrecedenceRowSingleSubject), string(FamilyPrecedenceRowNone),
	}
}

// FamilyIncompatibilityReasonVocabulary is the closed set of reasons a sample's attempted family was not used, with the empty reason of a sample whose family was used.
func FamilyIncompatibilityReasonVocabulary() []string {
	return []string{
		"", string(FamilyIncompatibilityUnrecognized), string(FamilyIncompatibilityStructuralMismatch), string(FamilyIncompatibilityUnreachable),
	}
}
