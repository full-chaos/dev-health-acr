package v1

import (
	"encoding/json"
	"testing"
)

// candidateWithMechanisms builds a minimal-but-valid SubjectCandidate so each
// test below varies ONLY the mechanism set.
func candidateWithMechanisms(mechanisms ...ContextFabricSubjectMatchMechanism) ContextFabricSubjectCandidate {
	return ContextFabricSubjectCandidate{
		ReceiptID: "receipt_12345678",
		Subject: ContextFabricSubjectRef{
			Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev",
		},
		State:           ContextFabricResolutionProposed,
		MatchReasons:    []string{"Hybrid graph search matched the subject."},
		Confidence:      0.6,
		EvidenceRefIDs:  []string{"evidence_project_identity"},
		MatchMechanisms: mechanisms,
	}
}

func TestAC_3778_6_EveryClosedEnumMemberValidates(t *testing.T) {
	all := []ContextFabricSubjectMatchMechanism{
		ContextFabricMatchExact, ContextFabricMatchAlias, ContextFabricMatchProviderKey,
		ContextFabricMatchLexical, ContextFabricMatchVector, ContextFabricMatchTraversalParent,
	}
	for _, mechanism := range all {
		if !ValidContextFabricSubjectMatchMechanism(mechanism) {
			t.Fatalf("mechanism %q must be a recognized enum member", mechanism)
		}
		if err := candidateWithMechanisms(mechanism).Validate(); err != nil {
			t.Fatalf("candidate with mechanism %q must validate: %v", mechanism, err)
		}
	}
	// The whole set at once is the documented upper bound (maxItems 6).
	if err := candidateWithMechanisms(all...).Validate(); err != nil {
		t.Fatalf("candidate carrying every mechanism must validate: %v", err)
	}
}

// AC-3778-6 requires a reader to be able to TELL a vector match apart from an
// exact, alias, or graph match. That is only true if the enum is closed: an
// unrecognized value must be rejected, never passed through as a seventh
// mechanism that graphrank's corroboration count would treat as distinct.
func TestAC_3778_6_UnknownMechanismIsRejected(t *testing.T) {
	if ValidContextFabricSubjectMatchMechanism(ContextFabricSubjectMatchMechanism("semantic")) {
		t.Fatal("an unrecognized mechanism must not validate")
	}
	if err := candidateWithMechanisms(ContextFabricSubjectMatchMechanism("semantic")).Validate(); err == nil {
		t.Fatal("a candidate carrying an unrecognized mechanism must fail validation")
	}
}

// A duplicate would let one mechanism be counted twice toward the corroboration
// band in graphrank, which is exactly the "double-counting" a reviewer would
// object to. The contract forbids it at the boundary.
func TestAC_3778_6_DuplicateMechanismIsRejected(t *testing.T) {
	if err := candidateWithMechanisms(ContextFabricMatchVector, ContextFabricMatchVector).Validate(); err == nil {
		t.Fatal("a duplicated mechanism must fail validation")
	}
}

// The field is ADDITIVE OPTIONAL in v1. Every InvestigationResult persisted
// before CHAOS-3778 was serialized without it; those snapshots must still
// validate on replay, and must still round-trip WITHOUT the key reappearing as
// an empty array (which would change the stored bytes of an immutable result).
func TestAC_3778_6_AbsentMechanismSetStaysValidAndOmitted(t *testing.T) {
	candidate := candidateWithMechanisms()
	if err := candidate.Validate(); err != nil {
		t.Fatalf("a candidate with no recorded mechanisms must validate: %v", err)
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := decoded["match_mechanisms"]; present {
		t.Fatalf("an empty mechanism set must be omitted from the wire form, got %s", encoded)
	}
	// A pre-CHAOS-3778 snapshot decodes with the field simply absent.
	var replayed ContextFabricSubjectCandidate
	legacy := `{"receipt_id":"receipt_12345678","subject":{"kind":"project","canonical_id":"project_ask_dev","label":"Ask Dev"},"state":"proposed","match_reasons":["Hybrid graph search matched the subject."],"confidence":0.6,"evidence_ref_ids":["evidence_project_identity"]}`
	if err := json.Unmarshal([]byte(legacy), &replayed); err != nil {
		t.Fatalf("a pre-CHAOS-3778 snapshot must decode: %v", err)
	}
	if replayed.MatchMechanisms != nil {
		t.Fatalf("a pre-CHAOS-3778 snapshot must decode with no mechanisms, got %v", replayed.MatchMechanisms)
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("a pre-CHAOS-3778 snapshot must still validate: %v", err)
	}
}
