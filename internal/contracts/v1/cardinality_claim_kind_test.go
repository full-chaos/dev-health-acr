package v1

import "testing"

// `cardinality` is claimable and NOT requestable, and both halves are pinned
// because the whole design rests on the asymmetry.
//
// It exists so the server can CLAIM a number it computed. Nothing produces it,
// so a request for it can only ever be unserved -- and the requestable
// vocabulary is rendered verbatim into the interpretation prompt, so admitting
// it there would advertise to the model a kind it may ask for and nothing can
// answer.

func TestCardinalityIsAdmittedAsAClaimKind(t *testing.T) {
	t.Parallel()
	if !validClaimedFactKind(ContextFabricFactCardinality) {
		t.Fatal("validClaimedFactKind refuses cardinality -- the server could not claim the count it computed")
	}
}

func TestCardinalityIsRefusedAsARequestableFactKind(t *testing.T) {
	t.Parallel()
	if validFactKind(ContextFabricFactCardinality) {
		t.Fatal("validFactKind admits cardinality -- a model could request a kind no producer serves")
	}
	for _, kind := range ContextFabricFactKindVocabulary() {
		if kind == ContextFabricFactCardinality {
			t.Fatal("cardinality is in the requestable vocabulary -- that array is rendered into the interpretation prompt's closed set")
		}
	}
}

// A REQUEST carrying it is refused at the boundary, not merely absent from a
// list: the vocabulary and the validator are separate mechanisms and this
// drives the one a caller actually hits.
func TestAFactRequirementForCardinalityIsRefused(t *testing.T) {
	t.Parallel()
	requirement := ContextFabricFactRequirement{
		Kind:     ContextFabricFactCardinality,
		Subjects: []ContextFabricSubjectRef{},
	}
	if err := requirement.Validate(); err == nil {
		t.Fatal("a fact requirement for cardinality validated -- it must be unknown on the request side")
	}
}
