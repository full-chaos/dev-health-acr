package v1

import (
	"regexp"
	"strconv"
)

// CHAOS-6558: the fact-row truncation disclosure.
//
// WHAT IT CLOSES. An answer whose claimed facts carry large row tables (a
// per-day series, a per-team breakdown) can overrun the response BYTE budget
// while sitting well inside the ITEM budget: rows are not charged items, so no
// item-axis lever could reduce them, and the stage-3 cohort retry kept every
// fact whose rows overran. The answer was refused with a 413 after two
// syntheses. Chris's ruling of 2026-09-24 (option b): cut the rows, serve the
// answer, and SAY SO -- from how many rows to how many, on which axis.
//
// INTERPOLATED WITH COUNTS, like the cohort-narrowing sentence beside it and
// for the same reason: the counts are the disclosure. The same hazard is
// bounded the same way -- a strict parse of a fixed grammar with bounded
// canonical integers, re-validated and re-composed, so a string that only
// looks like this sentence is not recognised.
//
// DETERMINISTIC. No model writes it; the engine composes it from the rows it
// cut.

const (
	contextFabricFactRowTruncationPrefix = "This answer shows "
	contextFabricFactRowTruncationOf     = " of the "
	contextFabricFactRowTruncationRows   = " table rows its facts carried, because the assembled answer exceeded the size budget: each table keeps at most "
	contextFabricFactRowTruncationRow    = " row"
	contextFabricFactRowTruncationRowsPl = " rows"
	// The suffix states WHICH END of every table was cut, as the whole
	// policy, so it is true of every table in the answer whichever rule that
	// table fell under: recency wins for a dated series (trends over
	// absolutes), rank wins for a ranked table, and only a table with
	// neither falls back to the order its source listed it in.
	contextFabricFactRowTruncationSuffix   = " (a dated time series keeps its most recent days, a ranking its highest-ranked rows, any other table its first rows in the order its source listed them). Ask about a shorter evidence window or allow a larger response budget to see the rest."
	contextFabricFactRowTruncationNumber   = `(0|[1-9][0-9]{0,8})`
	contextFabricFactRowTruncationMaxCount = 999999999
)

// ContextFabricFactRowTruncationLimitation composes the disclosure. THE SOLE
// COMPOSER: recognition is a parse over exactly what this writes.
//
// served and declared count claimed-fact TABLE rows (Rows and TimeSeriesRows
// together) across the whole answer; perTable is the per-table row cap the
// truncation applied. The second return is false -- and nothing is composed --
// unless the numbers describe a real truncation: at least one row kept per
// table, strictly fewer rows served than declared, and a cap that can have
// produced the served count (no more than served, and below declared).
func ContextFabricFactRowTruncationLimitation(served, declared, perTable int) (string, bool) {
	if !contextFabricFactRowTruncationValid(served, declared, perTable) {
		return "", false
	}
	unit := contextFabricFactRowTruncationRowsPl
	if perTable == 1 {
		unit = contextFabricFactRowTruncationRow
	}
	return contextFabricFactRowTruncationPrefix + strconv.Itoa(served) +
		contextFabricFactRowTruncationOf + strconv.Itoa(declared) +
		contextFabricFactRowTruncationRows + strconv.Itoa(perTable) + unit +
		contextFabricFactRowTruncationSuffix, true
}

func contextFabricFactRowTruncationValid(served, declared, perTable int) bool {
	for _, value := range []int{served, declared, perTable} {
		if value < 0 || value > contextFabricFactRowTruncationMaxCount {
			return false
		}
	}
	return perTable >= 1 && served >= perTable && served < declared
}

var contextFabricFactRowTruncationPattern = func() *regexp.Regexp {
	q := regexp.QuoteMeta
	number := contextFabricFactRowTruncationNumber
	return regexp.MustCompile(`^` + q(contextFabricFactRowTruncationPrefix) + number +
		q(contextFabricFactRowTruncationOf) + number +
		q(contextFabricFactRowTruncationRows) + number +
		`(` + q(contextFabricFactRowTruncationRowsPl) + `|` + q(contextFabricFactRowTruncationRow) + `)` +
		q(contextFabricFactRowTruncationSuffix) + `$`)
}()

// IsContextFabricFactRowTruncationLimitation reports whether a limitation is
// one ContextFabricFactRowTruncationLimitation could have composed: a parse,
// then a re-composition compared for equality.
func IsContextFabricFactRowTruncationLimitation(limitation string) bool {
	_, _, _, ok := ParseContextFabricFactRowTruncationLimitation(limitation)
	return ok
}

// ParseContextFabricFactRowTruncationLimitation returns the served, declared
// and per-table numbers of a limitation ContextFabricFactRowTruncationLimitation
// composed, and false for any other string.
func ParseContextFabricFactRowTruncationLimitation(limitation string) (served, declared, perTable int, ok bool) {
	match := contextFabricFactRowTruncationPattern.FindStringSubmatch(limitation)
	if match == nil {
		return 0, 0, 0, false
	}
	served, errServed := strconv.Atoi(match[1])
	declared, errDeclared := strconv.Atoi(match[2])
	perTable, errPerTable := strconv.Atoi(match[3])
	if errServed != nil || errDeclared != nil || errPerTable != nil {
		return 0, 0, 0, false
	}
	recomposed, composed := ContextFabricFactRowTruncationLimitation(served, declared, perTable)
	if !composed || recomposed != limitation {
		return 0, 0, 0, false
	}
	return served, declared, perTable, true
}
