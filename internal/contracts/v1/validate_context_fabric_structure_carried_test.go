package v1

import "testing"

// TestConfirmedStructureEntry_AdmitsACarriedExpectedKind is this lane's
// test 7, and it is a PIN, not a red-first test: the carried-source rules
// were written per-source rather than per-member, so the wire contract
// already admits a carried expected_kind today. That is load-bearing for
// the structure-axis carry -- it is why that mechanism needs no contract
// widening, no schema/OpenAPI/MCP churn and no consumer pin bump -- and
// nothing currently states it, so a later tightening of these rules to
// "carried means window" would break the carry with every contract test
// still green. This test is what makes that regression visible.
func TestConfirmedStructureEntry_AdmitsACarriedExpectedKind(t *testing.T) {
	t.Parallel()
	entry := ContextFabricConfirmedStructureEntry{
		Member:        ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(ContextFabricSubjectTeam),
		Source:        ContextFabricStructureSourceCarried,
		PriorResultID: "result_turn_a",
		Provenance:    ContextFabricStructureClarificationConfirmed,
		Disposition:   ContextFabricStructureDispositionApplied,
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil: a carried expected_kind is expressible in v1 today", err)
	}
}

// TestConfirmedStructureEntry_CarriedExpectedKindMustNotClaimAReceipt pins
// the other half: a carried entry inherited a value, it did not redeem
// anything on this request. Letting it name a receipt id would make a carry
// indistinguishable from a redemption in the persisted record the
// supersession claims and the Bridge learning loop both read.
func TestConfirmedStructureEntry_CarriedExpectedKindMustNotClaimAReceipt(t *testing.T) {
	t.Parallel()
	entry := ContextFabricConfirmedStructureEntry{
		Member:        ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(ContextFabricSubjectTeam),
		Source:        ContextFabricStructureSourceCarried,
		PriorResultID: "result_turn_a",
		ReceiptID:     "kindr_turn_a_01",
		Provenance:    ContextFabricStructureClarificationConfirmed,
		Disposition:   ContextFabricStructureDispositionApplied,
	}
	if err := entry.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: a carried entry redeemed no receipt of its own")
	}
}

// TestConfirmedStructureEntry_CarriedExpectedKindMustNameItsOrigin pins the
// disclosure floor: a carry whose origin cannot be named is a value with no
// provenance, which is the silent inheritance this whole mechanism exists to
// avoid.
func TestConfirmedStructureEntry_CarriedExpectedKindMustNameItsOrigin(t *testing.T) {
	t.Parallel()
	entry := ContextFabricConfirmedStructureEntry{
		Member:       ContextFabricStructureNeedExpectedKind,
		AppliedValue: string(ContextFabricSubjectTeam),
		Source:       ContextFabricStructureSourceCarried,
		Provenance:   ContextFabricStructureClarificationConfirmed,
		Disposition:  ContextFabricStructureDispositionApplied,
	}
	if err := entry.Validate(); err == nil {
		t.Fatal("Validate() = nil, want an error: a carried entry must name the prior result it inherited from")
	}
}
