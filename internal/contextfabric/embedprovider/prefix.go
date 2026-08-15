// Model-family task prefixes (CHAOS-3836; embed-text spec §5 L6 / §6 T6).
//
// Some embedding models are trained on an ASYMMETRIC task contract: the text
// embedded for storage (a "document") and the text embedded for a search (a
// "query") must each carry a different fixed marker string, or retrieval
// quality measurably degrades. nomic-embed-text requires
// "search_document: " / "search_query: "; e5-family models require
// "passage: " / "query: ". OpenAI-shaped models (text-embedding-3-large and
// siblings) need neither.
//
// Selection is EXPLICIT CONFIGURATION, never inferred from the model id
// string. A model id is operator-chosen free text -- this package's own doc
// comment promises no vendor or model is ever hardcoded -- so sniffing it for
// a substring like "nomic" would silently mismatch a differently-named
// deployment of the same weights, or silently match an unrelated model whose
// id happens to contain the substring. That is exactly the failure shape the
// embed-text spec's §4 Layer C already treats every semantic input as a
// fail-closed, spelled-out setting to avoid. An unrecognized family is a
// configuration error, not a silent fallback to PrefixFamilyNone.
package embedprovider

import (
	"fmt"
	"sort"
	"strings"
)

// PrefixFamily names one closed-vocabulary task-prefix convention.
type PrefixFamily string

const (
	// PrefixFamilyNone applies no prefix to either side. This is the
	// default: an unconfigured deployment's retrieval text is byte-for-byte
	// what it always was.
	PrefixFamilyNone PrefixFamily = "none"
	// PrefixFamilyNomic applies nomic-embed-text's required asymmetric
	// prefixes. Retrieval quality measurably degrades without them.
	PrefixFamilyNomic PrefixFamily = "nomic"
)

// PrefixPair is the asymmetric task-prefix pair for one PrefixFamily.
type PrefixPair struct {
	// Document is prepended to text embedded for storage -- the subject
	// side, applied at projection time.
	Document string
	// Query is prepended to text embedded for a search -- the question
	// side, applied at resolution time.
	Query string
}

// knownPrefixFamilies is the closed vocabulary this package recognizes.
// Adding a family (e5, ...) is a one-entry change here, with a corresponding
// config test; nothing else in this package branches on family identity.
var knownPrefixFamilies = map[PrefixFamily]PrefixPair{
	PrefixFamilyNone:  {},
	PrefixFamilyNomic: {Document: "search_document: ", Query: "search_query: "},
}

// resolvedPrefixFamily normalizes the zero value to PrefixFamilyNone. Every
// reader of Config.PrefixFamily (validate, New) goes through this, so the
// empty string and PrefixFamilyNone are the same configuration everywhere,
// never just in one of the two places that matters.
func (c Config) resolvedPrefixFamily() PrefixFamily {
	if c.PrefixFamily == "" {
		return PrefixFamilyNone
	}
	return c.PrefixFamily
}

func validPrefixFamily(family PrefixFamily) bool {
	_, ok := knownPrefixFamilies[family]
	return ok
}

// sortedPrefixFamilyNames renders the closed vocabulary deterministically,
// for a stable, testable error message.
func sortedPrefixFamilyNames() string {
	names := make([]string, 0, len(knownPrefixFamilies))
	for family := range knownPrefixFamilies {
		names = append(names, string(family))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func prefixFamilyError(family PrefixFamily) error {
	return fmt.Errorf("embedder prefix family %q is not recognized; valid values: %s", family, sortedPrefixFamilyNames())
}

// applyPrefix prepends prefix to text, unless prefix is empty.
func applyPrefix(prefix, text string) string {
	if prefix == "" {
		return text
	}
	return prefix + text
}
