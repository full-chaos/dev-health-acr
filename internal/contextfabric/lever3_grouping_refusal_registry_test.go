package contextfabric

import (
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// SEPARATE FILE, same reason as lever3_admission_walk_kinds_test.go: the
// tests below name contract symbols the fix introduces, so they cannot
// compile at the fix parent. Keeping them out of
// lever3_grouping_refusal_never_silent_test.go leaves that file compiling
// there and failing on its own assertions, which is what a red-first proof
// has to do.

// TestTheRefusalDisclosureIsRegisteredAsServiceAuthored states the property
// the two behavioural tests above depend on, directly and at the contract
// boundary, so a failure says WHICH of the two things broke.
func TestTheRefusalDisclosureIsRegisteredAsServiceAuthored(t *testing.T) {
	t.Parallel()
	disclosure := contractsv1.ContextFabricGroupingRefusalLimitation(SubjectRepository, SubjectTeam)
	if !contractsv1.IsContextFabricServiceAuthoredLimitation(disclosure) {
		t.Fatal("the grouping-refusal disclosure is not service-authored to the contract, so every displacement rule treats it as a model caveat and may drop it")
	}
	if !contractsv1.HasContextFabricServiceAuthoredLimitation([]string{disclosure}) {
		t.Fatal("the validator's LimitationsDisplaced coherence oracle does not see the disclosure, so a positive displaced count carried only by it is rejected as incoherent")
	}
	// The engine's own view must agree with the contract's: two
	// hand-maintained lists is how three disclosures came to be
	// displaceable on one side and unrecognised on the other.
	if !isServiceAuthoredLimitation(disclosure) {
		t.Fatal("the engine does not recognise the disclosure the contract does")
	}
}

// TestOnlyAComposableGroupingRefusalSentenceIsRecognised is the negative
// control on the RECOGNISER, which is the part of this fix that could itself
// become a hole.
//
// Recognition is a parse rather than a prefix match precisely so that a
// model-authored caveat opening with this wording cannot become
// undisplaceable and take a real caveat's slot. These are the shapes that
// must NOT be recognised.
func TestOnlyAComposableGroupingRefusalSentenceIsRecognised(t *testing.T) {
	t.Parallel()
	prefix := "This question asked for a breakdown by "
	middle := ", but the available facts group by "
	suffix := ", so the answer is presented ungrouped."

	for name, limitation := range map[string]string{
		"a kind outside the closed vocabulary": prefix + "squad" + middle + "team" + suffix,
		"a source kind outside the vocabulary": prefix + "team" + middle + "squad" + suffix,
		"an empty planned kind":                prefix + "" + middle + "team" + suffix,
		"free model prose after the prefix":    prefix + "team, and I would add that the data looked thin" + suffix,
		"the prefix alone":                     prefix,
		"trailing model text after the suffix": prefix + "repository" + middle + "team" + suffix + " Also, see below.",
		"a plain model caveat":                 "The evidence for this answer is thin.",
		"the empty string":                     "",
	} {
		if contractsv1.IsContextFabricGroupingRefusalLimitation(limitation) {
			t.Errorf("%s: recognised as the service's own disclosure, so a model caveat became undisplaceable", name)
		}
	}

	// Positive control: every closed-vocabulary pair the composer can
	// actually produce IS recognised, so the parse is not merely strict.
	for _, planned := range contractsv1.ContextFabricSubjectKindVocabulary() {
		for _, source := range contractsv1.ContextFabricSubjectKindVocabulary() {
			composed := contractsv1.ContextFabricGroupingRefusalLimitation(planned, source)
			if !contractsv1.IsContextFabricGroupingRefusalLimitation(composed) {
				t.Errorf("composer output for (%s, %s) is not recognised by its own parser", planned, source)
			}
		}
	}
}
