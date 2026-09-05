package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The completion quantifier is fixed by the OBLIGATION, never by how many
// producers the registry happens to declare.
//
// The rule it replaces derived the standard from the seed's cardinality, which
// inverts: REMOVING a producer LOWERS the bar instead of raising a gap, and a
// duplicate adapter manufactures corroboration that never existed. The standard
// becomes a property of the deployment rather than of the question.
//
// These tests hold the FRAME constant and vary the REGISTRY, which is the only
// way to state the inversion: under the old rule the derived row moves, under
// the new one it cannot.

// teamStateFrame is a named-subject frame that derives a `state` read
// requirement for a team -- the coordinate the acceptance artifact exercises
// most.
func teamStateFrame(t *testing.T) QuestionFrame {
	t.Helper()
	kind := SubjectTeam
	return frameWith(
		[]InvestigationGoal{GoalAssessState},
		SubjectExpression{Kind: SubjectExpressionNamed, Named: &NamedSubjectExpression{
			Handle: "team-one", ExpectedKind: &kind,
		}},
		TemporalIntentCurrent,
		nil,
	)
}

// stateCapability declares one producer serving `state` for a team over the
// given fact kinds.
func stateCapability(name string, kinds ...FactKind) FactCapability {
	capability := FactCapability{
		Kind:                  kinds[0],
		SupportedSubjectKinds: []SubjectKind{SubjectTeam},
		Obligations:           map[SubjectKind][]AnswerObligation{SubjectTeam: {ObligationState}},
	}
	_ = name
	return capability
}

// derivedStateRow finds the `state/subject/team` row in a derivation.
func derivedStateRow(t *testing.T, rows []DerivedRequirement) DerivedRequirement {
	t.Helper()
	for _, row := range rows {
		if row.Obligation == ObligationState && row.Role == SubjectRoleSubject {
			return row
		}
	}
	t.Fatalf("no state/subject row in %d derived rows", len(rows))
	return DerivedRequirement{}
}

// TestRemovingAProducerCannotLowerTheStandard is the inversion, stated as the
// harm.
//
// Under the superseded rule the same frame derives `corroborated` with two
// declaring kinds and `at_least_one` with one -- so decommissioning a provider
// silently relaxes what the answer must meet. The assertion is POSITIVE (the
// quantifier equals the declared standard) rather than "did not change", so it
// reds at the parent for the right reason.
func TestRemovingAProducerCannotLowerTheStandard(t *testing.T) {
	t.Parallel()
	frame := teamStateFrame(t)
	rich := []FactCapability{
		stateCapability("a", contractsv1.ContextFabricFactHealth),
		stateCapability("b", contractsv1.ContextFabricFactWorkload),
	}
	thin := rich[:1]

	richRow := derivedStateRow(t, DeriveRequirements(frame, GenerateObligationSeed(rich), rich))
	thinRow := derivedStateRow(t, DeriveRequirements(frame, GenerateObligationSeed(thin), thin))

	want := readQuantifiers[ObligationState]
	if richRow.Quantifier != want {
		t.Fatalf("with two declaring kinds the standard is %q, want the declared %q", richRow.Quantifier, want)
	}
	if thinRow.Quantifier != want {
		t.Fatalf("REMOVING a producer changed the standard to %q, want the declared %q -- the bar is "+
			"a property of the obligation, and a thinner registry raises a GAP, never a lower bar",
			thinRow.Quantifier, want)
	}
	// The serving set itself is still allowed to shrink; it is the STANDARD
	// that may not. Asserting this keeps the test from passing on a
	// derivation that stopped varying with the registry altogether.
	if len(richRow.FactKinds) == len(thinRow.FactKinds) {
		t.Fatalf("the fixture no longer varies the registry: both derivations name %d serving kinds",
			len(richRow.FactKinds))
	}
}

