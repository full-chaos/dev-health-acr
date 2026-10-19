package v1

import (
	"regexp"
	"strconv"
)

// CHAOS-6786: the outcome-row eviction disclosure.
//
// At the outcome row cap a stage-3 lever has no room for the row that
// discloses its cut. Rather than refuse a servable answer, the lever evicts
// the lowest-ranked row, and this sentence says how many rows the document
// has had evicted. Composed and recognised like the path-drop sentence: a
// strict parse of a fixed grammar, then a re-composition compared for
// equality, so no composer displaces it as a model caveat.

const (
	contextFabricOutcomeEvictionPrefix = "This answer's requirement outcome list was full, so "
	contextFabricOutcomeEvictionSuffix = " outcome row(s) with the least reader impact were evicted to make room for the row that discloses a size-budget cut."
	contextFabricOutcomeEvictionMax    = 999999999
)

// ContextFabricOutcomeEvictionLimitation composes the disclosure. THE SOLE
// COMPOSER. The second return is false unless evicted is a real count.
func ContextFabricOutcomeEvictionLimitation(evicted int) (string, bool) {
	if evicted < 1 || evicted > contextFabricOutcomeEvictionMax {
		return "", false
	}
	return contextFabricOutcomeEvictionPrefix + strconv.Itoa(evicted) + contextFabricOutcomeEvictionSuffix, true
}

var contextFabricOutcomeEvictionPattern = regexp.MustCompile(`^` + regexp.QuoteMeta(contextFabricOutcomeEvictionPrefix) +
	`([1-9][0-9]{0,8})` + regexp.QuoteMeta(contextFabricOutcomeEvictionSuffix) + `$`)

// ParseContextFabricOutcomeEvictionLimitation returns the evicted count of a
// limitation ContextFabricOutcomeEvictionLimitation composed.
func ParseContextFabricOutcomeEvictionLimitation(limitation string) (evicted int, ok bool) {
	match := contextFabricOutcomeEvictionPattern.FindStringSubmatch(limitation)
	if match == nil {
		return 0, false
	}
	evicted, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, false
	}
	recomposed, composed := ContextFabricOutcomeEvictionLimitation(evicted)
	if !composed || recomposed != limitation {
		return 0, false
	}
	return evicted, true
}

// IsContextFabricOutcomeEvictionLimitation reports whether a limitation is
// one ContextFabricOutcomeEvictionLimitation could have composed.
func IsContextFabricOutcomeEvictionLimitation(limitation string) bool {
	_, ok := ParseContextFabricOutcomeEvictionLimitation(limitation)
	return ok
}
