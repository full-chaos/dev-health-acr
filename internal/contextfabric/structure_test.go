package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestCHAOS3900_StructureOrderingPin_ConfirmedKindNeverHitsReuseGate is
// P1.B's own acceptance pin, mirroring W1's OrderingPin test: a request
// confirming structure via receipt must NEVER reach the reuse gate at all
// (design brief §2.1/DP11's BYPASS, not a keyed lookup the way window's own
// ordering pin proves) -- canonicalizeStructure runs before tryReuse, and a
// non-empty confirmed set skips the tryReuse call entirely.
func TestCHAOS3900_StructureOrderingPin_ConfirmedKindNeverHitsReuseGate(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0001"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

	gateCalls := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			gateCalls++
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if gateCalls != 0 {
		t.Fatalf("reuse gate called %d times, want 0: a confirmed-structure request must BYPASS reuse entirely (DP11), not merely key differently", gateCalls)
	}
	if result.Reused {
		t.Fatal("result.Reused = true on a confirmed-structure request, want false")
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != "expected_kind" || entry.AppliedValue != string(SubjectPullRequest) || entry.Disposition != "applied" || entry.Provenance != "clarification_confirmed" {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=expected_kind applied_value=pull_request disposition=applied provenance=clarification_confirmed", entry)
	}
	if entry.PriorResultID != priorResult.ResultID || entry.ReceiptID != "kindr_confirm0001" {
		t.Errorf("result.ConfirmedStructure[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, priorResult.ResultID, "kindr_confirm0001")
	}
}

// TestCHAOS3900_UnresolvedStructureReceiptVetoesTheWholeRequest mirrors
// TestCHAOS3900_UnresolvedWindowReceiptVetoesTheWholeRequest exactly: a
// structure receipt naming a prior result that does not exist vetoes the
// WHOLE request, no reuse lookup, no interpretation, no inference
// substituted.
func TestCHAOS3900_UnresolvedStructureReceiptVetoesTheWholeRequest(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_01", ReceiptID: "ancr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if result.Reused {
		t.Fatal("result.Reused = true on a structure-veto terminal, want false")
	}
}

// TestCHAOS3900_StructureReceiptNamingWrongOfferListVetoes pins that a
// receipt matching NO entry in the member-appropriate offer list vetoes,
// even when the named prior result exists and carries a StructureNeeds
// block -- e.g. a kindr_ receipt naming an id that only exists among
// AnchorOptions (or nowhere at all) must never silently apply the wrong
// value.
func TestCHAOS3900_StructureReceiptNamingWrongOfferListVetoes(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0002"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_anchor"},
		AnchorOptions: []AnchorOption{
			{ReceiptID: "ancr_confirm0001", OptionID: "opt_repo", Label: "the repo", Kind: SubjectRepository, CanonicalID: "repository_ask_dev", ClaimantKey: "claimant_1", OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	// kindr_ receipt naming a result whose StructureNeeds carries NO
	// KindOptions at all (only AnchorOptions) -- must veto, not silently
	// find nothing and fall through to inference.
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
}

// TestCHAOS3900_PluralStructureReceiptsVetoAsConflict mirrors
// TestCHAOS3900_PluralWindowReceiptsVetoAsConflict: two or more receipts
// for the SAME member are ambiguous by construction, never
// first-match-wins.
func TestCHAOS3900_PluralStructureReceiptsVetoAsConflict(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: &resultStoreStub{},
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorHandleReceipts = []BoundSubjectReceipt{
		{ResultID: "result_prior_a_00001", ReceiptID: "handr_confirm0001"},
		{ResultID: "result_prior_b_00001", ReceiptID: "handr_confirm0002"},
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
}

// TestCHAOS3900_NoStructureReceipts_CanonicalizeStructureIsANoOp pins the
// common case: a request carrying none of the three structure-receipt
// fields canonicalizes to an empty, non-vetoing result, so it neither
// bypasses reuse nor blocks it -- disclosure alone (no receipts sent yet)
// must never change behavior, matching the design brief's P1 acceptance
// row ("disclosure alone creates nothing").
func TestCHAOS3900_NoStructureReceipts_CanonicalizeStructureIsANoOp(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), validInvestigationRequest())
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}
