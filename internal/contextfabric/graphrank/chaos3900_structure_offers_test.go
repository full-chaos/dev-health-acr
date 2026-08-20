package graphrank

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

// TestResolveSubjects_AnchorAndHandleOffersEndToEnd is P1.C”s own
// end-to-end integration pin (mirroring P1.D's TestResolveSubjects_
// ConfirmedKindNarrowsThePool pattern): proves resolve.go's OWN wiring
// (not anchorOfferMaterial/handleOfferMaterial in isolation) produces the
// combined offer material. Two different terms each uniquely alias-match a
// DIFFERENT repository (the disagreement case), and the question text
// carries a grammar-bound PR number -- neither commits, and both surface
// as offers on the SAME ResolveSubjects call.
func TestResolveSubjects_AnchorAndHandleOffersEndToEnd(t *testing.T) {
	t.Parallel()
	repoA := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", -1, []string{"widget-service"}, nil, true)
	repoB := aliasCandidateNode(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", -1, []string{"widget-svc"}, nil, true)
	backend := &fakeGraphBackend{
		enableAliasLookup: true,
		aliasLookupClaimants: map[string][]CandidateNode{
			"widget-service": {repoA},
			"widget-svc":     {repoB},
		},
		aliasLookupComplete: true,
	}
	request := testRequest()
	request.Question = "is PR 532 related to widget-service or widget-svc?"

	resolution, material, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("widget-service", "widget-svc"), backend.deps(), nil)
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want NOTHING committed: two terms disagree on the anchor, genuinely ambiguous", resolution.Committed)
	}

	wantMissing := map[contractsv1.ContextFabricStructureNeedKind]bool{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: true,
		contractsv1.ContextFabricStructureNeedSubjectHandle: true,
	}
	if len(material.Missing) != len(wantMissing) {
		t.Fatalf("material.Missing = %v, want exactly %v (kind is unambiguous here: only repositories are in the pool)", material.Missing, wantMissing)
	}
	for _, m := range material.Missing {
		if !wantMissing[m] {
			t.Errorf("material.Missing contains unexpected member %q", m)
		}
	}

	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2 (one per disagreeing candidate)", len(material.AnchorOptions))
	}
	seenAnchors := map[string]bool{}
	for _, opt := range material.AnchorOptions {
		seenAnchors[opt.CanonicalID] = true
	}
	if !seenAnchors["repoA"] || !seenAnchors["repoB"] {
		t.Errorf("material.AnchorOptions = %+v, want repoA AND repoB", material.AnchorOptions)
	}

	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1", len(material.HandleOptions))
	}
	if material.HandleOptions[0].Value != "532" || material.HandleOptions[0].Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Errorf("material.HandleOptions[0] = %+v, want value=532 kind=pull_request", material.HandleOptions[0])
	}
}

func TestValidateHandleGrammar(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		kind      contractsv1.ContextFabricSubjectKind
		patternID string
		value     string
		want      bool
	}{
		{"pull request number, bare digits, valid", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", true},
		{"pull request number, with PR context, invalid (value must be bare)", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "PR 532", false},
		{"pull request number, non-numeric, invalid", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "abc", false},
		{"work item ticket key, valid", contractsv1.ContextFabricSubjectWorkItem, "work_item_ticket_key", "CHAOS-3896", true},
		{"work item ticket key, missing prefix, invalid", contractsv1.ContextFabricSubjectWorkItem, "work_item_ticket_key", "3896", false},
		{"ci run id, valid", contractsv1.ContextFabricSubjectCIRun, "ci_run_id", "18234567", true},
		{"ci run id, too short, invalid", contractsv1.ContextFabricSubjectCIRun, "ci_run_id", "123", false},
		{"kind and pattern_id mismatched, invalid", contractsv1.ContextFabricSubjectPullRequest, "work_item_ticket_key", "CHAOS-3896", false},
		{"unknown pattern_id, invalid", contractsv1.ContextFabricSubjectPullRequest, "bogus_pattern", "532", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateHandleGrammar(tc.kind, tc.patternID, tc.value)
			if got != tc.want {
				t.Errorf("ValidateHandleGrammar(%q, %q, %q) = %v, want %v", tc.kind, tc.patternID, tc.value, got, tc.want)
			}
		})
	}
}

