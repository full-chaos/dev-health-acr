package v1

import (
	"strconv"
	"strings"
)

// CHAOS-12360: the disclosure of a match a requested repository narrowing
// refused. A term whose only matches lie outside the requested repositories
// resolves to nothing, and a silent terminal told the caller nothing could be
// found. The narrowing stands, and the answer says it refused a real match.
//
// A COUNT AND NEVER A NAME. It serves only an unrestricted principal, whose
// grant covers the subject, because for any other caller it is an existence
// oracle. The strict parse below is what keeps the digits from widening what a
// model caveat can impersonate.
const (
	contextFabricOutsideRequestedRepositoriesOne   = "1 subject matched outside the requested repositories."
	contextFabricOutsideRequestedRepositoriesMany  = " subjects matched outside the requested repositories."
	contextFabricOutsideRequestedRepositoriesMaxOf = 999999999
)

// ContextFabricOutsideRequestedRepositoriesLimitation composes the sentence.
// THE SOLE COMPOSER: recognition is a parse over exactly what this writes. A
// count below one has nothing to state, and yields "".
func ContextFabricOutsideRequestedRepositoriesLimitation(count int) string {
	switch {
	case count < 1:
		return ""
	case count == 1:
		return contextFabricOutsideRequestedRepositoriesOne
	}
	return strconv.Itoa(count) + contextFabricOutsideRequestedRepositoriesMany
}

// IsContextFabricOutsideRequestedRepositoriesLimitation reports whether a
// limitation is one this file composed: the singular sentence, or a canonical
// decimal count from 2 to the bound before the plural.
func IsContextFabricOutsideRequestedRepositoriesLimitation(limitation string) bool {
	if limitation == contextFabricOutsideRequestedRepositoriesOne {
		return true
	}
	digits, ok := strings.CutSuffix(limitation, contextFabricOutsideRequestedRepositoriesMany)
	if !ok {
		return false
	}
	count, err := strconv.Atoi(digits)
	return err == nil && count >= 2 && count <= contextFabricOutsideRequestedRepositoriesMaxOf && strconv.Itoa(count) == digits
}
