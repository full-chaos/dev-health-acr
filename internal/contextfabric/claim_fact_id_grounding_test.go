package contextfabric

import (
	"reflect"
	"strings"
	"testing"
)

func factIDGroup() []CanonicalFact {
	subject := SubjectRef{Kind: SubjectTeam, CanonicalID: "team.v2:00000000-0000-0000-0000-000000000001", Label: "Platform"}
	one := CanonicalFact{Kind: FactReadiness, Subject: subject, Fields: map[string]FactValue{"estimate_coverage_ratio": NumberFactValue(0.25)}, EvidenceRefIDs: []string{"acr:v1:work_item:a:1"}, Source: "s", SourceVersion: "v1"}
	two := CanonicalFact{Kind: FactReadiness, Subject: subject, Fields: map[string]FactValue{"estimate_coverage_ratio": NumberFactValue(0.75)}, EvidenceRefIDs: []string{"acr:v1:work_item:a:2"}, Source: "s", SourceVersion: "v1"}
	one.ID, two.ID = MintFactID(one), MintFactID(two)
	return []CanonicalFact{one, two}
}

func factIDClaim(facts []CanonicalFact, factID string, value float64) ClaimedFact {
	return ClaimedFact{
		ClaimID: "claim-readiness-1",
		Kind:    FactReadiness,
		Subject: facts[0].Subject,
		Field:   "estimate_coverage_ratio",
		Value:   ScalarValue{Number: &value},
		FactID:  factID,
	}
}

func TestMintFactIDIsDeterministicAndContentDerived(t *testing.T) {
	facts := factIDGroup()
	if facts[0].ID == "" || facts[0].ID == facts[1].ID {
		t.Fatalf("two facts with different values must carry different non-empty ids: %q %q", facts[0].ID, facts[1].ID)
	}
	if again := MintFactID(facts[0]); again != facts[0].ID {
		t.Fatalf("id is not deterministic: %q vs %q", again, facts[0].ID)
	}
	withProvenance := facts[0]
	withProvenance.Source, withProvenance.SourceVersion, withProvenance.EvidenceRefIDs = "other", "v9", nil
	if MintFactID(withProvenance) != facts[0].ID {
		t.Fatal("provenance must not move the id")
	}
	if !strings.HasPrefix(facts[0].ID, "fact_") {
		t.Fatalf("unexpected id shape %q", facts[0].ID)
	}
}

func TestClaimWithFactIDGroundsAgainstThatFactOnly(t *testing.T) {
	facts := factIDGroup()
	// 0.75 is carried by the second fact; citing the FIRST fact's id with
	// that value must NOT ground through the group closure.
	if _, outcome := groundClaim(facts, factIDClaim(facts, facts[0].ID, 0.75)); outcome != claimValueContradicts {
		t.Fatalf("claim naming fact one with fact two's value: outcome %v, want claimValueContradicts", outcome)
	}
	got, outcome := groundClaim(facts, factIDClaim(facts, facts[1].ID, 0.75))
	if outcome != claimGrounded || got.ID != facts[1].ID {
		t.Fatalf("claim naming fact two: outcome %v id %q", outcome, got.ID)
	}
	if _, outcome, position := groundClaimAt(facts, factIDClaim(facts, facts[1].ID, 0.75)); outcome != claimGrounded || position != 1 {
		t.Fatalf("position %d outcome %v, want 1 grounded", position, outcome)
	}
}

func TestClaimWithoutFactIDKeepsGroupWideGrounding(t *testing.T) {
	facts := factIDGroup()
	got, outcome := groundClaim(facts, factIDClaim(facts, "", 0.75))
	if outcome != claimGrounded || got.ID != facts[1].ID {
		t.Fatalf("id-less claim must still ground against the group: outcome %v", outcome)
	}
}

func TestClaimWithWrongFactIDIsUngroundedNotWidened(t *testing.T) {
	facts := factIDGroup()
	for name, id := range map[string]string{"unknown": "fact_doesnotexist", "other kind or subject": "fact_" + strings.Repeat("0", 24)} {
		if _, outcome := groundClaim(facts, factIDClaim(facts, id, 0.75)); outcome != claimFactIDUnknown {
			t.Fatalf("%s: outcome %v, want claimFactIDUnknown (a wrong id must not fall back to the group)", name, outcome)
		}
	}
	foreign := facts[1]
	foreign.Subject = SubjectRef{Kind: SubjectTeam, CanonicalID: "team.v2:00000000-0000-0000-0000-000000000002", Label: "Other"}
	foreign.ID = MintFactID(foreign)
	if _, outcome := groundClaim(append(facts, foreign), factIDClaim(facts, foreign.ID, 0.75)); outcome != claimFactIDUnknown {
		t.Fatalf("an id of another subject's fact must not ground this subject's claim: %v", outcome)
	}
}

func TestRowsAttachFromTheFactTheClaimNames(t *testing.T) {
	facts := factIDGroup()
	got, ok := claimSourceFact(facts, factIDClaim(facts, facts[1].ID, 0.75))
	if !ok || got.ID != facts[1].ID {
		t.Fatalf("claimSourceFact must resolve through the same id: ok=%v id=%q", ok, got.ID)
	}
}

func TestCanonicalFactAndClaimedFactCarryTheIDOnTheWire(t *testing.T) {
	wire := func(typ reflect.Type, name string) bool {
		for i := 0; i < typ.NumField(); i++ {
			if tag := typ.Field(i).Tag.Get("json"); tag == name+",omitempty" {
				return true
			}
		}
		return false
	}
	if !wire(reflect.TypeOf(CanonicalFact{}), "id") {
		t.Fatal("CanonicalFact has no optional id")
	}
	if !wire(reflect.TypeOf(ClaimedFact{}), "fact_id") {
		t.Fatal("ClaimedFact has no optional fact_id")
	}
}
