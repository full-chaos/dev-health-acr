package graphrank

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func candidateOf(kind contractsv1.ContextFabricSubjectKind, id string) contextfabric.SubjectCandidate {
	return contextfabric.SubjectCandidate{
		Subject: contextfabric.SubjectRef{Kind: kind, CanonicalID: id, Label: id},
	}
}

func TestKindOfferMaterial_EmptyPoolOffersNothing(t *testing.T) {
	t.Parallel()
	material := kindOfferMaterial(nil)
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(nil) = %+v, want empty (nothing to disambiguate)", material)
	}
}

func TestKindOfferMaterial_SingleKindPoolOffersNothing(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_2"),
	}
	material := kindOfferMaterial(candidates)
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(single-kind pool) = %+v, want empty: nothing to disambiguate when every candidate is the same kind", material)
	}
}

// TestKindOfferMaterial_MultiKindPoolOffersDisambiguation is the P1.C
// acceptance shape for expected_kind: "30 of 41 stalled pools span >=2
// census kinds" (design brief §1.2 reading 1) is exactly the case this
// proves the engine now discloses.
func TestKindOfferMaterial_MultiKindPoolOffersDisambiguation(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material := kindOfferMaterial(candidates)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedExpectedKind {
		t.Fatalf("material.Missing = %v, want exactly [expected_kind]", material.Missing)
	}
	if len(material.KindOptions) != 2 {
		t.Fatalf("len(material.KindOptions) = %d, want 2", len(material.KindOptions))
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, opt := range material.KindOptions {
		seen[opt.Kind] = true
		if opt.Label == "" {
			t.Errorf("KindOption for %q has an empty Label", opt.Kind)
		}
		if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
			t.Errorf("KindOption for %q OfferSource = %q, want %q", opt.Kind, opt.OfferSource, contractsv1.ContextFabricStructureOfferEngine)
		}
		// ReceiptID/OptionID are deliberately unset here -- minted later
		// by composeStructureNeeds once a ResultID exists (see
		// StructureOfferMaterial's own doc comment).
		if opt.ReceiptID != "" || opt.OptionID != "" {
			t.Errorf("KindOption for %q carries a pre-minted ReceiptID/OptionID (%q/%q), want both unset at this stage", opt.Kind, opt.ReceiptID, opt.OptionID)
		}
	}
	if !seen[contractsv1.ContextFabricSubjectPullRequest] || !seen[contractsv1.ContextFabricSubjectWorkItem] {
		t.Errorf("material.KindOptions = %+v, want one entry per distinct kind in the pool", material.KindOptions)
	}
}

// TestKindOfferMaterial_DuplicateKindsCollapseToOneOption pins that a pool
// with many candidates of the SAME kind contributes exactly one
// KindOption for it, not one per candidate.
func TestKindOfferMaterial_DuplicateKindsCollapseToOneOption(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_2"),
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_3"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	}
	material := kindOfferMaterial(candidates)
	if len(material.KindOptions) != 2 {
		t.Fatalf("len(material.KindOptions) = %d, want 2 (one per DISTINCT kind, not one per candidate)", len(material.KindOptions))
	}
}

// TestKindOfferMaterial_NonOfferableKindsAreIgnoredForDisambiguation pins
// the closed structureOfferKinds set: a pool spanning a non-offerable kind
// (e.g. document) alongside exactly one offerable kind must NOT be treated
// as ambiguous on the expected_kind axis, since only one OFFERABLE kind is
// actually in contention.
func TestKindOfferMaterial_NonOfferableKindsAreIgnoredForDisambiguation(t *testing.T) {
	t.Parallel()
	candidates := []contextfabric.SubjectCandidate{
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectDocument, "doc_1"),
	}
	material := kindOfferMaterial(candidates)
	if len(material.Missing) != 0 || len(material.KindOptions) != 0 {
		t.Errorf("kindOfferMaterial(pull_request + document) = %+v, want empty: document is not in the offerable expected_kind vocabulary", material)
	}
}
