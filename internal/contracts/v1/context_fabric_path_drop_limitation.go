package v1

import (
	"regexp"
	"strconv"
)

// CHAOS-6558: the relationship-path drop disclosure.
//
// WHAT IT CLOSES. On prod, "Which teams need attention over the last 30
// days?" assembled 79,376 bytes against a 65,536-byte ceiling, and 68,244 of
// them were NOT fact-table rows: cutting every table to one row still left
// 69,712 bytes, the cohort retry could not reduce it, and the caller got a
// 413 for a question the engine can answer. The byte axis now has a second
// lever after the row cut: relationship paths are dropped in a defined order
// (paths no driver cites first, then cited paths, each from the end of the
// list), and this sentence says so -- from how many paths to how many.
//
// Composed and recognised exactly as the fact-row truncation sentence beside
// it: a strict parse of a fixed grammar, then a re-composition compared for
// equality.

const (
	contextFabricPathDropPrefix = "This answer shows "
	contextFabricPathDropOf     = " of the "
	contextFabricPathDropPaths  = " relationship paths it found, because the assembled answer exceeded the size budget after its fact tables were cut as far as they go (paths no driver cites are dropped first, then cited paths, each from the end of the list, and a dropped path's citation is removed from its driver). Ask a narrower question or allow a larger response budget to see the rest."
	contextFabricPathDropNumber = `(0|[1-9][0-9]{0,8})`
	contextFabricPathDropMax    = 999999999
)

// ContextFabricPathDropLimitation composes the disclosure. THE SOLE COMPOSER.
// The second return is false unless the numbers describe a real drop.
func ContextFabricPathDropLimitation(served, declared int) (string, bool) {
	if served < 0 || declared > contextFabricPathDropMax || served >= declared {
		return "", false
	}
	return contextFabricPathDropPrefix + strconv.Itoa(served) +
		contextFabricPathDropOf + strconv.Itoa(declared) +
		contextFabricPathDropPaths, true
}

var contextFabricPathDropPattern = func() *regexp.Regexp {
	q := regexp.QuoteMeta
	number := contextFabricPathDropNumber
	return regexp.MustCompile(`^` + q(contextFabricPathDropPrefix) + number +
		q(contextFabricPathDropOf) + number + q(contextFabricPathDropPaths) + `$`)
}()

// IsContextFabricPathDropLimitation reports whether a limitation is one
// ContextFabricPathDropLimitation could have composed.
func IsContextFabricPathDropLimitation(limitation string) bool {
	match := contextFabricPathDropPattern.FindStringSubmatch(limitation)
	if match == nil {
		return false
	}
	served, errServed := strconv.Atoi(match[1])
	declared, errDeclared := strconv.Atoi(match[2])
	if errServed != nil || errDeclared != nil {
		return false
	}
	recomposed, ok := ContextFabricPathDropLimitation(served, declared)
	return ok && recomposed == limitation
}
