package contextfabric

import "testing"

// CHAOS-6564: identical kind.field=value claims across many subjects were
// restated once per subject ("membership.organization_id=X for A; ...=X for B;
// ..."). One statement per distinct kind.field=value naming every subject is
// the same information without the repetition.
func TestComposeCurrentStateGroupsIdenticalValuesAcrossSubjects(t *testing.T) {
	t.Parallel()
	claim := func(id, label, org string) ClaimedFact {
		return ClaimedFact{
			ClaimID: id, Kind: FactMembership, Field: "organization_id",
			Subject: SubjectRef{Kind: SubjectRepository, CanonicalID: "repository:" + label, Label: label},
			Value:   ScalarValue{String: ptrString(org)},
		}
	}
	got := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{
		claim("c1", "org/a", "org_1"), claim("c2", "org/b", "org_1"), claim("c3", "org/c", "org_1"),
	}})
	want := "Current observed values: membership.organization_id=org_1 for org/a, org/b, org/c."
	if got != want {
		t.Fatalf("CurrentState = %q, want %q", got, want)
	}
	single := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{claim("c1", "org/a", "org_1")}})
	if single != "Current observed values: membership.organization_id=org_1 for org/a." {
		t.Fatalf("single-subject CurrentState changed: %q", single)
	}
	mixed := composeCurrentState(SynthesisDraft{ClaimedFacts: []ClaimedFact{
		claim("c1", "org/a", "org_1"), claim("c2", "org/b", "org_2"), claim("c3", "org/c", "org_1"),
	}})
	if mixed != "Current observed values: membership.organization_id=org_1 for org/a, org/c; membership.organization_id=org_2 for org/b." {
		t.Fatalf("mixed CurrentState = %q", mixed)
	}
}