func TestVerifyHandle(t *testing.T) {
	t.Parallel()

	t.Run("grammar mismatch short-circuits before any census call", func(t *testing.T) {
		called := false
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			called = true
			return CensusOutcome{Count: 1}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "not-a-number", census)
		if valid || reason != HandleVerificationGrammarMismatch {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationGrammarMismatch)
		}
		if called {
			t.Error("census was called despite a grammar mismatch -- existence check must never run on an already-invalid value")
		}
	})
	t.Run("grammar valid, census confirms existence", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 1}, nil)
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if !valid || reason != HandleVerificationValid {
			t.Errorf("VerifyHandle() = (%v, %q), want (true, %q)", valid, reason, HandleVerificationValid)
		}
	})
	t.Run("grammar valid, census finds nothing", func(t *testing.T) {
		census := fakeCensusFn(map[CensusKind]int{contractsv1.ContextFabricSubjectPullRequest: 0}, nil)
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationNotFound {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationNotFound)
		}
	})
	t.Run("census unavailable (nil) fails safe", func(t *testing.T) {
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", nil)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	t.Run("census error fails safe, not open", func(t *testing.T) {
		census := fakeCensusFn(nil, errors.New("boom"))
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	// Codex xhigh review (chaos-pivot-p1, first round), finding 1: a
	// nonzero Count alone is not sufficient -- a ClosureMismatch (or
	// SatisfierSetClosureMismatch) means the census could not PROVE the
	// fetched set/witness actually closed against the aggregate (the same
	// "race can only demote, never mint" rule chaos3899_census.go's own
	// producer and every other CensusOutcome consumer in this package
	// already apply). VerifyHandle must fail the SAME way an unreachable
	// census does, never validate on a bare Count>0.
	t.Run("closure mismatch fails safe, not open", func(t *testing.T) {
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 1, ClosureMismatch: true}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
	t.Run("satisfier-set closure mismatch fails safe, not open", func(t *testing.T) {
		census := CensusFunc(func(context.Context, string, CensusKind, string, bool, contractsv1.ContextFabricSubjectKind, string, bool) (CensusOutcome, error) {
			return CensusOutcome{Count: 3, SatisfierSetClosureMismatch: true}, nil
		})
		valid, reason := VerifyHandle(context.Background(), "org_1", contractsv1.ContextFabricSubjectPullRequest, "pull_request_number", "532", census)
		if valid || reason != HandleVerificationCensusUnavailable {
			t.Errorf("VerifyHandle() = (%v, %q), want (false, %q)", valid, reason, HandleVerificationCensusUnavailable)
		}
	})
}

func identityRow(kind contractsv1.ContextFabricSubjectKind, id, label string, aliases ...string) IdentityRow {
	return IdentityRow{Kind: kind, CanonicalID: id, Label: label, Aliases: aliases}
}

func fakeIdentityUniverseFn(rows []IdentityRow, complete bool, err error) IdentityUniverseFunc {
	return func(context.Context, string) ([]IdentityRow, time.Time, bool, error) {
		return rows, time.Time{}, complete, err
	}
}

// TestHashAliasTerm pins HashAliasTerm's own contract: deterministic,
// case/whitespace-insensitive (it hashes the NORMALIZED term, exactly what
// NormalizeAliasTerm produces), and a fixed 24-character digest -- the same
// length mintStructureReceiptID/mintStructureOptionID already use.
func TestHashAliasTerm(t *testing.T) {
	t.Parallel()

	a := HashAliasTerm("widget-service")
	b := HashAliasTerm("  Widget-Service  ")
	if a != b {
		t.Errorf("HashAliasTerm(%q) = %q, HashAliasTerm(%q) = %q, want equal: both normalize to the same term", "widget-service", a, "  Widget-Service  ", b)
	}
	if len(a) != 24 {
		t.Errorf("len(HashAliasTerm(...)) = %d, want 24", len(a))
	}
	if c := HashAliasTerm("a-different-term"); c == a {
		t.Errorf("HashAliasTerm(%q) = %q, want different from HashAliasTerm(%q) = %q", "a-different-term", c, "widget-service", a)
	}
}

// TestNormalizationParity_MatchIdentityRowsAndHashAliasTermAgree is the
// team-lead-mandated normalization-parity pin: MatchIdentityRows (the
// derive-side match) and HashAliasTerm (the verify-side hash) must reach
// the SAME verdict on the same (row, term) input -- a row MatchIdentityRows
// says matches a term must ALSO be a row identityRowCarriesTermHash finds
// via HashAliasTerm(term). If the two sides ever normalized differently,
// this is the test that would catch it; without it, the check "fails
// toward fine" (silently never contests anything) rather than loud.
func TestNormalizationParity_MatchIdentityRowsAndHashAliasTermAgree(t *testing.T) {
	t.Parallel()

	row := identityRow(contractsv1.ContextFabricSubjectRepository, "repo_1", "Widget Service", "  WIDGET-service  ", "widget_svc")
	for _, term := range []string{"widget service", "WIDGET SERVICE", "  Widget-Service  ", "widget_svc"} {
		matches := MatchIdentityRows([]IdentityRow{row}, []string{term})
		derivedMatch := len(matches[term]) == 1
		hashMatch := identityRowCarriesTermHash(row, HashAliasTerm(term))
		if derivedMatch != hashMatch {
			t.Errorf("term %q: MatchIdentityRows found a match = %v, identityRowCarriesTermHash found a match = %v -- the two sides disagree", term, derivedMatch, hashMatch)
		}
	}
}

// TestVerifyAnchorClaimantUnique_CaseFortyFiveTwinRepoShape is the
// team-lead-mandated case-45-shaped regression (CHAOS-3917's own corpus
// case, reused here for narrative continuity with
// TestResolveFromMergedCandidatesWithGate_ExactLabelNeverCommitsOverACollidingAliasClaimant):
// an anchor offer minted while "widget-service" uniquely names repoA must
// re-verify VALID before a rival claims the same alias, and CONTESTED
// (never silently still-valid, never a different wrong verdict) the moment
// a second repo (repoB) gains the identical alias.
func TestVerifyAnchorClaimantUnique_CaseFortyFiveTwinRepoShape(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)
	repoB := identityRow(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB", term)

	t.Run("unique claimant re-verifies valid", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if !valid || reason != AnchorVerificationValid {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (true, %q)", valid, reason, AnchorVerificationValid)
		}
	})
	t.Run("a rival gaining the SAME alias contests the claim", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA, repoB}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimContested {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimContested)
		}
	})
}

