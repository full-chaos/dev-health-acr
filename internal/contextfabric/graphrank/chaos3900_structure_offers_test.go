package graphrank

import (
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

func poolOf(candidates ...contextfabric.SubjectCandidate) map[string]contextfabric.SubjectCandidate {
	pool := make(map[string]contextfabric.SubjectCandidate, len(candidates))
	for _, c := range candidates {
		pool[SubjectKey(c.Subject)] = c
	}
	return pool
}

// TestFilterCandidatesByConfirmedKind_NilIsNoOp is the P1.D structural
// pin: an ordinary request (no kindr_ receipt confirmed, the overwhelming
// common case) must see BYTE-IDENTICAL pool composition, proving the
// filter never touches the pool absent a confirmation.
func TestFilterCandidatesByConfirmedKind_NilIsNoOp(t *testing.T) {
	t.Parallel()
	pool := poolOf(
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
	)
	got := filterCandidatesByConfirmedKind(pool, nil)
	if len(got) != len(pool) {
		t.Fatalf("filterCandidatesByConfirmedKind(pool, nil) returned %d entries, want %d (unchanged)", len(got), len(pool))
	}
	for key, candidate := range pool {
		if got[key].Subject.CanonicalID != candidate.Subject.CanonicalID {
			t.Errorf("filterCandidatesByConfirmedKind(pool, nil)[%q] = %+v, want unchanged %+v", key, got[key], candidate)
		}
	}
}

func TestFilterCandidatesByConfirmedKind_NarrowsToConfirmedKindOnly(t *testing.T) {
	t.Parallel()
	pool := poolOf(
		candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_1"),
		candidateOf(contractsv1.ContextFabricSubjectWorkItem, "wi_2"),
	)
	got := filterCandidatesByConfirmedKind(pool, &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem})
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (only the two work_item candidates)", len(got))
	}
	for _, candidate := range got {
		if candidate.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("filtered pool contains a non-confirmed kind %q: %+v", candidate.Subject.Kind, candidate)
		}
	}
}

func TestFilterCandidatesByConfirmedKind_NoMatchingCandidatesEmptiesThePool(t *testing.T) {
	t.Parallel()
	pool := poolOf(candidateOf(contractsv1.ContextFabricSubjectPullRequest, "pr_1"))
	got := filterCandidatesByConfirmedKind(pool, &contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem})
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0: nothing in the pool matches the confirmed kind", len(got))
	}
}

// fakeCensusFn builds a CensusFunc returning a fixed count per kind, for
// kindInsensitivityProof's own tests.
func fakeCensusFn(counts map[CensusKind]int, err error) CensusFunc {
	return func(_ context.Context, _ string, kind CensusKind, _ string, _ bool, _ contractsv1.ContextFabricSubjectKind, _ string, _ bool) (CensusOutcome, error) {
		if err != nil {
			return CensusOutcome{}, err
		}
		return CensusOutcome{Count: counts[kind]}, nil
	}
}

