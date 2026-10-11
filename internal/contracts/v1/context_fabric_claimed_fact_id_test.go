package v1

import (
	"encoding/json"
	"strings"
	"testing"
)

func claimedFactIDFixture(factID string) ContextFabricClaimedFact {
	value := int64(3)
	return ContextFabricClaimedFact{
		ClaimID: "claim-fact-id-1",
		Kind:    ContextFabricFactWork,
		Subject: ContextFabricSubjectRef{Kind: ContextFabricSubjectTeam, CanonicalID: "team.v2:00000000-0000-0000-0000-000000000001", Label: "Platform"},
		Field:   "open_items",
		Value:   ContextFabricScalarValue{Integer: &value},
		FactID:  factID,
	}
}

func TestClaimedFactFactIDRoundTripsAndIsOptional(t *testing.T) {
	with := claimedFactIDFixture("fact_0123456789abcdef01234567")
	raw, err := json.Marshal(with)
	if err != nil {
		t.Fatal(err)
	}
	var back ContextFabricClaimedFact
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.FactID != with.FactID || back.Validate() != nil {
		t.Fatalf("fact_id lost or invalid after round trip: %q %v", back.FactID, back.Validate())
	}
	without, err := json.Marshal(claimedFactIDFixture(""))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(without), "fact_id") {
		t.Fatalf("an absent fact_id must be omitted from the wire: %s", without)
	}
	if err := claimedFactIDFixture("").Validate(); err != nil {
		t.Fatalf("a claim without fact_id must stay valid: %v", err)
	}
}

func TestClaimedFactFactIDIsBounded(t *testing.T) {
	if err := claimedFactIDFixture(strings.Repeat("x", ContextFabricModelMintedIDMaxLength+1)).Validate(); err == nil {
		t.Fatal("an over-long fact_id must be refused")
	}
	if err := claimedFactIDFixture(" padded ").Validate(); err == nil {
		t.Fatal("a whitespace-padded fact_id must be refused")
	}
}
