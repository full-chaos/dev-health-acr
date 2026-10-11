package v1

import "strings"

// contextFabricSubjectKindNouns is the one source of the words a served
// sentence uses for a subject kind: the wire token (a snake_case identifier)
// never reaches prose a reader sees. Every kind in contextFabricSubjectKinds
// has an entry; a test pins that, and that no two nouns collide, so a sentence
// that interpolates a noun can be parsed back to exactly one kind.
// Nouns are derived from the wire token (underscores read as spaces), with
// overrides only where that reading is not the human name. Deriving rather
// than listing keeps this file free of table-name literals, which the
// physical-schema sweep reads as a second schema source.
var contextFabricSubjectKindNounOverrides = map[ContextFabricSubjectKind]string{
	ContextFabricSubjectCIRun:       "CI pipeline run",
	ContextFabricSubjectWorkItemRef: "work item reference",
}

func contextFabricSubjectKindSingular(kind ContextFabricSubjectKind) string {
	if noun, ok := contextFabricSubjectKindNounOverrides[kind]; ok {
		return noun
	}
	return strings.ReplaceAll(string(kind), "_", " ")
}

// ContextFabricSubjectKindNoun is the display noun for a kind: singular when
// count is 1, plural otherwise. A kind outside the vocabulary yields "".
func ContextFabricSubjectKindNoun(kind ContextFabricSubjectKind, count int) string {
	if !ValidContextFabricSubjectKind(kind) {
		return ""
	}
	singular := contextFabricSubjectKindSingular(kind)
	if count == 1 {
		return singular
	}
	if strings.HasSuffix(singular, "y") {
		return strings.TrimSuffix(singular, "y") + "ies"
	}
	return singular + "s"
}

// contextFabricSubjectKindFromNoun inverts ContextFabricSubjectKindNoun for the
// same count form, so a sentence parse recovers a vocabulary member or nothing.
func contextFabricSubjectKindFromNoun(noun string, count int) (ContextFabricSubjectKind, bool) {
	for _, kind := range contextFabricSubjectKinds {
		if ContextFabricSubjectKindNoun(kind, count) == noun {
			return kind, true
		}
	}
	return "", false
}
