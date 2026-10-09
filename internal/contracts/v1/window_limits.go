package v1

import "fmt"

// WindowBeyondKindMaxReason is the invalid_request reason of a window wider
// than the kinds asked allow. It travels with error.details.max_days.
const WindowBeyondKindMaxReason = "window_beyond_kind_max"

// DefaultMaxRangeDays bounds a range or trailing window for every kind that
// declares no larger maximum in maxRangeDaysByKind.
const DefaultMaxRangeDays = 60

// maxRangeDaysByKind declares the kinds whose stored rows serve a longer
// window in one read. investment reads work_unit_investments once however
// long the window is, so its cost does not grow with the span. A kind is
// listed here only when its rows support it; there is no blanket lift.
var maxRangeDaysByKind = map[ContextFabricFactKind]int{
	ContextFabricFactInvestment: 365,
}

// MaxRangeDaysFor is the widest range or trailing window one read may take
// for kinds: the smallest maximum among them, so a mixed read is held to its
// most limited kind.
func MaxRangeDaysFor(kinds []ContextFabricFactKind) int {
	widest := 0
	for _, kind := range kinds {
		limit := DefaultMaxRangeDays
		if declared, ok := maxRangeDaysByKind[kind]; ok {
			limit = declared
		}
		if widest == 0 || limit < widest {
			widest = limit
		}
	}
	if widest == 0 {
		return DefaultMaxRangeDays
	}
	return widest
}

// WindowRefusalAdvice is the closing advice of a window refusal. A kind whose
// maximum is the default may be read as several windows; a kind with a larger
// declared maximum counts a work unit that spans a boundary whole in each
// window, so adding shorter windows overcounts and is not advised.
func WindowRefusalAdvice(maxDays int) string {
	if maxDays > DefaultMaxRangeDays {
		return "; ask one window of at most that length and do not add shorter windows: a work unit that spans a window boundary counts in each of them"
	}
	return "; read a longer period as several windows"
}

// WindowRefusalMessage is the fixed text of a window refusal for maxDays.
func WindowRefusalMessage(maxDays int) string {
	return fmt.Sprintf("a window spans at most %d days for the kinds asked%s", maxDays, WindowRefusalAdvice(maxDays))
}

// WidestRangeDays is the widest window any kind takes in one read.
func WidestRangeDays() int {
	widest := DefaultMaxRangeDays
	for _, days := range maxRangeDaysByKind {
		widest = max(widest, days)
	}
	return widest
}
