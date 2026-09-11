package contextfabric

import "strconv"

// SanitizeLogInt is the log barrier for an INTEGER that derives from a
// request (CHAOS-5558, alerts #61/#68: telemetry.go's RecordPlanNarrowing/
// RecordBudgetAssertion log MaxSerializedBytes, a request option, as a bare
// int). SanitizeLogAttr's string-only shape does not apply directly -- an
// int cannot carry a line break, but CodeQL's go/log-injection query still
// traces a request-derived value of ANY type straight to a logger sink and
// flags it, because it has no type-specific rule that a Go int argument to
// a variadic slog call can never forge a line. There is no numeric-typed
// CodeQL-recognized barrier to call directly (unlike strings.Replace for
// CWE-117's own remediation), so the barrier is built the same way #499's
// requestDerivedLogInt in this same file's telemetry.go independently
// established for the exact same class (member/cohort allowance ints):
// round-trip the value through the ALREADY-recognized string barrier --
// format to decimal, sanitize (a no-op on digits, but the CALL is what
// CodeQL recognizes), parse back. A value that somehow fails to parse back
// (impossible for strconv.Itoa's own output, but the barrier must still
// have a defined behavior for it) reports as 0, never the unparsed text.
//
// Every int64-typed log attribute in this file that derives from a request
// option goes through this ONE function; a scalar/[]string value still
// goes through SanitizeLogAttr/SanitizeLogStrings above -- this is
// additive for the numeric class, not a replacement. int64, not int:
// MaxSerializedBytes (both alert sites) is declared int64 on its event
// struct; a narrower helper would need a conversion at every call site
// anyway, so the barrier itself takes the field's own type.
func SanitizeLogInt(value int64) int64 {
	parsed, err := strconv.ParseInt(SanitizeLogAttr(strconv.FormatInt(value, 10)), 10, 64)
	if err != nil {
		return 0
	}
	return parsed
}
