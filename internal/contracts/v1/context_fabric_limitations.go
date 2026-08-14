package v1

// The retrieval-degradation limitation lives here, in the contract, rather
// than in the engine that writes it (CHAOS-3746, option (a)).
//
// It is written by internal/contextfabric and read by
// internal/contextfabric/answerprojection, and the projection may not
// import the engine: answerprojection is import-pure by constraint --
// standard library and this package, nothing else -- so that both the
// hosted API and the MCP sidecar can call it. TestPackageImportsStayPure
// enforces that, which leaves exactly two options for a string both sides
// must recognise: restate it on the read side, or move it to the contract
// they already share. Restating it is the anchor-drift class this codebase
// keeps closing; the string is part of what an answer MEANS, not an
// implementation detail of how one gets composed.

// ContextFabricRetrievalDegradedLimitation is the fixed, non-interpolated
// limitation an investigation carries when a retrieval mechanism was
// unavailable while the answer was produced.
//
// It names no mechanism, no provider, no model, and no error text. A
// limitation is answer-facing prose, and every cause -- an embed timeout,
// an unreachable embedder, a server that served the wrong model, a
// fenced-off stale index -- has the same consequence for a reader:
// retrieval saw less than it should have. Operator-facing detail belongs in
// telemetry, which already receives it.
//
// The phrasing describes the ANSWER'S PROVENANCE ("when this answer was
// produced"), not the current request, and that is load-bearing rather than
// stylistic: a REUSED answer carries this limitation forward verbatim from
// the run that produced it, and the earlier wording pointed ambiguously at
// the current request in exactly that case.
const ContextFabricRetrievalDegradedLimitation = "One retrieval mechanism was unavailable when this answer was produced, so fewer candidate subjects may have been considered than usual."

// ContextFabricRetrievalDegradedLimitationLegacy is the wording used before
// the phrasing above replaced it.
//
// BOTH STRINGS EXIST IN THE WILD, permanently. A
// ContextFabricInvestigationResult is immutable and CHAOS-3782's answer
// reuse keys on its stored bytes, so results written before the change keep
// this spelling verbatim -- nothing rewrites a stored row, and nothing may
// treat one as malformed.
const ContextFabricRetrievalDegradedLimitationLegacy = "One retrieval mechanism was unavailable for this investigation, so fewer candidate subjects may have been considered than usual."

// IsContextFabricRetrievalDegradedLimitation reports whether a limitation
// string is either spelling.
//
// It exists so no caller compares against ONE constant and silently stops
// recognising answers written by the other.
func IsContextFabricRetrievalDegradedLimitation(limitation string) bool {
	return limitation == ContextFabricRetrievalDegradedLimitation ||
		limitation == ContextFabricRetrievalDegradedLimitationLegacy
}
