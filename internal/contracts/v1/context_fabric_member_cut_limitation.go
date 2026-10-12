package v1

import (
	"regexp"
	"strconv"
)

// The member-cut disclosure: the sentence the model-free item lever writes
// when it drops listed members to fit the item budget. It states the rule the
// cut followed -- members no claim, driver or finding names go first, from the
// end of the list -- so the cut is never read as a pure rank cut, and states
// how many of how many.
//
// Composed and recognised as the path-drop sentence beside it: a strict parse
// of a fixed grammar, then a re-composition compared for equality.

const (
	contextFabricMemberCutPrefix = "This answer leaves out "
	contextFabricMemberCutOf     = " of the "
	contextFabricMemberCutRule   = " listed members, because the assembled answer exceeded the item budget: members that no claim, driver or finding names were dropped first, "
	contextFabricMemberCutByRank = "the lowest attention rank first"
	contextFabricMemberCutByEnd  = "from the end of the list"
	contextFabricMemberCutAsk    = ". Ask a narrower question or allow a larger response budget to see the rest."
	contextFabricMemberCutNumber = `(0|[1-9][0-9]{0,8})`
	contextFabricMemberCutMax    = 999999999
)

// ContextFabricMemberCutLimitation composes the disclosure. THE SOLE COMPOSER.
// ranked says which order the cut followed: the lowest attention rank first for
// a ranked list, the end of the list otherwise. The second return is false
// unless the numbers describe a real cut.
func ContextFabricMemberCutLimitation(cut, listed int, ranked bool) (string, bool) {
	if cut < 1 || listed > contextFabricMemberCutMax || cut >= listed {
		return "", false
	}
	order := contextFabricMemberCutByEnd
	if ranked {
		order = contextFabricMemberCutByRank
	}
	return contextFabricMemberCutPrefix + strconv.Itoa(cut) + contextFabricMemberCutOf + strconv.Itoa(listed) + contextFabricMemberCutRule + order + contextFabricMemberCutAsk, true
}

var contextFabricMemberCutPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(contextFabricMemberCutPrefix) + contextFabricMemberCutNumber +
	regexp.QuoteMeta(contextFabricMemberCutOf) + contextFabricMemberCutNumber + regexp.QuoteMeta(contextFabricMemberCutRule) +
	`(` + regexp.QuoteMeta(contextFabricMemberCutByRank) + `|` + regexp.QuoteMeta(contextFabricMemberCutByEnd) + `)` + regexp.QuoteMeta(contextFabricMemberCutAsk) + `$`)

// IsContextFabricMemberCutLimitation reports whether a limitation is one
// ContextFabricMemberCutLimitation could have composed.
func IsContextFabricMemberCutLimitation(limitation string) bool {
	match := contextFabricMemberCutPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	cut, errCut := strconv.Atoi(match[1])
	listed, errListed := strconv.Atoi(match[2])
	if errCut != nil || errListed != nil {
		return false
	}
	recomposed, ok := ContextFabricMemberCutLimitation(cut, listed, match[3] == contextFabricMemberCutByRank)
	return ok && recomposed == limitation
}
