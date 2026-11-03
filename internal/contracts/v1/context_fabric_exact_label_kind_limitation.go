package v1

// CHAOS-12600: the disclosure a comparison owes when the kind of its two sides
// was resolved from their names rather than stated: each name matched exactly
// one subject by its full label, all of one kind, and no subject of another
// kind carried that label. The answer says how it read the sides, so a reader
// who meant another kind can say so.

var exactLabelKindPlurals = map[ContextFabricSubjectKind]string{
	ContextFabricSubjectRepository: "repositories",
	ContextFabricSubjectProject:    "projects",
	ContextFabricSubjectTeam:       "teams",
}

// ContextFabricExactLabelKindLimitation is the fixed sentence for kind, or ""
// for a kind the rule does not resolve (only repository, project and team).
func ContextFabricExactLabelKindLimitation(kind ContextFabricSubjectKind) string {
	plural, ok := exactLabelKindPlurals[kind]
	if !ok {
		return ""
	}
	return "Both sides were read as " + plural + ": each name matched exactly one " + string(kind) + " by its full name, and no subject of another kind."
}

// IsContextFabricExactLabelKindLimitation reports whether a limitation is one
// of the sentences above.
func IsContextFabricExactLabelKindLimitation(limitation string) bool {
	for kind := range exactLabelKindPlurals {
		if limitation == ContextFabricExactLabelKindLimitation(kind) {
			return true
		}
	}
	return false
}