func TestVerifyAnchorClaimantUnique(t *testing.T) {
	t.Parallel()
	const term = "widget-service"
	hash := HashAliasTerm(term)
	repoA := identityRow(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA", term)

	t.Run("claim lost: no row carries the hash any longer", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("claim lost: the unique claimant is now a DIFFERENT canonical id", func(t *testing.T) {
		renamed := identityRow(contractsv1.ContextFabricSubjectRepository, "repoZ", "repoZ", term)
		universe := fakeIdentityUniverseFn([]IdentityRow{renamed}, true, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationClaimLost {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationClaimLost)
		}
	})
	t.Run("incomplete enumeration fails closed, not open", func(t *testing.T) {
		universe := fakeIdentityUniverseFn([]IdentityRow{repoA}, false, nil)
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
	t.Run("identity universe error fails closed, not open", func(t *testing.T) {
		universe := fakeIdentityUniverseFn(nil, true, errors.New("boom"))
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, universe)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
	t.Run("nil identity universe dependency fails closed, not open", func(t *testing.T) {
		valid, reason := VerifyAnchorClaimantUnique(context.Background(), "org_1", contractsv1.ContextFabricSubjectRepository, "repoA", hash, nil)
		if valid || reason != AnchorVerificationIncompleteEnumeration {
			t.Errorf("VerifyAnchorClaimantUnique() = (%v, %q), want (false, %q)", valid, reason, AnchorVerificationIncompleteEnumeration)
		}
	})
}

func identityMatch(kind contractsv1.ContextFabricSubjectKind, id, label string) IdentityMatch {
	return IdentityMatch{Row: IdentityRow{Kind: kind, CanonicalID: id, Label: label}}
}

func TestAnchorOfferMaterial_UniqueClaimantOffersNothing(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
	}
	material := anchorOfferMaterial(claimants, true)
	if len(material.Missing) != 0 || len(material.AnchorOptions) != 0 {
		t.Errorf("material = %+v, want empty: a unique claimant is already decisive, nothing to elicit", material)
	}
}

func TestAnchorOfferMaterial_DisagreementOffersOnePerCandidate(t *testing.T) {
	t.Parallel()
	claimants := map[string][]IdentityMatch{
		"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
		"widget-svc":     {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoB", "repoB")},
	}
	material := anchorOfferMaterial(claimants, true)
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
		t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
	}
	if len(material.AnchorOptions) != 2 {
		t.Fatalf("len(material.AnchorOptions) = %d, want 2", len(material.AnchorOptions))
	}
	for _, opt := range material.AnchorOptions {
		if opt.Kind != contractsv1.ContextFabricSubjectRepository {
			t.Errorf("AnchorOption.Kind = %q, want repository", opt.Kind)
		}
		if len(opt.MatchedTermHash) != 24 {
			t.Errorf("len(AnchorOption.MatchedTermHash) = %d, want 24", len(opt.MatchedTermHash))
		}
		if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
			t.Errorf("AnchorOption.OfferSource = %q, want engine", opt.OfferSource)
		}
	}
}

func TestAnchorOfferMaterial_NoCandidatesStillMissingWithEmptyOptions(t *testing.T) {
	t.Parallel()

	t.Run("zero claimants", func(t *testing.T) {
		material := anchorOfferMaterial(map[string][]IdentityMatch{}, true)
		if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
		}
		if len(material.AnchorOptions) != 0 {
			t.Errorf("len(material.AnchorOptions) = %d, want 0", len(material.AnchorOptions))
		}
	})
	t.Run("incomplete read", func(t *testing.T) {
		claimants := map[string][]IdentityMatch{
			"widget-service": {identityMatch(contractsv1.ContextFabricSubjectRepository, "repoA", "repoA")},
		}
		material := anchorOfferMaterial(claimants, false)
		if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("material.Missing = %v, want [subject_anchor]", material.Missing)
		}
		if len(material.AnchorOptions) != 0 {
			t.Errorf("len(material.AnchorOptions) = %d, want 0", len(material.AnchorOptions))
		}
	})
}

// TestAnchorOfferMaterial_MoreThanMaxCandidatesIsCapped pins the codex
// xhigh review finding (chaos-pivot-p1, round 2, finding 1): an ambiguous
// term matching more than structureOfferMaxOptions distinct
// (kind, canonical_id) claimants must never mint an AnchorOptions list the
// wire Validate() would then reject (len > 20) -- it must be capped,
// deterministically, to the first structureOfferMaxOptions candidates in
// the SAME sorted (kind, canonical_id) order anchorOfferMaterial already
// uses.
func TestAnchorOfferMaterial_MoreThanMaxCandidatesIsCapped(t *testing.T) {
	t.Parallel()
	claimants := make(map[string][]IdentityMatch, structureOfferMaxOptions+5)
	for i := 0; i < structureOfferMaxOptions+5; i++ {
		term := fmt.Sprintf("term-%02d", i)
		id := fmt.Sprintf("repo-%02d", i)
		claimants[term] = []IdentityMatch{identityMatch(contractsv1.ContextFabricSubjectRepository, id, id)}
	}
	material := anchorOfferMaterial(claimants, true)
	if len(material.AnchorOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.AnchorOptions) = %d, want %d (capped)", len(material.AnchorOptions), structureOfferMaxOptions)
	}
	// The kept candidates must be the lexicographically-first
	// canonical_ids ("repo-00".."repo-19"), not an arbitrary subset.
	seen := make(map[string]bool, len(material.AnchorOptions))
	for _, opt := range material.AnchorOptions {
		seen[opt.CanonicalID] = true
	}
	for i := 0; i < structureOfferMaxOptions; i++ {
		want := fmt.Sprintf("repo-%02d", i)
		if !seen[want] {
			t.Errorf("capped AnchorOptions missing %q, want the first %d ids kept", want, structureOfferMaxOptions)
		}
	}
}

