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
	"unicode/utf8"
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

// applyPrefixWithBudget prepends prefix to text, truncating text FIRST so the
// prefixed result never exceeds maxRunes (round-1 review P1).
//
// Both ApplyDocumentPrefix and ApplyQueryPrefix already run downstream of
// Embed's own TruncateRunes(text, MaxTextRunes) call in embedChunk. Without
// this budgeting, a caller that truncated to MaxTextRunes and then prepended
// a prefix would hand Embed a text LONGER than MaxTextRunes, and Embed's
// truncation would silently cut retrieval-bearing runes off the TAIL to make
// room for the prefix it never knew was there -- worse, the two prefixes in
// PrefixPair differ in length ("search_document: " vs "search_query: "), so
// the two arms would lose a different number of runes for the same
// underlying text, which is exactly the kind of arm-specific divergence the
// embed-text spec's byte-identity design (§0(c)) exists to rule out.
//
// Budgeting the PREFIX side of the arithmetic instead makes the guarantee
// unconditional: whatever text a caller hands in, and whether or not it was
// pre-truncated, the combined result is provably <= maxRunes, so Embed's own
// truncation is a no-op on the text this package composes. A negative budget
// (a prefix longer than maxRunes) clamps to zero rather than panicking --
// Config.validate's 2,000-rune floor and this package's short, fixed
// prefixes make that case unreachable in practice, but a defensive clamp
// costs nothing.
func applyPrefixWithBudget(prefix, text string, maxRunes int) string {
	if prefix == "" {
		return text
	}
	budget := maxRunes - utf8.RuneCountInString(prefix)
	if budget < 0 {
		budget = 0
	}
	// Idempotency guard (round-1 review P2), round-2 correction: a second
	// application of the same prefix must be a no-op, not
	// "prefix + prefix + text" -- but "already carries this prefix" is NOT
	// license to skip the budget too. An already-prefixed input can still be
	// over-budget (e.g. handed in from outside this package's own previous
	// call), and returning it unchanged in that case would let Embed's
	// TruncateRunes fire on it after all, silently cutting from the tail --
	// exactly the failure this function exists to prevent, just reached via
	// the guard meant to prevent a DIFFERENT failure. So the already-prefixed
	// branch still re-applies the SAME budget to the REMAINDER after the
	// prefix, not to the whole string: split the prefix off, truncate what's
	// left to budget, rejoin. Applied to this function's own output, that
	// truncation is always a no-op (the remainder is already <= budget),
	// which is exactly what makes this a true idempotent fixed point rather
	// than a skip that happens to look like one.
	//
	// The guard's theoretical false positive -- real text that legitimately
	// BEGINS with the literal prefix string, e.g. a PR titled exactly
	// "search_document: implement the indexer" -- is accepted. Budget-fitting
	// that text once is indistinguishable from budget-fitting a genuinely
	// re-applied prefix, and a wiring or composition-tag mistake causing a
	// real double-application is both more likely and more damaging than
	// this coincidence.
	if strings.HasPrefix(text, prefix) {
		remainder := strings.TrimPrefix(text, prefix)
		return prefix + TruncateRunes(remainder, budget)
	}
	return prefix + TruncateRunes(text, budget)
}
