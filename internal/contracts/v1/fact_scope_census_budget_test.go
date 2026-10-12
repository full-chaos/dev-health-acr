package v1

import "testing"

func TestFactScopeCensusRowCeilingDerivesFromTheBudget(t *testing.T) {
	t.Parallel()

	if got := ContextFabricFactScopeCensusRowCeiling(0); got != ContextFabricFactScopeCensusMaxCount {
		t.Fatalf("no declared budget: ceiling = %d, want every row (%d)", got, ContextFabricFactScopeCensusMaxCount)
	}
	if got := ContextFabricFactScopeCensusRowCeiling(ContextFabricSerializedBytesMax); got != ContextFabricFactScopeCensusMaxCount {
		t.Fatalf("largest budget: ceiling = %d, want every row (%d)", got, ContextFabricFactScopeCensusMaxCount)
	}
	smallest := ContextFabricFactScopeCensusRowCeiling(ContextFabricSerializedBytesMin)
	if smallest <= 0 || smallest >= ContextFabricFactScopeCensusMaxCount {
		t.Fatalf("smallest budget: ceiling = %d, want some rows but fewer than the %d the table holds", smallest, ContextFabricFactScopeCensusMaxCount)
	}
	// Rows that fit must fit: the ceiling's rows at their widest, with the
	// envelope, stay inside the headroom a caller of the smallest budget has.
	spend := smallest*(contextFabricFactScopeCensusRecordWorstBytes()+1) + contextFabricFactScopeCensusEnvelopeBytes()
	if spend > ContextFabricSerializedBytesMin-ContextFabricMinimumAnswerBytes {
		t.Fatalf("ceiling %d spends %d bytes, over the %d of headroom", smallest, spend, ContextFabricSerializedBytesMin-ContextFabricMinimumAnswerBytes)
	}
	// One more row would not fit.
	if over := (smallest+1)*(contextFabricFactScopeCensusRecordWorstBytes()+1) + contextFabricFactScopeCensusEnvelopeBytes(); over <= ContextFabricSerializedBytesMin-ContextFabricMinimumAnswerBytes {
		t.Fatalf("ceiling %d is not the largest that fits: %d rows would spend %d", smallest, smallest+1, over)
	}
	if got := ContextFabricFactScopeCensusRowCeiling(int64(ContextFabricMinimumAnswerBytes)); got != 0 {
		t.Fatalf("a budget with no headroom: ceiling = %d, want 0", got)
	}
	previous := 0
	for budget := int64(ContextFabricSerializedBytesMin); budget <= 4*ContextFabricSerializedBytesMin; budget += 97 {
		got := ContextFabricFactScopeCensusRowCeiling(budget)
		if got < previous {
			t.Fatalf("ceiling fell from %d to %d as the budget grew to %d", previous, got, budget)
		}
		previous = got
	}
}

func TestFactScopeCensusOmittedCountIsBoundedAgainstTheCensus(t *testing.T) {
	t.Parallel()

	one := []ContextFabricFactScopeCensusRecord{chaos5405ValidCensusRecord()}
	if err := validateFactScopeCensusOmitted(one, 0); err != nil {
		t.Fatalf("no omission: %v", err)
	}
	if err := validateFactScopeCensusOmitted(one, ContextFabricFactScopeCensusMaxCount-1); err != nil {
		t.Fatalf("rows + omitted at the maximum: %v", err)
	}
	if err := validateFactScopeCensusOmitted(one, -1); err == nil {
		t.Fatal("a negative omitted count was accepted")
	}
	if err := validateFactScopeCensusOmitted(one, ContextFabricFactScopeCensusMaxCount); err == nil {
		t.Fatal("rows + omitted beyond the number of decisions the resolver can make was accepted")
	}
}