// TestHandleOfferMaterial_DuplicateOccurrencesAreDeduped pins the codex
// xhigh review finding (chaos-pivot-p1, round 2, finding 1): the SAME
// handle text repeated in one question must not mint two options with
// identical content (and therefore identical receipt_id/option_id, which
// the wire Validate() rejects as a duplicate).
func TestHandleOfferMaterial_DuplicateOccurrencesAreDeduped(t *testing.T) {
	t.Parallel()
	material := handleOfferMaterial("PR 532 relates to PR 532 which also mentions PR 532")
	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1 (three identical occurrences deduped)", len(material.HandleOptions))
	}
	if material.HandleOptions[0].Value != "532" {
		t.Errorf("HandleOptions[0].Value = %q, want 532", material.HandleOptions[0].Value)
	}
}

// TestHandleOfferMaterial_MoreThanMaxDistinctMatchesIsCapped is the dedup
// fix's companion: enough DISTINCT handle-shaped tokens in one question
// must still be capped at structureOfferMaxOptions, for the same
// never-fail-Validate reasoning as the anchor cap above.
func TestHandleOfferMaterial_MoreThanMaxDistinctMatchesIsCapped(t *testing.T) {
	t.Parallel()
	question := "compare"
	for i := 0; i < structureOfferMaxOptions+5; i++ {
		question += fmt.Sprintf(" PR %d", 1000+i)
	}
	material := handleOfferMaterial(question)
	if len(material.HandleOptions) != structureOfferMaxOptions {
		t.Fatalf("len(material.HandleOptions) = %d, want %d (capped)", len(material.HandleOptions), structureOfferMaxOptions)
	}
}

