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
	facts := []CanonicalFact{one, two}
	AssignFactIDs(facts)
	return facts
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

func TestFactIDsAreDeterministicAndIndependentOfValues(t *testing.T) {
	facts := factIDGroup()
	if facts[0].ID == "" || facts[0].ID == facts[1].ID {
		t.Fatalf("two facts of one group must carry different non-empty ids: %q %q", facts[0].ID, facts[1].ID)
	}
	if !strings.HasPrefix(facts[0].ID, "fact_") {
		t.Fatalf("unexpected id shape %q", facts[0].ID)
	}
	again := factIDGroup()
	if again[0].ID != facts[0].ID || again[1].ID != facts[1].ID {
		t.Fatal("ids are not deterministic")
	}
	// Aggregation noise and a clock-moved window change values, never ids.
	noisy := factIDGroup()
	noisy[0].Fields = map[string]FactValue{"estimate_coverage_ratio": NumberFactValue(0.25 + 3e-16)}
	AssignFactIDs(noisy)
	if noisy[0].ID != facts[0].ID {
		t.Fatal("a value change must not move the id")
	}
}

func TestFactIDsDoNotCollideAcrossAMergedBundle(t *testing.T) {
	member := factIDGroup()
	group := factIDGroup()
	memberIDs := []string{member[0].ID, member[1].ID}
	merged := AppendFactsWithIDs(member, group)
	if merged[0].ID != memberIDs[0] || merged[1].ID != memberIDs[1] {
		t.Fatal("merging a group read must not change the ids of facts already read")
	}
	seen := map[string]bool{}
	for _, fact := range merged {
		if seen[fact.ID] {
			t.Fatalf("duplicate id %q after merging two reads of the same kind and subject", fact.ID)
		}
		seen[fact.ID] = true
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
	foreign.ID = MintFactID(foreign.Kind, foreign.Subject, 0)
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

func TestIDTargetedClaimIsNotCountedAsGroupClosure(t *testing.T) {
	facts := factIDGroup()
	if got := ClaimsGroundedBeyondFirstGroupMember(facts, []ClaimedFact{factIDClaim(facts, facts[1].ID, 0.75)}); got != 0 {
		t.Fatalf("an id-targeted claim must not be attributed to the group closure, got %d", got)
	}
	if got := ClaimsGroundedBeyondFirstGroupMember(facts, []ClaimedFact{factIDClaim(facts, "", 0.75)}); got != 1 {
		t.Fatalf("an id-less claim grounded on the second fact is the closure's decision, got %d", got)
	}
}
