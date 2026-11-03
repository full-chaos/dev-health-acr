package v1

// CHAOS-12557 (lead ruling (c)): a question that names several subjects by their
// identifiers beside one repository, and states no kind, keeps the repository's
// answer. The answer says it does not answer for them. Nothing was read for the
// identifiers, so the sentence claims nothing more. One fixed text: no
// identifier, label or count is interpolated.
const ContextFabricSeveralHandlesUnansweredLimitation = "The question names more than one subject by its identifier, so this answer is about the named repository and does not answer for each of them. Ask about one at a time."

// IsContextFabricUnansweredHandlesLimitation reports whether a limitation is the
// sentence above.
func IsContextFabricUnansweredHandlesLimitation(limitation string) bool {
	return limitation == ContextFabricSeveralHandlesUnansweredLimitation
}