func TestHandleOfferMaterial_NoGrammarMatchStillMissingWithEmptyOptions(t *testing.T) {
	t.Parallel()
	material := handleOfferMaterial("how healthy is the payments team")
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectHandle {
		t.Fatalf("material.Missing = %v, want [subject_handle]", material.Missing)
	}
	if len(material.HandleOptions) != 0 {
		t.Errorf("len(material.HandleOptions) = %d, want 0", len(material.HandleOptions))
	}
}

func TestHandleOfferMaterial_GrammarMatchOffersHandle(t *testing.T) {
	t.Parallel()
	material := handleOfferMaterial("what is the status of PR 532?")
	if len(material.Missing) != 1 || material.Missing[0] != contractsv1.ContextFabricStructureNeedSubjectHandle {
		t.Fatalf("material.Missing = %v, want [subject_handle]", material.Missing)
	}
	if len(material.HandleOptions) != 1 {
		t.Fatalf("len(material.HandleOptions) = %d, want 1", len(material.HandleOptions))
	}
	opt := material.HandleOptions[0]
	if opt.Kind != contractsv1.ContextFabricSubjectPullRequest || opt.PatternID != "pull_request_number" || opt.Value != "532" {
		t.Errorf("HandleOptions[0] = %+v, want kind=pull_request pattern_id=pull_request_number value=532", opt)
	}
	if opt.SourceColumn != "git_pull_requests.number" {
		t.Errorf("HandleOptions[0].SourceColumn = %q, want %q", opt.SourceColumn, "git_pull_requests.number")
	}
	if opt.OfferSource != contractsv1.ContextFabricStructureOfferEngine {
		t.Errorf("HandleOptions[0].OfferSource = %q, want engine", opt.OfferSource)
	}
}

func TestHandleOfferMaterial_MultipleGrammarMatchesOfferAll(t *testing.T) {
	t.Parallel()
	material := handleOfferMaterial("does PR 532 relate to CHAOS-3896?")
	if len(material.HandleOptions) != 2 {
		t.Fatalf("len(material.HandleOptions) = %d, want 2", len(material.HandleOptions))
	}
}

func TestCombineStructureOfferMaterial(t *testing.T) {
	t.Parallel()
	kind := contextfabric.StructureOfferMaterial{
		Missing:     []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind},
		KindOptions: []contractsv1.ContextFabricKindOption{{Kind: contractsv1.ContextFabricSubjectPullRequest}},
	}
	anchor := contextfabric.StructureOfferMaterial{
		Missing:       []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []contractsv1.ContextFabricAnchorOption{{CanonicalID: "repoA"}},
	}
	handle := contextfabric.StructureOfferMaterial{}

	combined := combineStructureOfferMaterial(kind, anchor, handle)
	wantMissing := []contractsv1.ContextFabricStructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind,
		contractsv1.ContextFabricStructureNeedSubjectAnchor,
	}
	if len(combined.Missing) != len(wantMissing) {
		t.Fatalf("combined.Missing = %v, want %v", combined.Missing, wantMissing)
	}
	for i, m := range wantMissing {
		if combined.Missing[i] != m {
			t.Errorf("combined.Missing[%d] = %q, want %q (order pin: kind before anchor)", i, combined.Missing[i], m)
		}
	}
	if len(combined.KindOptions) != 1 || len(combined.AnchorOptions) != 1 || len(combined.HandleOptions) != 0 {
		t.Errorf("combined = %+v, want 1 kind option, 1 anchor option, 0 handle options", combined)
	}
}
