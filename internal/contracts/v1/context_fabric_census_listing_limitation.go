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
const (
	contextFabricCensusUnlistedOne     = "1 subject matches this question in what this credential can read, and it could not be listed, so no subject was confirmed and no canonical facts were read. Name the repository to narrow it."
	contextFabricCensusUnlistedMany    = " subjects match this question in what this credential can read, and they could not be listed, so no subject was confirmed and no canonical facts were read. Name the repository to narrow them."
	contextFabricCensusListedManyHead  = " subjects match this question in what this credential can read. Only "
	contextFabricCensusListedTailOne   = " of them is listed. Name the repository to narrow them."
	contextFabricCensusListedTailMany  = " of them are listed. Name the repository to narrow them."
	contextFabricCensusLimitationMaxOf = 999999999
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

// IsContextFabricCensusListingLimitation reports whether a limitation is one this
// file composed.
func IsContextFabricCensusListingLimitation(limitation string) bool {
	if limitation == contextFabricCensusUnlistedOne {
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