// TestKindInsensitivityProof pins design brief §2.0's own all-kinds
// census proof and its two implementation pins (registry-miss poison,
// error-fails-safe) -- this primitive is UNWIRED today (see its own
// doc comment) but must still be correct and independently testable
// ahead of the decisive-path wiring a future inferred-tier kind source
// requires.
func TestKindInsensitivityProof(t *testing.T) {
	t.Parallel()

	t.Run("single all-kinds satisfier is commit-sound", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 1,
			contractsv1.ContextFabricSubjectWorkItem:    0,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectWorkItem},
			"", false, "", "", false, census)
		if got != kindInsensitivityCommitSound {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivityCommitSound)
		}
	})
	t.Run("zero all-kinds satisfiers is no-match-sound", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 0,
			contractsv1.ContextFabricSubjectWorkItem:    0,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectWorkItem},
			"", false, "", "", false, census)
		if got != kindInsensitivityNoMatchSound {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivityNoMatchSound)
		}
	})
	t.Run("more than one all-kinds satisfier is kind_sensitive_outcome", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{
			contractsv1.ContextFabricSubjectPullRequest: 1,
			contractsv1.ContextFabricSubjectWorkItem:    1,
		}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectWorkItem},
			"", false, "", "", false, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q", got, kindInsensitivitySensitive)
		}
	})
	t.Run("a pre-narrowing kind outside the closed registry poisons the round", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 1}, nil)
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest, contractsv1.ContextFabricSubjectDocument},
			"", false, "", "", false, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q (registry-miss poison)", got, kindInsensitivitySensitive)
		}
	})
	t.Run("a census error fails safe, not open", func(t *testing.T) {
		census := fakeCensusFn(nil, errors.New("boom"))
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest}, "", false, "", "", false, census)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on census error", got, kindInsensitivitySensitive)
		}
	})
	t.Run("nil census is sensitive, not a panic", func(t *testing.T) {
		got := kindInsensitivityProof(context.Background(), "org_1",
			[]CensusKind{contractsv1.ContextFabricSubjectPullRequest}, "", false, "", "", false, nil)
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on nil census", got, kindInsensitivitySensitive)
		}
	})
	t.Run("empty pre-narrowing kind set is sensitive", func(t *testing.T) {
		got := kindInsensitivityProof(context.Background(), "org_1", nil, "", false, "", "", false, fakeCensusFn(nil, nil))
		if got != kindInsensitivitySensitive {
			t.Errorf("kindInsensitivityProof() = %q, want %q on empty kind set", got, kindInsensitivitySensitive)
		}
	})
}

// TestResolveSubjects_ConfirmedKindNarrowsThePool is the P1.D end-to-end
// integration pin: a confirmedKind passed into ResolveSubjects itself
// (not just filterCandidatesByConfirmedKind in isolation) actually
// narrows resolution.Candidates to that kind alone.
func TestResolveSubjects_ConfirmedKindNarrowsThePool(t *testing.T) {
	t.Parallel()
	pr := candidateNode(contractsv1.ContextFabricSubjectPullRequest, "pr_1", "PR 1", 0.5, "*")
	wi := candidateNode(contractsv1.ContextFabricSubjectWorkItem, "wi_1", "WI 1", 0.5, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {pr, wi}}}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(),
		&contextfabric.ConfirmedExpectedKind{Kind: contractsv1.ContextFabricSubjectWorkItem})
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, candidate := range resolution.Candidates {
		if candidate.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("resolution.Candidates contains a non-confirmed kind %q: %+v (confirmed kind must have narrowed the pool)", candidate.Subject.Kind, candidate)
		}
	}
	for _, committed := range resolution.Committed {
		if committed.Kind != contractsv1.ContextFabricSubjectWorkItem {
			t.Errorf("resolution.Committed contains a non-confirmed kind %q: %+v", committed.Kind, committed)
		}
	}
}

// TestResolveSubjects_NilConfirmedKindIsByteIdenticalToPreP1D is the
// structural pin at the ResolveSubjects call level: an ordinary request
// (nil confirmedKind) must resolve EXACTLY as it did before P1.D existed
// -- both kinds present in the pool, neither dropped.
func TestResolveSubjects_NilConfirmedKindIsByteIdenticalToPreP1D(t *testing.T) {
	t.Parallel()
	pr := candidateNode(contractsv1.ContextFabricSubjectPullRequest, "pr_1", "PR 1", 0.5, "*")
	wi := candidateNode(contractsv1.ContextFabricSubjectWorkItem, "wi_1", "WI 1", 0.5, "*")
	backend := &fakeGraphBackend{searchResults: map[string][]CandidateNode{"Ask Dev": {pr, wi}}}

	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("Ask Dev"), backend.deps(), nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	seen := map[contractsv1.ContextFabricSubjectKind]bool{}
	for _, candidate := range resolution.Candidates {
		seen[candidate.Subject.Kind] = true
	}
	if !seen[contractsv1.ContextFabricSubjectPullRequest] || !seen[contractsv1.ContextFabricSubjectWorkItem] {
		t.Errorf("resolution.Candidates = %#v, want BOTH kinds present (nil confirmedKind must not narrow anything)", resolution.Candidates)
	}
}
