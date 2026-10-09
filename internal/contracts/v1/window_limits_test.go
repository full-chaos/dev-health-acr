package v1

import "testing"

func TestWindowTableIsTheOneSource(t *testing.T) {
	if got := MaxRangeDaysFor([]ContextFabricFactKind{ContextFabricFactHealth}); got != 60 {
		t.Errorf("health %d", got)
	}
	if got := MaxRangeDaysFor([]ContextFabricFactKind{ContextFabricFactInvestment, ContextFabricFactHealth}); got != 60 {
		t.Errorf("mixed %d", got)
	}
	if got := MaxRangeDaysFor([]ContextFabricFactKind{ContextFabricFactInvestment}); got != 365 || WidestRangeDays() != 365 {
		t.Errorf("investment %d widest %d", got, WidestRangeDays())
	}
}
