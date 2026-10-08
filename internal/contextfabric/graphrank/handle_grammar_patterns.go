package graphrank

// HandleGrammarPattern names one entry of the closed handle-grammar registry
// and the subject kind its handles bind to.
type HandleGrammarPattern struct {
	ID   string
	Kind CensusKind
}

// HandleGrammarPatterns returns the registry's entries in registry order.
//
// It reads handleGrammarRegistry directly, so it cannot become a second
// definition of the grammar. It exists so a documentation generator in a
// package that must not link the engine's decision code can enumerate the
// registry from a test or a build-time tool; no decision calls it.
func HandleGrammarPatterns() []HandleGrammarPattern {
	patterns := make([]HandleGrammarPattern, 0, len(handleGrammarRegistry))
	for _, entry := range handleGrammarRegistry {
		patterns = append(patterns, HandleGrammarPattern{ID: entry.name, Kind: entry.kind})
	}
	return patterns
}