// TestADuplicateAdapterCannotManufactureCorroboration is the other direction.
//
// Two adapters over the SAME observation are one source. Under the superseded
// rule a second declaring kind was enough to demand -- and therefore to claim
// -- corroboration.
func TestADuplicateAdapterCannotManufactureCorroboration(t *testing.T) {
	t.Parallel()
	frame := teamStateFrame(t)
	single := []FactCapability{stateCapability("a", contractsv1.ContextFabricFactHealth)}
	doubled := []FactCapability{
		stateCapability("a", contractsv1.ContextFabricFactHealth),
		stateCapability("a-mirror", contractsv1.ContextFabricFactWorkload),
	}

	before := derivedStateRow(t, DeriveRequirements(frame, GenerateObligationSeed(single), single))
	after := derivedStateRow(t, DeriveRequirements(frame, GenerateObligationSeed(doubled), doubled))
	if before.Quantifier != after.Quantifier {
		t.Fatalf("adding a second adapter moved the standard %q -> %q; corroboration is not something "+
			"a deployment can manufacture", before.Quantifier, after.Quantifier)
	}
}

// TestEveryReadObligationDeclaresItsQuantifier keeps the table TOTAL.
//
// A missing entry yields `none`, the read evaluator then skips that
// requirement, and the requirement keeps only its planning seed. That is
// fail-closed on the STATE -- the answer reads partial rather than complete --
// but it silently costs the row its cause, so the table is asserted total
// rather than left to the fallback.
func TestEveryReadObligationDeclaresItsQuantifier(t *testing.T) {
	t.Parallel()
	if len(readQuantifiers) == 0 {
		t.Fatal("the quantifier table is empty; every assertion below would pass vacuously")
	}
	reads := 0
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known || kind != ObligationKindRead {
			if _, declared := readQuantifiers[obligation]; declared {
				t.Fatalf("obligation %q is not a read but carries a read quantifier", obligation)
			}
			continue
		}
		reads++
		quantifier, declared := readQuantifiers[obligation]
		if !declared {
			t.Fatalf("read obligation %q declares no completion standard", obligation)
		}
		if quantifier != CompletionQuantifierAtLeastOne && quantifier != CompletionQuantifierCorroborated {
			t.Fatalf("read obligation %q declares %q; a read standard is at_least_one or corroborated "+
				"-- exact and all belong to the computed obligations", obligation, quantifier)
		}
	}
	if reads == 0 {
		t.Fatal("no obligation classified as a read; this test proved nothing")
	}
	if len(readQuantifiers) != reads {
		t.Fatalf("the table holds %d entries for %d read obligations", len(readQuantifiers), reads)
	}
}

// TestTheObligationKindMirrorAgreesInBothDirections is the parity gate for the
// contracts-side mirror the completeness derivation scopes on.
//
// A mirror entry that outlives its domain member, or a domain member with no
// mirror entry, changes which requirements the read pass considers -- silently.
func TestTheObligationKindMirrorAgreesInBothDirections(t *testing.T) {
	t.Parallel()
	mirror := contractsv1.ContextFabricAnswerObligationKindByObligation()
	if len(mirror) == 0 {
		t.Fatal("the mirror is empty; both loops below would pass vacuously")
	}
	domain := 0
	for _, obligation := range AnswerObligationVocabulary() {
		kind, known := KindOfObligation(obligation)
		if !known {
			t.Fatalf("obligation %q has no kind in the domain table", obligation)
		}
		domain++
		mirrored, present := mirror[string(obligation)]
		if !present {
			t.Fatalf("obligation %q is missing from the contracts-side mirror", obligation)
		}
		if mirrored != string(kind) {
			t.Fatalf("obligation %q is %q in the domain and %q in the mirror", obligation, kind, mirrored)
		}
	}
	if len(mirror) != domain {
		t.Fatalf("the mirror holds %d entries and the domain %d; a mirror entry has outlived its "+
			"domain member", len(mirror), domain)
	}
}
