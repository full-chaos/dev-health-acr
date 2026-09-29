package contextfabric

// DirectReadSources returns the graph and the fact registry this Engine
// reads, for the hosted runtime to build the direct-read subject gate and
// its fact reader from (internal/contextfabric/directread, CHAOS-7071). The
// direct data tools read the SAME graph and registry the engine does; they
// never hold the registry itself -- only a directread.FactReader, which
// reads gate-admitted subjects only (the construction rule
// TestNoDirectRegistryReadOutsideContextFabric pins). The registry is
// returned WITHOUT the engine's embedded-subject gate: the direct tools apply
// the same filter through their own SubjectGate (directread.EmbeddedSubjectGate).
func (e *Engine) DirectReadSources() (GraphReader, CanonicalFactReader) {
	if e == nil {
		return nil, nil
	}
	return e.graph, e.directFacts
}
