package v1

import "testing"

func TestComparisonPeriodUnreadLimitationIsAServiceDisclosureRecognisedWhole(t *testing.T) {
	const (
		statedStart, statedEnd = "2026-09-04T22:58:22.154587457Z", "2026-10-04T22:58:22.154587457Z"
		priorStart, priorEnd   = "2026-08-05T22:58:22.154587457Z", "2026-09-04T22:58:22.154587457Z"
	)
	sentence := ContextFabricComparisonPeriodUnreadLimitation(statedStart, statedEnd, priorStart, priorEnd)
	if !IsContextFabricComparisonPeriodUnreadLimitation(sentence) || !IsContextFabricServiceAuthoredLimitation(sentence) {
		t.Fatalf("the composed sentence is not recognised as a service disclosure: %q", sentence)
	}
	if whole := ContextFabricComparisonPeriodUnreadLimitation("2026-09-04T00:00:00Z", "2026-10-04T00:00:00Z", "2026-08-05T00:00:00Z", "2026-09-04T00:00:00Z"); !IsContextFabricComparisonPeriodUnreadLimitation(whole) {
		t.Errorf("whole-second bounds are not recognised: %q", whole)
	}
	for _, other := range []string{
		sentence + " More.",
		"Note: " + sentence,
		ContextFabricComparisonPeriodUnreadLimitation("2026-09-04", "2026-10-04", "2026-08-05", "2026-09-04"),
		ContextFabricComparisonPeriodUnreadLimitation(statedStart, statedEnd, "the month before", priorEnd),
		"This question compares two periods; this answer read only the stated period, " + statedStart + " to " + statedEnd + ". The period it is compared with, " + priorStart + " to " + priorEnd + ", was not read; a second call with evidence_window start " + statedStart + " and end " + priorEnd + " reads it.",
		"This question compares two periods; this answer read only the stated period, " + statedStart + " to " + statedEnd + ". The period it is compared with, " + priorStart + " to " + priorEnd + ", was not read; a second call with evidence_window start " + priorStart + " and end " + statedEnd + " reads it.",
		"This question compares two periods.",
	} {
		if IsContextFabricComparisonPeriodUnreadLimitation(other) {
			t.Errorf("%q is recognised as the comparison-period disclosure", other)
		}
	}
}
