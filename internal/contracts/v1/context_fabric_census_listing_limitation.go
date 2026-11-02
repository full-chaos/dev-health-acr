package v1

import (
	"strconv"
	"strings"
)

// CHAOS-12467: the disclosure of a census count a turn could not list in full. A
// census that counted satisfiers must never read as "found nothing".
//
// A COUNT AND NEVER A NAME. It counts what the census read, which for a
// restricted caller holds only subjects its grant admits, so no subject outside
// the grant can change it. It is one text for every cause: over the set budget, a
// set that did not close, a narrowing that could not decide, an id the graph or
// the gate refused. The strict parse below is what keeps the digits from widening
// what a model caveat can impersonate.
//
// The sentences say "you": a web user has no credential in view, and "you" reads
// true for an MCP caller too.
const (
	contextFabricCensusUnlistedOne     = "1 subject matches this question in what you can read, and it could not be listed, so no subject was confirmed and no canonical facts were read. Name the repository to narrow it."
	contextFabricCensusUnlistedMany    = " subjects match this question in what you can read, and they could not be listed, so no subject was confirmed and no canonical facts were read. Name the repository to narrow them."
	contextFabricCensusListedManyHead  = " subjects match this question in what you can read. Only "
	contextFabricCensusListedTailOne   = " of them is listed. Name the repository to narrow them."
	contextFabricCensusListedTailMany  = " of them are listed. Name the repository to narrow them."
	contextFabricCensusLimitationMaxOf = 999999999
)

// CHAOS-12498: a complete census count of zero on the question's handle. It is a
// proven absence inside what the caller can read, and nothing more, so neither
// sentence may read as "does not exist". A subject outside the grant gives the same
// count, so each is one text for no such subject and a subject the caller cannot
// read, and tells the caller nothing about what lies outside.
const (
	// ContextFabricCensusAbsentLimitation is served when the count covers all the
	// caller can read.
	ContextFabricCensusAbsentLimitation = "No subject with this identifier is in what you can read, so no subject was confirmed and no canonical facts were read. This does not show that it does not exist. Check the identifier."
	// ContextFabricCensusAbsentInScopeLimitation is served when a requested
	// repository or an anchor narrowed the count: the subject may be elsewhere in
	// what the caller can read.
	ContextFabricCensusAbsentInScopeLimitation = "No subject with this identifier is in what you can read of the requested scope, so no subject was confirmed and no canonical facts were read. This does not show that it does not exist. Check the identifier, or widen the scope."
)

// ContextFabricCensusUnlistedLimitation composes the sentence for a count with
// nothing listed. THE SOLE COMPOSER of it: recognition is a parse over exactly
// what this writes. A count below one has nothing to state, and yields "".
func ContextFabricCensusUnlistedLimitation(count int) string {
	switch {
	case count < 1:
		return ""
	case count == 1:
		return contextFabricCensusUnlistedOne
	}
	return strconv.Itoa(count) + contextFabricCensusUnlistedMany
}

// ContextFabricCensusListedLimitation composes the sentence for a count of which
// only some are listed. THE SOLE COMPOSER of it. A listing that holds nothing, or
// all of the count, has nothing to state here, and yields "".
func ContextFabricCensusListedLimitation(count, listed int) string {
	if listed < 1 || listed >= count {
		return ""
	}
	tail := contextFabricCensusListedTailMany
	if listed == 1 {
		tail = contextFabricCensusListedTailOne
	}
	return strconv.Itoa(count) + contextFabricCensusListedManyHead + strconv.Itoa(listed) + tail
}

// IsContextFabricCensusAbsenceLimitation reports a sentence a complete zero owes,
// the restricted work item's included: its text is the link sentence.
func IsContextFabricCensusAbsenceLimitation(limitation string) bool {
	switch limitation {
	case ContextFabricCensusAbsentLimitation, ContextFabricCensusAbsentInScopeLimitation, ContextFabricWorkItemNoGrantedLinkLimitation:
		return true
	}
	return false
}

// IsContextFabricCensusListingLimitation reports whether a limitation is one this
// file composed.
func IsContextFabricCensusListingLimitation(limitation string) bool {
	switch limitation {
	case contextFabricCensusUnlistedOne, ContextFabricCensusAbsentLimitation, ContextFabricCensusAbsentInScopeLimitation:
		return true
	}
	if digits, ok := strings.CutSuffix(limitation, contextFabricCensusUnlistedMany); ok {
		count, valid := censusLimitationCount(digits)
		return valid && count >= 2
	}
	countDigits, rest, ok := strings.Cut(limitation, contextFabricCensusListedManyHead)
	if !ok {
		return false
	}
	count, valid := censusLimitationCount(countDigits)
	if !valid {
		return false
	}
	if digits, ok := strings.CutSuffix(rest, contextFabricCensusListedTailOne); ok {
		listed, valid := censusLimitationCount(digits)
		return valid && listed == 1 && count >= 2
	}
	digits, ok := strings.CutSuffix(rest, contextFabricCensusListedTailMany)
	if !ok {
		return false
	}
	listed, valid := censusLimitationCount(digits)
	return valid && listed >= 2 && listed < count
}

// censusLimitationCount parses a canonical decimal count inside the bound.
func censusLimitationCount(digits string) (int, bool) {
	count, err := strconv.Atoi(digits)
	return count, err == nil && count >= 1 && count <= contextFabricCensusLimitationMaxOf && strconv.Itoa(count) == digits
}
