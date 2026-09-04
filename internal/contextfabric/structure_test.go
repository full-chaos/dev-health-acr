package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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

// TestCHAOS3900_StructureOfferSnapshot_EchoesTheWholeOfferListNotJustTheWinner
// is P1.G's own acceptance pin (design brief §2.1 B5): a decisive result
// reached via structure confirmation carries structure_offer_snapshot --
// and it echoes EVERY offer the confirmed member's own source list carried,
// not just the one the receipt redeemed, so the Bridge can learn from the
// full (offered, selected) pair.
func TestCHAOS3900_StructureOfferSnapshot_EchoesTheWholeOfferListNotJustTheWinner(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0005"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
			{ReceiptID: "kindr_confirm0002", OptionID: "opt_wi", Label: "a work item", Kind: SubjectWorkItem, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

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
	})

	request := validInvestigationRequest()
	// Redeems ONLY the first option -- kindr_confirm0002 (the second
	// offer) is never named by any receipt.
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.StructureOfferSnapshot) != 2 {
		t.Fatalf("len(result.StructureOfferSnapshot) = %d, want 2 (BOTH offers, not just the redeemed one)", len(result.StructureOfferSnapshot))
	}
	seenRanks := map[int]string{}
	for _, entry := range result.StructureOfferSnapshot {
		if entry.Member != "expected_kind" {
			t.Errorf("snapshot entry %+v: member = %q, want expected_kind", entry, entry.Member)
		}
		if entry.OfferSource != "engine" {
			t.Errorf("snapshot entry %+v: offer_source = %q, want engine", entry, entry.OfferSource)
		}
		seenRanks[entry.Rank] = entry.OfferID
	}
	if seenRanks[0] != "opt_pr" || seenRanks[1] != "opt_wi" {
		t.Errorf("snapshot ranks = %+v, want rank 0 -> opt_pr, rank 1 -> opt_wi (source list order preserved)", seenRanks)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS3900_StructureOfferSnapshot_EmptyOnVeto pins the negative case:
// a vetoed structure confirmation carries NO offer snapshot -- nothing was
// confirmed, so there is nothing to echo.
func TestCHAOS3900_StructureOfferSnapshot_EmptyOnVeto(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_02", ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.StructureOfferSnapshot) != 0 {
		t.Errorf("len(result.StructureOfferSnapshot) = %d, want 0 on a vetoed structure confirmation", len(result.StructureOfferSnapshot))
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
			{ReceiptID: "ancr_confirm0001", OptionID: "opt_repo", Label: "the repo", Kind: SubjectRepository, CanonicalID: "repository_ask_dev", OfferSource: "engine"},
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
// TestCHAOS3963_ConflictVeto_EchoesAlreadyConfirmedMember pins the fix for
// composeConfirmedStructure's own former KNOWN GAP: an atomic batch veto
// must not silently discard a member the loop had ALREADY resolved before
// a LATER member triggered the veto -- "member A confirmed fine, member B
// is why the whole batch was rejected" must be visible in the echo, not
// indistinguishable from "nothing in this batch was ever confirmed."
// expected_kind resolves cleanly first (members are evaluated in fixed
// declared order: kind, anchor, handle); subject_handle then vetoes as a
// conflict (plural receipts) -- kind's own entry must still appear,
// re-dispositioned to vetoed_conflict (never "applied": the all-or-nothing
// rule means it was not).
func TestCHAOS3963_ConflictVeto_EchoesAlreadyConfirmedMember(t *testing.T) {
	t.Parallel()

	kindPrior := validInvestigationResult()
	kindPrior.ResultID = "result_prior_structure_3963a"
	kindPrior.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{kindPrior.ResultID: kindPrior}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: kindPrior.ResultID, ReceiptID: "kindr_confirm0001"}}
	request.PriorHandleReceipts = []BoundSubjectReceipt{
		{ResultID: "result_prior_structure_3963b", ReceiptID: "handr_confirm0001"},
		{ResultID: "result_prior_structure_3963c", ReceiptID: "handr_confirm0002"},
	}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationConflict {
		t.Fatalf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationConflict)
	}
	if len(canon.VetoedEntries) != 1 {
		t.Fatalf("len(canon.VetoedEntries) = %d, want 1 (the already-confirmed kind member)", len(canon.VetoedEntries))
	}
	entry := canon.VetoedEntries[0]
	if entry.Member != "expected_kind" || entry.AppliedValue != string(SubjectPullRequest) {
		t.Errorf("canon.VetoedEntries[0] = %+v, want member=expected_kind applied_value=%q", entry, SubjectPullRequest)
	}
	if entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
		t.Errorf("canon.VetoedEntries[0].Disposition = %q, want vetoed_conflict (never applied -- the batch was vetoed)", entry.Disposition)
	}
	if entry.PriorResultID != kindPrior.ResultID || entry.ReceiptID != "kindr_confirm0001" {
		t.Errorf("canon.VetoedEntries[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, kindPrior.ResultID, "kindr_confirm0001")
	}
	if err := entry.Validate(); err != nil {
		t.Errorf("canon.VetoedEntries[0] fails Validate(): %v", err)
	}
}

// TestCHAOS3963_UnresolvedVeto_EchoesBothTheConfirmedAndTriggeringMember
// extends the same pin to structureVetoConfirmationUnresolved AND to the
// triggering member itself: a handle reverify rejection has a real
// resolved value at the point of failure (appliedValueFor already
// succeeded -- only re-verification failed), so unlike a malformed/
// unloadable receipt, it IS echoable. Both members must appear: kind
// (already confirmed, re-dispositioned) and handle (the trigger, full
// attempted value included).
func TestCHAOS3963_UnresolvedVeto_EchoesBothTheConfirmedAndTriggeringMember(t *testing.T) {
	t.Parallel()

	kindPrior := validInvestigationResult()
	kindPrior.ResultID = "result_prior_structure_3963d"
	kindPrior.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	handlePrior, request := handleReceiptTestSetup()
	handlePrior.ResultID = "result_prior_structure_3963e"
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: handlePrior.ResultID, ReceiptID: "handr_confirm0001"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: kindPrior.ResultID, ReceiptID: "kindr_confirm0001"}}
	store := &staticResultStore{results: map[string]InvestigationResult{
		kindPrior.ResultID:   kindPrior,
		handlePrior.ResultID: handlePrior,
	}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, HandleVerificationReason) {
			return false, HandleVerificationNotFound
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Fatalf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.VetoedEntries) != 2 {
		t.Fatalf("len(canon.VetoedEntries) = %d, want 2 (kind already-confirmed + handle triggering)", len(canon.VetoedEntries))
	}
	kindEntry, handleEntry := canon.VetoedEntries[0], canon.VetoedEntries[1]
	if kindEntry.Member != "expected_kind" || kindEntry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
		t.Errorf("canon.VetoedEntries[0] = %+v, want member=expected_kind disposition=vetoed_unresolved", kindEntry)
	}
	if handleEntry.Member != "subject_handle" || handleEntry.AppliedValue != "532" {
		t.Errorf("canon.VetoedEntries[1] = %+v, want member=subject_handle applied_value=532", handleEntry)
	}
	if handleEntry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedUnresolved {
		t.Errorf("canon.VetoedEntries[1].Disposition = %q, want vetoed_unresolved", handleEntry.Disposition)
	}
	if handleEntry.PriorResultID != handlePrior.ResultID || handleEntry.ReceiptID != "handr_confirm0001" {
		t.Errorf("canon.VetoedEntries[1] receipt identity = (%q, %q), want (%q, %q)", handleEntry.PriorResultID, handleEntry.ReceiptID, handlePrior.ResultID, "handr_confirm0001")
	}
	for i, e := range canon.VetoedEntries {
		if err := e.Validate(); err != nil {
			t.Errorf("canon.VetoedEntries[%d] fails Validate(): %v", i, err)
		}
	}
}

// TestCHAOS3963_StructureVetoResult_ConflictVetoComposesTheEcho is the
// end-to-end pin (mirrors TestCHAOS3927P4_SupersededKindReceiptVetoesAsStale's
// own shape for the stale case): the wire-visible InvestigationResult
// returned from a full Investigate() call must actually carry the
// per-member echo, not just the internal requestStructureCanonicalization
// this file's other CHAOS-3963 tests assert on directly.
func TestCHAOS3963_StructureVetoResult_ConflictVetoComposesTheEcho(t *testing.T) {
	t.Parallel()

	kindPrior := validInvestigationResult()
	kindPrior.ResultID = "result_prior_structure_3963f"
	kindPrior.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{kindPrior.ResultID: kindPrior}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: kindPrior.ResultID, ReceiptID: "kindr_confirm0001"}}
	request.PriorHandleReceipts = []BoundSubjectReceipt{
		{ResultID: "result_prior_structure_3963g", ReceiptID: "handr_confirm0001"},
		{ResultID: "result_prior_structure_3963h", ReceiptID: "handr_confirm0002"},
	}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (CHAOS-3963: the already-confirmed kind member survives the conflict veto)", len(result.ConfirmedStructure))
	}
	if result.ConfirmedStructure[0].Member != "expected_kind" || result.ConfirmedStructure[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=expected_kind disposition=vetoed_conflict", result.ConfirmedStructure[0])
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

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
	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), validInvestigationRequest(), ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// supersedingResultStore wraps staticResultStore, additionally implementing
// StructureSupersessionChecker (CHAOS-3927 P4) so a test can pin exactly
// which (orgID, priorResultID, member) tuples canonicalizeStructure's own
// pre-flight consult must treat as already superseded -- keyed exactly
// like structure.go's own call, so a test constructs the key with the
// SAME three values it configured the request/prior result with.
type supersedingResultStore struct {
	*staticResultStore
	superseded map[string]bool
	checkErr   error
	checkCalls []string
}

func supersessionKey(orgID, priorResultID string, member contractsv1.ContextFabricStructureNeedKind) string {
	return orgID + "|" + priorResultID + "|" + string(member)
}

func (s *supersedingResultStore) IsStructureSuperseded(_ context.Context, orgID, priorResultID string, member contractsv1.ContextFabricStructureNeedKind) (bool, error) {
	key := supersessionKey(orgID, priorResultID, member)
	s.checkCalls = append(s.checkCalls, key)
	if s.checkErr != nil {
		return false, s.checkErr
	}
	return s.superseded[key], nil
}

// TestCHAOS3927P4_SupersededKindReceiptVetoesAsStale is P4's own
// pre-flight-detection acceptance pin (design brief §2.1/§2.5's "redeeming
// a superseded offer" row): a kindr_ receipt that resolves cleanly against
// its stored offer, and reverifies fine, must STILL veto if a
// StructureSupersessionChecker reports the (org, prior_result_id, member)
// tuple already claimed by a newer result -- the whole request terminates
// stale_superseded_offer, never applying the stale confirmation.
func TestCHAOS3927P4_SupersededKindReceiptVetoesAsStale(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0006"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	principal := reusePrincipal()
	store := &supersedingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		superseded:        map[string]bool{supersessionKey(principal.OrgID, priorResult.ResultID, "expected_kind"): true},
	}

	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
			t.Fatal("reuse gate must not be called on a structure-veto request")
			return InvestigationResult{}, false, nil
		}),
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), principal, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(store.checkCalls) == 0 {
		t.Fatal("IsStructureSuperseded was never consulted")
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 1 (the stale echo entry)", len(result.ConfirmedStructure))
	}
	entry := result.ConfirmedStructure[0]
	if entry.Member != "expected_kind" || entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Errorf("result.ConfirmedStructure[0] = %+v, want member=expected_kind disposition=vetoed_stale", entry)
	}
	if entry.PriorResultID != priorResult.ResultID || entry.ReceiptID != "kindr_confirm0001" {
		t.Errorf("result.ConfirmedStructure[0] receipt identity = (%q, %q), want (%q, %q)", entry.PriorResultID, entry.ReceiptID, priorResult.ResultID, "kindr_confirm0001")
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS3927P4_SupersessionCheckError_FailsClosed pins the
// authority-relevant-read fail-closed rule (design brief §2.0): when the
// wired StructureSupersessionChecker itself errors, canonicalizeStructure
// must treat that identically to a confirmed-stale claim -- never
// "assume fresh and apply the confirmation anyway."
func TestCHAOS3927P4_SupersessionCheckError_FailsClosed(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0007"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &supersedingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		checkErr:          errors.New("claim table unavailable"),
	}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q (fail-closed on an unreadable claim table)", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 1 || result.ConfirmedStructure[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Errorf("result.ConfirmedStructure = %+v, want one vetoed_stale entry", result.ConfirmedStructure)
	}
}

// TestCHAOS3927P4_NoCheckerWired_ConfirmationAppliesNormally proves the
// pre-flight consult is a pure optimization: staticResultStore does NOT
// implement StructureSupersessionChecker, and a request that would
// otherwise confirm cleanly must still confirm -- the type assertion
// missing must never itself veto anything (StructureSupersessionChecker's
// own doc comment: "this checker only ever shortcuts an otherwise-doomed
// round").
func TestCHAOS3927P4_NoCheckerWired_ConfirmationAppliesNormally(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0008"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Fatalf("canon.Veto = %q, want structureVetoNone (no checker wired -> nothing to veto on)", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Fatalf("len(canon.Confirmed) = %d, want 1", len(canon.Confirmed))
	}
}

// capturingStructureSelectionSink is a fake contextfabric.StructureSelectionSink
// (CHAOS-3927 P4) that records every event handed to it, for asserting
// captureStructureSelection's own wiring end to end.
type capturingStructureSelectionSink struct {
	events []StructureSelectionEvent
}

func (s *capturingStructureSelectionSink) RecordSelection(_ context.Context, event StructureSelectionEvent) {
	s.events = append(s.events, event)
}

// TestCHAOS3927P4_ConfirmedKindReceipt_CapturesAStructureSelectionEvent is
// the capture-pipeline acceptance pin (design brief §2.4/§3.1): a receipt
// that resolves cleanly to a confirmed member must hand a
// StructureSelectionEvent to the wired sink, carrying the FULL offer list
// (not just the winner), the correct Selected/Accepted values, and the
// human/agent mode+provenance proxies.
func TestCHAOS3927P4_ConfirmedKindReceipt_CapturesAStructureSelectionEvent(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0010"
	priorResult.Question = "was the widget-service release healthy?"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
			{ReceiptID: "kindr_confirm0002", OptionID: "opt_wi", Label: "a work item", Kind: SubjectWorkItem, OfferSource: "engine"},
		},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	sink := &capturingStructureSelectionSink{}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results:                store,
		StructureSelectionSink: sink,
		Graph:                  graphReaderStub{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}}},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return freshResult, nil
		}),
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
	})

	request := validInvestigationRequest()
	// Redeems the SECOND offer (rank 1) -- proves Accepted correctly
	// reports false for a non-top-ranked selection, not just true by
	// happenstance.
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0002"}}

	// CHAOS-3927 P4 (codex adversarial review fix): capture is now
	// DEFERRED until Save actually succeeds (requestStructureCanonicalization.
	// PendingSelections' own doc comment) -- this test must therefore drive
	// the FULL Investigate call, not canonicalizeStructure alone, to reach
	// the point capture is sent.
	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(result.ConfirmedStructure) != 1 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want a clean single confirmation", len(result.ConfirmedStructure))
	}
	if len(sink.events) != 1 {
		t.Fatalf("len(sink.events) = %d, want 1", len(sink.events))
	}
	event := sink.events[0]
	if event.OrgID != reusePrincipal().OrgID {
		t.Errorf("event.OrgID = %q, want %q", event.OrgID, reusePrincipal().OrgID)
	}
	if event.QuestionHash != QuestionHash(priorResult.Question) {
		t.Errorf("event.QuestionHash = %q, want QuestionHash(prior.Question)", event.QuestionHash)
	}
	if event.PriorResultID != priorResult.ResultID || event.Member != "expected_kind" {
		t.Errorf("event = %+v, want prior_result_id=%q member=expected_kind", event, priorResult.ResultID)
	}
	if len(event.Offered) != 2 {
		t.Fatalf("len(event.Offered) = %d, want 2 (the COMPLETE offer set, not just the winner)", len(event.Offered))
	}
	if event.Selected.ReceiptID != "kindr_confirm0002" || event.Selected.AppliedValue != string(SubjectWorkItem) {
		t.Errorf("event.Selected = %+v, want receipt_id=kindr_confirm0002 applied_value=work_item", event.Selected)
	}
	if event.Accepted {
		t.Error("event.Accepted = true for a rank-1 (non-top) selection, want false")
	}
	if event.SelectionMode != "agent_receipt" {
		t.Errorf("event.SelectionMode = %q, want agent_receipt (reusePrincipal carries no AuthenticationMethodWebAssertion)", event.SelectionMode)
	}
}

// TestCHAOS3927P4_VetoedReceipt_CapturesNoStructureSelectionEvent pins the
// negative case: a receipt that VETOES (never confirmed) must capture
// nothing -- mirrors captureClarificationSelection's own "a veto has
// nothing to label" placement.
func TestCHAOS3927P4_VetoedReceipt_CapturesNoStructureSelectionEvent(t *testing.T) {
	t.Parallel()

	store := &staticResultStore{results: map[string]InvestigationResult{}}
	sink := &capturingStructureSelectionSink{}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store, StructureSelectionSink: sink})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_does_not_exist_03", ReceiptID: "kindr_confirm0001"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto == structureVetoNone {
		t.Fatal("expected a veto for a receipt naming a nonexistent prior result")
	}
	if len(sink.events) != 0 {
		t.Errorf("len(sink.events) = %d, want 0 on a vetoed request", len(sink.events))
	}
}

// TestCHAOS3927P4_SaveTimeSupersessionConflict_TerminatesRoundAsStale
// covers the race the pre-flight consult cannot fully close (its own doc
// comment): a decisive result's Save call itself returns
// ErrStructureOfferSuperseded (the atomic claim lost at Save time, after
// the whole investigation already computed). Engine must discard the
// computed result -- never return it or a bare persistence error -- and
// terminate the round with the SAME stale_superseded_offer veto terminal a
// pre-flight detection would have produced.
func TestCHAOS3927P4_SaveTimeSupersessionConflict_TerminatesRoundAsStale(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0009"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	store := &supersessionRacingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
	}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()
	sink := &capturingStructureSelectionSink{}

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
		Results:                store,
		StructureSelectionSink: sink,
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want no error (the conflict must convert to a veto terminal, not surface as a raw error)", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if result.ResultID == freshResult.ResultID {
		t.Error("result.ResultID equals the discarded decisive result's own id -- the superseded computation must never be returned")
	}
	// CHAOS-3927 P4 (codex adversarial review fix): the losing round must
	// NEVER durably capture a selection for a result that was never
	// persisted (requestStructureCanonicalization.PendingSelections' own
	// doc comment) -- this is the acceptance pin for that fix.
	if len(sink.events) != 0 {
		t.Errorf("len(sink.events) = %d, want 0: a round that loses the Save-time supersession race must capture NOTHING (its result was never persisted)", len(sink.events))
	}
	if len(result.ConfirmedStructure) != 1 || result.ConfirmedStructure[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Errorf("result.ConfirmedStructure = %+v, want one vetoed_stale entry echoing the raced member", result.ConfirmedStructure)
	}
	if result.ConfirmedStructure[0].Member != "expected_kind" {
		t.Errorf("result.ConfirmedStructure[0].Member = %q, want expected_kind", result.ConfirmedStructure[0].Member)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// supersessionRacingResultStore wraps staticResultStore, making its FIRST
// Save call (the decisive, ConfirmedStructure-bearing one Investigate
// builds) return ErrStructureOfferSuperseded -- simulating the race
// pginvestigation.Store.Save's own atomic claim insert closes -- and every
// subsequent Save (the veto terminal Engine retries with) succeed
// normally, exactly as a real store would once the losing transaction has
// rolled back.
type supersessionRacingResultStore struct {
	*staticResultStore
	saveCalls int
	// conflictMembers is returned as ErrStructureOfferSuperseded.Members on
	// the FIRST Save call -- defaults to []{"expected_kind"} when left nil
	// (every pre-existing single-member test's own expectation).
	conflictMembers []contractsv1.ContextFabricStructureNeedKind
}

func (s *supersessionRacingResultStore) Save(ctx context.Context, principal storage.Principal, result InvestigationResult, snap SourceWatermarkSnapshot, epoch RebuildEpoch, axisKey string, retrieval ReuseRetrievalIdentity, prompts ReusePromptVersions, authorities ReuseVersionAuthorities, graphEpoch int64, _ string) error {
	s.saveCalls++
	if s.saveCalls == 1 {
		members := s.conflictMembers
		if members == nil {
			members = []contractsv1.ContextFabricStructureNeedKind{"expected_kind"}
		}
		return &ErrStructureOfferSuperseded{Members: members}
	}
	return s.staticResultStore.Save(ctx, principal, result, snap, epoch, axisKey, retrieval, prompts, authorities, graphEpoch, "")
}

// TestCHAOS3927P4_SaveTimeSupersessionConflict_EchoesEveryLostMember is
// codex round-3's own acceptance pin (MEDIUM finding): a single Save-time
// supersession race can legitimately conflict on MORE THAN ONE confirmed
// member at once (a request redeeming both expected_kind AND
// subject_anchor against a prior result a concurrent Save already claimed
// BOTH members of) -- the resulting stale terminal's ConfirmedStructure
// echo must carry ONE ENTRY PER LOST MEMBER, never silently collapse to
// just the first (the wire contract's own "one entry per carried member"
// rule, and the exact defect an earlier version of staleConfirmedStructureEntries
// -- then singular, staleConfirmedStructureEntry -- had).
func TestCHAOS3927P4_SaveTimeSupersessionConflict_EchoesEveryLostMember(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0011"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind", "subject_anchor"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm0001", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
		AnchorOptions: []AnchorOption{
			{
				ReceiptID: "ancr_confirm0001", OptionID: "opt_anchor", Label: "the widget-service repository",
				Kind: SubjectRepository, CanonicalID: "repository_widget_service",
				MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: "engine",
			},
		},
	}
	store := &supersessionRacingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		conflictMembers:   []contractsv1.ContextFabricStructureNeedKind{"expected_kind", "subject_anchor"},
	}

	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	freshResult := validInvestigationResult()

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
		AnchorVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		},
		Results: store,
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm0001"}}
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "ancr_confirm0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Errorf("result.Status = %q, want %q", result.Status, InvestigationNoMatch)
	}
	if len(result.ConfirmedStructure) != 2 {
		t.Fatalf("len(result.ConfirmedStructure) = %d, want 2 (BOTH lost members echoed, not just the first)", len(result.ConfirmedStructure))
	}
	seen := map[StructureNeedKind]bool{}
	for _, entry := range result.ConfirmedStructure {
		if entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
			t.Errorf("entry %+v: disposition = %q, want vetoed_stale", entry, entry.Disposition)
		}
		seen[entry.Member] = true
	}
	if !seen["expected_kind"] || !seen["subject_anchor"] {
		t.Errorf("result.ConfirmedStructure = %+v, want entries for BOTH expected_kind and subject_anchor", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// TestCHAOS3478_SaveTimeSupersessionConflict_PreservesPriorSubjectReceiptDispositions
// is codex round-2's own finding (Medium): the Save-time structure
// supersession race helper (structureSupersessionVetoResult ->
// structureVetoResult) is reachable AFTER prior-subject-receipt resolution
// has already run (the decisive Save attempt at engine.go carries
// result.SubjectResolution.PriorSubjectReceiptDispositions by the time it
// races) -- before this fix, the race terminal built a fresh
// SubjectResolution with no dispositions threaded through at all, silently
// dropping them on a request that carried BOTH a structure receipt (to
// trigger the race) and a prior-subject receipt (to prove disclosure
// survives it).
func TestCHAOS3478_SaveTimeSupersessionConflict_PreservesPriorSubjectReceiptDispositions(t *testing.T) {
	t.Parallel()

	priorKindResult := validInvestigationResult()
	priorKindResult.ResultID = "result_prior_structure_3478"
	priorKindResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{ReceiptID: "kindr_confirm34780", OptionID: "opt_pr", Label: "a pull request", Kind: SubjectPullRequest, OfferSource: "engine"},
		},
	}
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	priorSubjectResult := validInvestigationResult()
	priorSubjectResult.ResultID = "result_prior_subject_3478"
	priorSubjectResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_racedispo0001", Subject: project, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{project},
	}
	store := &supersessionRacingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{
			priorKindResult.ResultID:    priorKindResult,
			priorSubjectResult.ResultID: priorSubjectResult,
		}},
	}

	freshResult := validInvestigationResult()
	engine := mustReuseTestEngine(t, EngineDependencies{
		// The graph commits `project` so the prior-subject receipt
		// resolves (applied), not just survives the race unresolved.
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
	})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorKindResult.ResultID, ReceiptID: "kindr_confirm34780"}}
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: priorSubjectResult.ResultID, ReceiptID: "receipt_racedispo0001"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v, want no error (the conflict must convert to a veto terminal, not surface as a raw error)", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("result.Status = %q, want %q (the supersession race still holds)", result.Status, InvestigationNoMatch)
	}
	wantDispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{PriorResultID: priorSubjectResult.ResultID, ReceiptID: "receipt_racedispo0001", Disposition: contractsv1.ContextFabricPriorSubjectReceiptApplied},
	}
	if !reflect.DeepEqual(result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions) {
		t.Fatalf("PriorSubjectReceiptDispositions = %#v, want %#v -- the race terminal must not silently drop disclosure the decisive attempt already had", result.SubjectResolution.PriorSubjectReceiptDispositions, wantDispositions)
	}
	// codex round-3 nit: make the race conversion itself explicit, not just
	// its downstream effect -- Save was actually attempted twice (the
	// decisive round that lost the race, then the veto retry that won),
	// and the structure echo carries the expected_kind member the race
	// contested, disposition vetoed_stale.
	if store.saveCalls != 2 {
		t.Fatalf("store.saveCalls = %d, want 2 (the decisive Save that lost the race, then the veto retry)", store.saveCalls)
	}
	if len(result.ConfirmedStructure) != 1 || result.ConfirmedStructure[0].Member != "expected_kind" || result.ConfirmedStructure[0].Disposition != contractsv1.ContextFabricStructureDispositionVetoedStale {
		t.Fatalf("result.ConfirmedStructure = %+v, want one vetoed_stale entry for expected_kind", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Errorf("result fails Validate(): %v", err)
	}
}

// anchorReceiptTestSetup builds a prior result carrying one AnchorOption
// offer and a request naming its receipt -- mirrors handleReceiptTestSetup
// below, shared fixture for the three TestCHAOS3900_AnchorReceiptReverification*
// tests.
func anchorReceiptTestSetup() (priorResult InvestigationResult, request InvestigationRequest) {
	priorResult = validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0004"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_anchor"},
		AnchorOptions: []AnchorOption{
			{
				ReceiptID: "ancr_confirm0001", OptionID: "opt_anchor", Label: "the widget-service repository",
				Kind: SubjectRepository, CanonicalID: "repository_widget_service",
				MatchedTermHash: "aa11bb22cc33dd44ee55ff66",
				OfferSource:     "engine",
			},
		},
	}
	request = validInvestigationRequest()
	request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "ancr_confirm0001"}}
	return priorResult, request
}

// TestCHAOS3900_AnchorReceiptReverification_NilVerifierVetoes mirrors
// TestCHAOS3900_HandleReceiptReverification_NilVerifierVetoes exactly:
// AnchorVerifier's own fail-CLOSED default (applying an un-reverified
// anchor claim would be a false sense of safety, not a weaker-but-honest
// check).
func TestCHAOS3900_AnchorReceiptReverification_NilVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestCHAOS3900_AnchorReceiptReverification_VerifierRejectsVetoes proves a
// wired AnchorVerifier reporting the claim contested/lost vetoes the whole
// request atomically, same discipline as every other structure veto in
// this file.
func TestCHAOS3900_AnchorReceiptReverification_VerifierRejectsVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	calls := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		AnchorVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			calls++
			if orgID != reusePrincipal().OrgID || kind != SubjectRepository || canonicalID != "repository_widget_service" || matchedTermHash != "aa11bb22cc33dd44ee55ff66" {
				t.Errorf("AnchorVerifier called with (org=%q, kind=%q, canonical_id=%q, matched_term_hash=%q), want the stored offer's own content", orgID, kind, canonicalID, matchedTermHash)
			}
			return false, AnchorVerificationClaimContested
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
	if calls != 1 {
		t.Errorf("AnchorVerifier called %d times, want 1", calls)
	}
}

// TestCHAOS3900_AnchorReceiptReverification_VerifierAcceptsConfirms is this
// test's positive twin.
func TestCHAOS3900_AnchorReceiptReverification_VerifierAcceptsConfirms(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		AnchorVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			return true, AnchorVerificationValid
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Fatalf("len(canon.Confirmed) = %d, want 1", len(canon.Confirmed))
	}
	entry := canon.Confirmed[0]
	if entry.Member != "subject_anchor" || entry.AppliedValue != "repository_widget_service" {
		t.Errorf("canon.Confirmed[0] = %+v, want member=subject_anchor applied_value=repository_widget_service", entry)
	}
}

// TestCHAOS3900_AnchorReceiptReverification_InconsistentVerifierVetoes pins
// the codex xhigh review finding (chaos-pivot-p1, first round, finding 2):
// a MISCONFIGURED AnchorVerifier returning ok=true alongside a non-Valid
// reason (an internal inconsistency a real verifier should never produce,
// but a defensive caller cannot assume it never will) must NOT confirm the
// anchor -- reverify requires both ok==true AND reason==AnchorVerificationValid,
// never trusting the bool alone.
func TestCHAOS3900_AnchorReceiptReverification_InconsistentVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		AnchorVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			// Inconsistent on purpose: ok=true but a failure reason.
			return true, AnchorVerificationClaimLost
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q (an inconsistent verifier result must fail closed)", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// anchorMembershipReceiptTestSetup mirrors anchorReceiptTestSetup exactly,
// except the stored prior result carries CHAOS-4042's v2 (membership-
// verify) schema_version -- the redemption dispatch's own discriminator.
func anchorMembershipReceiptTestSetup() (priorResult InvestigationResult, request InvestigationRequest) {
	priorResult, request = anchorReceiptTestSetup()
	priorResult.SchemaVersion = InvestigationResultSchemaV2
	return priorResult, request
}

// TestCHAOS4042_AnchorMembershipReverification_NilVerifierVetoes mirrors
// TestCHAOS3900_AnchorReceiptReverification_NilVerifierVetoes for the v2
// path: an unwired Engine.anchorMembershipVerifier is NOT "trust the
// stored offer" -- same fail-CLOSED default as every other structure
// reverify hook.
func TestCHAOS4042_AnchorMembershipReverification_NilVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorMembershipReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestCHAOS4042_AnchorMembershipReverification_VerifierAcceptsConfirms
// proves a wired AnchorMembershipVerifier reporting valid confirms the
// anchor, and that it receives the FULL principal, the request's own
// RequestedScope, and the pinned binding -- not just an org id, unlike
// v1's AnchorVerifier (the ruling's own "re-authorize the selected node
// at redemption under B" requirement).
func TestCHAOS4042_AnchorMembershipReverification_VerifierAcceptsConfirms(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorMembershipReceiptTestSetup()
	request.RequestedScope = RequestedScope{RepositorySlugs: []string{"widget-service"}}
	pinnedBinding := ResolvedGraphBinding{GraphKey: "graph_org_1", Epoch: 42}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	calls := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		AnchorMembershipVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			calls++
			if principal.OrgID != reusePrincipal().OrgID {
				t.Errorf("AnchorMembershipVerifier principal.OrgID = %q, want %q", principal.OrgID, reusePrincipal().OrgID)
			}
			if len(scope.RepositorySlugs) != 1 || scope.RepositorySlugs[0] != "widget-service" {
				t.Errorf("AnchorMembershipVerifier scope = %+v, want the request's own RequestedScope", scope)
			}
			if binding != pinnedBinding {
				t.Errorf("AnchorMembershipVerifier binding = %+v, want %+v (the pinned binding passed to canonicalizeStructure)", binding, pinnedBinding)
			}
			if kind != SubjectRepository || canonicalID != "repository_widget_service" || matchedTermHash != "aa11bb22cc33dd44ee55ff66" {
				t.Errorf("AnchorMembershipVerifier called with (kind=%q, canonical_id=%q, matched_term_hash=%q), want the stored offer's own content", kind, canonicalID, matchedTermHash)
			}
			return true, AnchorVerificationValid
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, pinnedBinding)
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Fatalf("len(canon.Confirmed) = %d, want 1", len(canon.Confirmed))
	}
	if calls != 1 {
		t.Errorf("AnchorMembershipVerifier called %d times, want 1", calls)
	}
}

// TestCHAOS4042_AnchorMembershipReverification_VerifierRejectsVetoes is
// the negative twin: a membership verifier reporting the claim lost (the
// selected claimant vanished or was re-keyed) vetoes atomically, same
// discipline as v1's own claim-contested rejection.
func TestCHAOS4042_AnchorMembershipReverification_VerifierRejectsVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := anchorMembershipReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		AnchorMembershipVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			return false, AnchorVerificationClaimLost
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestCHAOS4042_CrossVersionDispatch proves the ruling's binding
// constraint made executable: redemption dispatches on the ISSUING STORED
// result's OWN schema_version, and the two verifiers are NEVER
// interchangeable -- a v1-stamped stored result must call ONLY
// AnchorVerifier (never AnchorMembershipVerifier, even when wired), a
// v2-stamped one must call ONLY AnchorMembershipVerifier (never
// AnchorVerifier, even when wired), and an unrecognized schema_version
// must call NEITHER -- an explicit reject, never a silent fallthrough to
// either verifier's rules.
func TestCHAOS4042_CrossVersionDispatch(t *testing.T) {
	t.Parallel()

	t.Run("v1 stored result never calls the membership verifier", func(t *testing.T) {
		t.Parallel()
		priorResult, request := anchorReceiptTestSetup() // v1-stamped
		store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
		v1Calls, v2Calls := 0, 0
		engine := mustReuseTestEngine(t, EngineDependencies{
			Results: store,
			AnchorVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v1Calls++
				return true, AnchorVerificationValid
			},
			AnchorMembershipVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v2Calls++
				return true, AnchorVerificationValid
			},
		})
		canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
		if canon.Veto != structureVetoNone || len(canon.Confirmed) != 1 {
			t.Fatalf("canon = %+v, want a confirmed anchor via the v1 verifier", canon)
		}
		if v1Calls != 1 || v2Calls != 0 {
			t.Errorf("v1 verifier called %d times (want 1), v2 verifier called %d times (want 0)", v1Calls, v2Calls)
		}
	})

	t.Run("v2 stored result never calls the legacy verifier", func(t *testing.T) {
		t.Parallel()
		priorResult, request := anchorMembershipReceiptTestSetup() // v2-stamped
		store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
		v1Calls, v2Calls := 0, 0
		engine := mustReuseTestEngine(t, EngineDependencies{
			Results: store,
			AnchorVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v1Calls++
				return true, AnchorVerificationValid
			},
			AnchorMembershipVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v2Calls++
				return true, AnchorVerificationValid
			},
		})
		canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
		if canon.Veto != structureVetoNone || len(canon.Confirmed) != 1 {
			t.Fatalf("canon = %+v, want a confirmed anchor via the v2 verifier", canon)
		}
		if v2Calls != 1 || v1Calls != 0 {
			t.Errorf("v2 verifier called %d times (want 1), v1 verifier called %d times (want 0)", v2Calls, v1Calls)
		}
	})

	t.Run("unrecognized schema_version calls neither verifier and vetoes", func(t *testing.T) {
		t.Parallel()
		priorResult, request := anchorReceiptTestSetup()
		priorResult.SchemaVersion = "context_fabric_investigation_result.v99"
		store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
		v1Calls, v2Calls := 0, 0
		engine := mustReuseTestEngine(t, EngineDependencies{
			Results: store,
			AnchorVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v1Calls++
				return true, AnchorVerificationValid
			},
			AnchorMembershipVerifier: func(context.Context, storage.Principal, RequestedScope, ResolvedGraphBinding, contractsv1.ContextFabricSubjectKind, string, string) (bool, AnchorVerificationReason) {
				v2Calls++
				return true, AnchorVerificationValid
			},
		})
		canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
		if canon.Veto != structureVetoConfirmationUnresolved {
			t.Errorf("canon.Veto = %q, want %q (an unrecognized schema_version must fail closed)", canon.Veto, structureVetoConfirmationUnresolved)
		}
		if v1Calls != 0 || v2Calls != 0 {
			t.Errorf("v1 verifier called %d times, v2 verifier called %d times, want 0 and 0 -- neither verifier may run for an unrecognized schema_version", v1Calls, v2Calls)
		}
	})
}

// handleReceiptTestSetup builds a prior result carrying one HandleOption
// offer and a request naming its receipt -- shared fixture for the three
// TestCHAOS3900_HandleReceiptReverification* tests below.
func handleReceiptTestSetup() (priorResult InvestigationResult, request InvestigationRequest) {
	priorResult = validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0003"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_handle"},
		HandleOptions: []HandleOption{
			{
				ReceiptID: "handr_confirm0001", OptionID: "opt_handle", Label: "PR #532",
				Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532",
				OfferSource: "engine",
			},
		},
	}
	request = validInvestigationRequest()
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "handr_confirm0001"}}
	return priorResult, request
}

// TestCHAOS3900_HandleReceiptReverification_NilVerifierVetoes pins P1.E's
// fail-CLOSED default (HandleVerifier's own doc comment): an unwired
// Engine.handleVerifier is NOT "trust the stored offer" the way a nil
// ClarificationSelectionSink degrades to "capture nothing" -- applying an
// un-reverified handle value would be a false sense of safety. A deployment
// that never wires HandleVerifier and never mints handle offers (P1.C' not
// yet built) never exercises this path; this test proves what happens the
// moment it does.
func TestCHAOS3900_HandleReceiptReverification_NilVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := handleReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestCHAOS3900_HandleReceiptReverification_VerifierRejectsVetoes proves a
// wired HandleVerifier that reports the stored value invalid (grammar
// mismatch, source row gone, whatever the reason) vetoes the whole request
// exactly like an unresolved receipt -- no partial application, no
// inference substituted (design brief §2.5 rows 2/3, same atomic-veto
// discipline every other structure veto in this file upholds).
func TestCHAOS3900_HandleReceiptReverification_VerifierRejectsVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := handleReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	calls := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			calls++
			if orgID != reusePrincipal().OrgID || kind != SubjectPullRequest || patternID != "pull_request_number" || value != "532" {
				t.Errorf("HandleVerifier called with (org=%q, kind=%q, pattern=%q, value=%q), want the stored offer's own content", orgID, kind, patternID, value)
			}
			return false, HandleVerificationNotFound
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
	if calls != 1 {
		t.Errorf("HandleVerifier called %d times, want 1", calls)
	}
}

// TestCHAOS3900_HandleReceiptReverification_VerifierAcceptsConfirms is this
// test's positive twin: a wired HandleVerifier that reports the stored
// value still valid lets the receipt confirm normally, mirroring the kind
// receipt's own OrderingPin test (TestCHAOS3900_StructureOrderingPin_ConfirmedKindNeverHitsReuseGate).
func TestCHAOS3900_HandleReceiptReverification_VerifierAcceptsConfirms(t *testing.T) {
	t.Parallel()

	priorResult, request := handleReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			return true, HandleVerificationValid
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Fatalf("len(canon.Confirmed) = %d, want 1", len(canon.Confirmed))
	}
	entry := canon.Confirmed[0]
	if entry.Member != "subject_handle" || entry.AppliedValue != "532" {
		t.Errorf("canon.Confirmed[0] = %+v, want member=subject_handle applied_value=532", entry)
	}
}

// TestCHAOS3900_HandleReceiptReverification_InconsistentVerifierVetoes pins
// the codex xhigh review finding (chaos-pivot-p1, first round, finding 2):
// same reasoning as the anchor twin above -- ok=true with a non-Valid
// reason must not confirm.
func TestCHAOS3900_HandleReceiptReverification_InconsistentVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := handleReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			// Inconsistent on purpose: ok=true but a failure reason.
			return true, HandleVerificationCensusUnavailable
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q (an inconsistent verifier result must fail closed)", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// candidateReceiptTestSetup builds a prior result carrying one
// CandidateOption offer and a request naming its receipt -- shared fixture
// for the four TestCHAOS4012_CandidateReceiptReverification* tests below,
// same shape as handleReceiptTestSetup above.
func candidateReceiptTestSetup() (priorResult InvestigationResult, request InvestigationRequest) {
	priorResult = validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0004"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_candidate"},
		CandidateOptions: []CandidateOption{
			{
				ReceiptID: "candr_confirm0001", OptionID: "opt_candidate", Label: "repo-b",
				Kind: SubjectRepository, CanonicalID: "repository:r2",
				OfferSource: "engine",
			},
		},
	}
	request = validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "candr_confirm0001"}}
	return priorResult, request
}

// TestCHAOS4012_CandidateReceiptReverification_NilVerifierVetoes pins the
// SAME fail-CLOSED default HandleVerifier/AnchorMembershipVerifier already
// uphold (CandidateVerifier's own doc comment): an unwired
// Engine.candidateVerifier is NOT "trust the stored offer".
func TestCHAOS4012_CandidateReceiptReverification_NilVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := candidateReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestCHAOS4012_CandidateReceiptReverification_VerifierRejectsVetoes proves a
// wired CandidateVerifier that reports the stored (kind, canonical_id) gone
// or unauthorized vetoes the whole request exactly like an unresolved
// receipt -- no partial application, same atomic-veto discipline as every
// other structure veto in this file.
func TestCHAOS4012_CandidateReceiptReverification_VerifierRejectsVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := candidateReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	calls := 0
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		CandidateVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, CandidateVerificationReason) {
			calls++
			if kind != SubjectRepository || canonicalID != "repository:r2" {
				t.Errorf("CandidateVerifier called with (kind=%q, canonical_id=%q), want the stored offer's own content", kind, canonicalID)
			}
			return false, CandidateVerificationClaimLost
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
	if calls != 1 {
		t.Errorf("CandidateVerifier called %d times, want 1", calls)
	}
}

// TestCHAOS4012_CandidateReceiptReverification_VerifierAcceptsConfirms is
// this test's positive twin: a wired CandidateVerifier that reports the
// stored (kind, canonical_id) still valid lets the receipt confirm
// normally.
func TestCHAOS4012_CandidateReceiptReverification_VerifierAcceptsConfirms(t *testing.T) {
	t.Parallel()

	priorResult, request := candidateReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		CandidateVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, CandidateVerificationReason) {
			return true, CandidateVerificationValid
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Errorf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Fatalf("len(canon.Confirmed) = %d, want 1", len(canon.Confirmed))
	}
	entry := canon.Confirmed[0]
	if entry.Member != "subject_candidate" || entry.AppliedValue != "repository:r2" {
		t.Errorf("canon.Confirmed[0] = %+v, want member=subject_candidate applied_value=repository:r2", entry)
	}
}

// TestCHAOS4012_CandidateReceiptReverification_InconsistentVerifierVetoes
// pins the same reasoning as the anchor/handle twins above: ok=true with a
// non-Valid reason must not confirm.
func TestCHAOS4012_CandidateReceiptReverification_InconsistentVerifierVetoes(t *testing.T) {
	t.Parallel()

	priorResult, request := candidateReceiptTestSetup()
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		CandidateVerifier: func(ctx context.Context, principal storage.Principal, scope RequestedScope, binding ResolvedGraphBinding, kind contractsv1.ContextFabricSubjectKind, canonicalID string) (bool, CandidateVerificationReason) {
			// Inconsistent on purpose: ok=true but a failure reason.
			return true, CandidateVerificationGraphUnverifiable
		},
	})

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationUnresolved {
		t.Errorf("canon.Veto = %q, want %q (an inconsistent verifier result must fail closed)", canon.Veto, structureVetoConfirmationUnresolved)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("len(canon.Confirmed) = %d, want 0", len(canon.Confirmed))
	}
}

// TestMintStructureReceiptID_DeterministicAndUniqueWithinResult pins the
// P1.C receipt-minting contract (team-lead ruling): deterministic from
// (result identity, member, content) -- same inputs mint the SAME id
// every time (idempotent re-mint on retry, stable under replay) -- and
// distinct content for the same member/result mints a DIFFERENT id
// (unique-within-result by construction).
func TestMintStructureReceiptID_DeterministicAndUniqueWithinResult(t *testing.T) {
	t.Parallel()

	t.Run("deterministic: identical inputs mint the identical id", func(t *testing.T) {
		a := mintStructureReceiptID("expected_kind", "result_00000001", "pull_request")
		b := mintStructureReceiptID("expected_kind", "result_00000001", "pull_request")
		if a != b {
			t.Errorf("mintStructureReceiptID() = %q then %q, want identical", a, b)
		}
	})
	t.Run("distinct content, same member and result, mints a distinct id", func(t *testing.T) {
		a := mintStructureReceiptID("expected_kind", "result_00000001", "pull_request")
		b := mintStructureReceiptID("expected_kind", "result_00000001", "work_item")
		if a == b {
			t.Errorf("mintStructureReceiptID() = %q for both pull_request and work_item content, want distinct", a)
		}
	})
	t.Run("distinct member, same result and content, mints a distinct id (different namespace)", func(t *testing.T) {
		a := mintStructureReceiptID("expected_kind", "result_00000001", "x")
		b := mintStructureReceiptID("subject_anchor", "result_00000001", "x")
		if a == b {
			t.Errorf("mintStructureReceiptID() = %q for both expected_kind and subject_anchor, want distinct", a)
		}
	})
	t.Run("distinct result, same member and content, mints a distinct id", func(t *testing.T) {
		a := mintStructureReceiptID("expected_kind", "result_00000001", "pull_request")
		b := mintStructureReceiptID("expected_kind", "result_00000002", "pull_request")
		if a == b {
			t.Errorf("mintStructureReceiptID() = %q for both result_00000001 and result_00000002, want distinct", a)
		}
	})
	t.Run("carries the member's own namespace prefix", func(t *testing.T) {
		cases := map[StructureNeedKind]string{
			"expected_kind":  "kindr_",
			"subject_anchor": "ancr_",
			"subject_handle": "handr_",
			"window":         "winr_",
		}
		for member, prefix := range cases {
			id := mintStructureReceiptID(member, "result_00000001", "content")
			if len(id) < len(prefix) || id[:len(prefix)] != prefix {
				t.Errorf("mintStructureReceiptID(%q, ...) = %q, want prefix %q", member, id, prefix)
			}
			// receipt_id bounds: min 8, max 256 (validate_context_fabric_window.go / structure.go).
			if len(id) < 8 || len(id) > 256 {
				t.Errorf("mintStructureReceiptID(%q, ...) = %q, length %d violates the [8,256] receipt_id bound", member, id, len(id))
			}
		}
	})
}

// TestStructureOfferContent_EveryFieldIsLoadBearing pins the team-lead
// ruling made after round-1 finding 7 (offer_source/prior_version_id/
// prior_entry_id omitted from mint content) recurred as round-2 finding 3
// (label omitted): mint content is now structureOfferContent's own
// json.Marshal of the WHOLE option struct, not a hand-picked field list,
// specifically so this omission class becomes structurally impossible.
// This test enforces that mechanically: for every exported field on each
// of the three option types EXCEPT receipt_id/option_id (computed FROM
// the content, never part of it), changing that ONE field's value must
// change structureOfferContent's own output. If a future change reverts
// to a hand-picked field list and forgets one, this test fails on that
// field by name -- and it needs no update when a new field is added to
// any of these three types, unlike the hand-picked lists it replaces.
func TestStructureOfferContent_EveryFieldIsLoadBearing(t *testing.T) {
	t.Parallel()
	t.Run("KindOption", func(t *testing.T) {
		t.Parallel()
		assertContentFieldsLoadBearing(t, contractsv1.ContextFabricKindOption{})
	})
	t.Run("AnchorOption", func(t *testing.T) {
		t.Parallel()
		assertContentFieldsLoadBearing(t, contractsv1.ContextFabricAnchorOption{})
	})
	t.Run("HandleOption", func(t *testing.T) {
		t.Parallel()
		assertContentFieldsLoadBearing(t, contractsv1.ContextFabricHandleOption{})
	})
}

// assertContentFieldsLoadBearing takes a zero value of one option type,
// sets every field OTHER than ReceiptID/OptionID to a distinct baseline
// string value, then flips each of those fields one at a time and asserts
// structureOfferContent's output changes every time. Uses reflection so
// it needs no update when T grows a new field -- the whole point of the
// json.Marshal-based content derivation this test is pinning.
func assertContentFieldsLoadBearing(t *testing.T, zero interface{}) {
	t.Helper()
	typ := reflect.TypeOf(zero)
	base := reflect.New(typ).Elem()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "ReceiptID" || f.Name == "OptionID" {
			continue
		}
		if base.Field(i).Kind() != reflect.String {
			t.Fatalf("field %s is not string-kind; this test needs a case added for its kind", f.Name)
		}
		base.Field(i).SetString("base-" + f.Name)
	}
	baseContent := structureOfferContent(base.Interface())

	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "ReceiptID" || f.Name == "OptionID" {
			continue
		}
		mutated := reflect.New(typ).Elem()
		mutated.Set(base)
		mutated.Field(i).SetString("mutated-" + f.Name)
		mutatedContent := structureOfferContent(mutated.Interface())
		if mutatedContent == baseContent {
			t.Errorf("changing field %s alone did not change structureOfferContent's output -- this field is not load-bearing in the minted id (the round-1-finding-7/round-2-finding-3 omission class)", f.Name)
		}
	}
}

func TestMintStructureOptionID_Deterministic(t *testing.T) {
	t.Parallel()
	a := mintStructureOptionID("expected_kind", "result_00000001", "pull_request")
	b := mintStructureOptionID("expected_kind", "result_00000001", "pull_request")
	if a != b {
		t.Errorf("mintStructureOptionID() = %q then %q, want identical", a, b)
	}
	c := mintStructureOptionID("expected_kind", "result_00000001", "work_item")
	if a == c {
		t.Errorf("mintStructureOptionID() = %q for both pull_request and work_item content, want distinct", a)
	}
	// option_id bounds: min 1, max 256.
	if len(a) < 1 || len(a) > 256 {
		t.Errorf("mintStructureOptionID() = %q, length %d violates the [1,256] option_id bound", a, len(a))
	}
}

// TestComposeStructureNeeds_EmptyMissingIsNil pins the nil-means-nothing-
// in-play convention (mirrors composeEffectiveWindow's own nil rule).
func TestComposeStructureNeeds_EmptyMissingIsNil(t *testing.T) {
	t.Parallel()
	needs := composeStructureNeeds(StructureOfferMaterial{}, "result_00000001")
	if needs != nil {
		t.Errorf("composeStructureNeeds(empty material) = %+v, want nil", needs)
	}
}

// TestStructureNeedsWouldDisclose_MatchesComposeStructureNeeds is a
// CONTRACT LOCK (CHAOS-3927 P1 post-merge invariance measurement,
// team-lead ruling, hardened after codex xhigh review round 1 on
// chaos-replay-structure-needs-coverage): the FIRST version of this lock
// tested composeStructureNeeds against two hand-picked Missing shapes,
// pinning that StructureNeedsWouldDisclose's SEPARATE len(Missing) != 0
// expression currently agreed with it -- nothing stopped the two being
// edited independently later (codex's own example: narrowing the harness's
// copy to len(Missing) > 1 would have left that test green while
// undercounting every single-missing disclosure). StructureNeedsWouldDisclose
// is now the ONE function composeStructureNeeds itself calls (structure.go),
// so this test instead exhaustively cross-checks that shared function
// against every closed-vocabulary ContextFabricStructureNeedKind member
// PLUS the empty case -- proving nil/non-nil tracks
// StructureNeedsWouldDisclose's own return for the FULL vocabulary, not
// just one arbitrarily-chosen member (closing codex's "never tests
// subject_anchor" gap too).
func TestStructureNeedsWouldDisclose_MatchesComposeStructureNeeds(t *testing.T) {
	t.Parallel()
	t.Run("empty Missing", func(t *testing.T) {
		t.Parallel()
		material := StructureOfferMaterial{}
		want := StructureNeedsWouldDisclose(material)
		if want {
			t.Fatal("StructureNeedsWouldDisclose(empty material) = true, want false")
		}
		needs := composeStructureNeeds(material, "result_00000001")
		if (needs != nil) != want {
			t.Errorf("composeStructureNeeds(empty material) non-nil = %v, want %v (StructureNeedsWouldDisclose's own answer)", needs != nil, want)
		}
	})
	for _, kind := range contractsv1.ContextFabricStructureNeedKindVocabulary() {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			material := StructureOfferMaterial{Missing: []StructureNeedKind{kind}}
			want := StructureNeedsWouldDisclose(material)
			if !want {
				t.Fatalf("StructureNeedsWouldDisclose(Missing=[%s]) = false, want true (a single valid vocabulary member is always non-empty Missing)", kind)
			}
			needs := composeStructureNeeds(material, "result_00000001")
			if (needs != nil) != want {
				t.Errorf("composeStructureNeeds(Missing=[%s]) non-nil = %v, want %v (StructureNeedsWouldDisclose's own answer)", kind, needs != nil, want)
			}
		})
	}
}

// TestComposeStructureNeeds_MintsReceiptAndOptionIDs pins that
// composeStructureNeeds actually fills in the ids StructureOfferMaterial
// left unset, deterministically from the given resultID.
func TestComposeStructureNeeds_MintsReceiptAndOptionIDs(t *testing.T) {
	t.Parallel()
	material := StructureOfferMaterial{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: "engine"},
			{Kind: SubjectWorkItem, Label: "a work item", OfferSource: "engine"},
		},
	}
	needs := composeStructureNeeds(material, "result_00000001")
	if needs == nil {
		t.Fatal("composeStructureNeeds() = nil, want a non-nil block")
	}
	if len(needs.KindOptions) != 2 {
		t.Fatalf("len(needs.KindOptions) = %d, want 2", len(needs.KindOptions))
	}
	seenReceipts := map[string]bool{}
	seenOptions := map[string]bool{}
	for _, opt := range needs.KindOptions {
		if opt.ReceiptID == "" || opt.OptionID == "" {
			t.Errorf("KindOption for %q has an unset ReceiptID/OptionID after composeStructureNeeds: (%q, %q)", opt.Kind, opt.ReceiptID, opt.OptionID)
		}
		if seenReceipts[opt.ReceiptID] {
			t.Errorf("duplicate ReceiptID %q across sibling KindOptions", opt.ReceiptID)
		}
		seenReceipts[opt.ReceiptID] = true
		if seenOptions[opt.OptionID] {
			t.Errorf("duplicate OptionID %q across sibling KindOptions", opt.OptionID)
		}
		seenOptions[opt.OptionID] = true
		if err := opt.Validate(); err != nil {
			t.Errorf("minted KindOption fails Validate(): %v (%+v)", err, opt)
		}
	}
	if err := needs.Validate(); err != nil {
		t.Errorf("composeStructureNeeds() result fails Validate(): %v", err)
	}
}

// TestComposeStructureNeeds_DeterministicAcrossCalls pins that composing
// the SAME material against the SAME resultID twice mints byte-identical
// ids -- the idempotent-re-mint-on-retry property the receipt-minting
// primitive itself already proves, exercised here through the full
// composition path.
func TestComposeStructureNeeds_DeterministicAcrossCalls(t *testing.T) {
	t.Parallel()
	material := StructureOfferMaterial{
		Missing:     []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: "engine"}},
	}
	a := composeStructureNeeds(material, "result_00000001")
	b := composeStructureNeeds(material, "result_00000001")
	if a.KindOptions[0].ReceiptID != b.KindOptions[0].ReceiptID || a.KindOptions[0].OptionID != b.KindOptions[0].OptionID {
		t.Errorf("composeStructureNeeds() minted different ids on repeated calls with identical inputs: %+v vs %+v", a.KindOptions[0], b.KindOptions[0])
	}
}

// TestCHAOS3900_StructureNeeds_ComposedOnSubjectlessTerminal is the P1.C
// end-to-end wiring pin: a subjectless resolution carrying
// StructureOfferMaterial reaches the served result's own StructureNeeds
// field, fully minted (ReceiptID/OptionID set), through the real
// Investigate() call chain -- not just composeStructureNeeds in
// isolation.
func TestCHAOS3900_StructureNeeds_ComposedOnSubjectlessTerminal(t *testing.T) {
	t.Parallel()

	material := StructureOfferMaterial{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{
			{Kind: SubjectPullRequest, Label: "a pull request", OfferSource: "engine"},
			{Kind: SubjectWorkItem, Label: "a work item", OfferSource: "engine"},
		},
	}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Graph: graphReaderStub{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			material:   material,
		},
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Results: &resultStoreStub{},
	})

	result, err := engine.Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.StructureNeeds == nil {
		t.Fatal("result.StructureNeeds = nil, want the disclosure block composed from the resolved StructureOfferMaterial")
	}
	if len(result.StructureNeeds.KindOptions) != 2 {
		t.Fatalf("len(result.StructureNeeds.KindOptions) = %d, want 2", len(result.StructureNeeds.KindOptions))
	}
	for _, opt := range result.StructureNeeds.KindOptions {
		if opt.ReceiptID == "" || opt.OptionID == "" {
			t.Errorf("served KindOption for %q has an unset ReceiptID/OptionID: %+v", opt.Kind, opt)
		}
	}
	if err := result.Validate(); err != nil {
		t.Errorf("served result fails Validate(): %v", err)
	}
}

// TestRecordStructureReceiptTelemetry_Applied pins the success shape: every
// receipt-bearing member gets exactly one StructureReceiptApplied call.
func TestRecordStructureReceiptTelemetry_Applied(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "kindr_1"}}
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "handr_1"}}
	canon := requestStructureCanonicalization{Confirmed: []confirmedStructureMember{
		{Member: "expected_kind"}, {Member: "subject_handle"},
	}}

	recordStructureReceiptTelemetry(context.Background(), telemetry, reusePrincipal(), request, canon)

	if len(telemetry.structureReceipts) != 2 {
		t.Fatalf("len(structureReceipts) = %d, want 2 (kind and handle, both receipt-bearing)", len(telemetry.structureReceipts))
	}
	for _, rec := range telemetry.structureReceipts {
		if rec.outcome != StructureReceiptApplied {
			t.Errorf("record %+v: outcome = %q, want %q", rec, rec.outcome, StructureReceiptApplied)
		}
	}
}

// TestRecordStructureReceiptTelemetry_VetoAppliesTheSameOutcomeToEveryBearingMember
// pins the atomicity shape: a veto reports its OWN reason for every
// receipt-bearing member, never "applied" for any of them, even a member
// that was not itself the cause.
func TestRecordStructureReceiptTelemetry_VetoAppliesTheSameOutcomeToEveryBearingMember(t *testing.T) {
	t.Parallel()

	t.Run("unresolved", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		request := validInvestigationRequest()
		request.PriorAnchorReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "ancr_1"}}
		canon := requestStructureCanonicalization{Veto: structureVetoConfirmationUnresolved}

		recordStructureReceiptTelemetry(context.Background(), telemetry, reusePrincipal(), request, canon)

		if len(telemetry.structureReceipts) != 1 || telemetry.structureReceipts[0].outcome != StructureReceiptUnresolved {
			t.Errorf("structureReceipts = %+v, want exactly one unresolved record", telemetry.structureReceipts)
		}
	})
	t.Run("conflict, multiple bearing members share the veto's own outcome", func(t *testing.T) {
		telemetry := &recordingTelemetry{}
		request := validInvestigationRequest()
		request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "kindr_1"}, {ResultID: "r1", ReceiptID: "kindr_2"}}
		request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "handr_1"}}
		canon := requestStructureCanonicalization{Veto: structureVetoConfirmationConflict}

		recordStructureReceiptTelemetry(context.Background(), telemetry, reusePrincipal(), request, canon)

		if len(telemetry.structureReceipts) != 2 {
			t.Fatalf("len(structureReceipts) = %d, want 2 (kind and handle, both receipt-bearing)", len(telemetry.structureReceipts))
		}
		for _, rec := range telemetry.structureReceipts {
			if rec.outcome != StructureReceiptConflict {
				t.Errorf("record %+v: outcome = %q, want %q (atomicity: the whole batch shares one outcome)", rec, rec.outcome, StructureReceiptConflict)
			}
		}
	})
}

// TestRecordStructureReceiptTelemetry_NoReceiptsRecordsNothing pins the
// common case: a request carrying no structure receipts at all must not
// emit a spurious call for any member.
func TestRecordStructureReceiptTelemetry_NoReceiptsRecordsNothing(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	recordStructureReceiptTelemetry(context.Background(), telemetry, reusePrincipal(), validInvestigationRequest(), requestStructureCanonicalization{})
	if len(telemetry.structureReceipts) != 0 {
		t.Errorf("structureReceipts = %+v, want empty", telemetry.structureReceipts)
	}
}

// TestRecordStructureReceiptTelemetry_CandidateReceiptsRecordApplied is
// CHAOS-4355's own red-first proof (codex xhigh round-1 finding): CHAOS-4012
// added subject_candidate as a 4th receipt-bearing member
// (request.PriorCandidateReceipts, canonicalizeStructure's own receipt-member
// loop), but recordStructureReceiptTelemetry's bearing slice was never
// widened to match -- a redeemed candidate receipt emitted no
// cf_structure_receipt{member=subject_candidate,...} decision-basis signal
// at all, silently, exactly the class the standing telemetry-same-change
// order exists to catch.
func TestRecordStructureReceiptTelemetry_CandidateReceiptsRecordApplied(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "r1", ReceiptID: "candr_1"}}
	canon := requestStructureCanonicalization{Confirmed: []confirmedStructureMember{{Member: "subject_candidate"}}}

	recordStructureReceiptTelemetry(context.Background(), telemetry, reusePrincipal(), request, canon)

	if len(telemetry.structureReceipts) != 1 {
		t.Fatalf("len(structureReceipts) = %d, want 1 (subject_candidate, receipt-bearing)", len(telemetry.structureReceipts))
	}
	if telemetry.structureReceipts[0].member != contractsv1.ContextFabricStructureNeedSubjectCandidate {
		t.Errorf("member = %q, want subject_candidate", telemetry.structureReceipts[0].member)
	}
	if telemetry.structureReceipts[0].outcome != StructureReceiptApplied {
		t.Errorf("outcome = %q, want applied", telemetry.structureReceipts[0].outcome)
	}
}

// TestRecordStructureNeedsTelemetry_DisclosedAndOfferCounts pins the
// disclosure + per-(member,source) count shape together, including the
// zero-count-contributes-no-call rule.
func TestRecordStructureNeedsTelemetry_DisclosedAndOfferCounts(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	needs := &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind", "subject_anchor"},
		KindOptions: []KindOption{
			{Kind: SubjectPullRequest, OfferSource: "engine"},
			{Kind: SubjectWorkItem, OfferSource: "engine"},
		},
		AnchorOptions: []AnchorOption{
			{CanonicalID: "repoA", OfferSource: "engine"},
		},
	}

	recordStructureNeedsTelemetry(context.Background(), telemetry, reusePrincipal(), needs)

	if len(telemetry.structureNeedsDisclosed) != 2 {
		t.Fatalf("len(structureNeedsDisclosed) = %d, want 2", len(telemetry.structureNeedsDisclosed))
	}
	if telemetry.structureNeedsDisclosed[0] != "expected_kind" || telemetry.structureNeedsDisclosed[1] != "subject_anchor" {
		t.Errorf("structureNeedsDisclosed = %v, want [expected_kind subject_anchor] in Missing's own order", telemetry.structureNeedsDisclosed)
	}

	if len(telemetry.structureOfferCounts) != 2 {
		t.Fatalf("len(structureOfferCounts) = %d, want 2 (kind/engine and anchor/engine -- handle contributes nothing, it carried no offers)", len(telemetry.structureOfferCounts))
	}
	want := map[StructureNeedKind]int{"expected_kind": 2, "subject_anchor": 1}
	for _, rec := range telemetry.structureOfferCounts {
		if rec.source != "engine" {
			t.Errorf("record %+v: source = %q, want engine", rec, rec.source)
		}
		if rec.count != want[rec.member] {
			t.Errorf("record %+v: count = %d, want %d", rec, rec.count, want[rec.member])
		}
	}
}

// TestRecordStructureNeedsTelemetry_CandidateOfferCounts is CHAOS-4355's own
// red-first proof (codex xhigh round-1 finding): recordStructureNeedsTelemetry's
// offer-count loop covered KindOptions/AnchorOptions/HandleOptions only --
// CandidateOptions (CHAOS-4012) contributed no cf_structure_offer_count
// signal at all, silently, even though it is disclosed via Missing exactly
// like the other three members.
func TestRecordStructureNeedsTelemetry_CandidateOfferCounts(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	needs := &StructureNeeds{
		Missing: []StructureNeedKind{"subject_candidate"},
		CandidateOptions: []CandidateOption{
			{OptionID: "opt1", OfferSource: "engine"},
			{OptionID: "opt2", OfferSource: "engine"},
		},
	}

	recordStructureNeedsTelemetry(context.Background(), telemetry, reusePrincipal(), needs)

	if len(telemetry.structureOfferCounts) != 1 {
		t.Fatalf("len(structureOfferCounts) = %d, want 1 (subject_candidate/engine)", len(telemetry.structureOfferCounts))
	}
	rec := telemetry.structureOfferCounts[0]
	if rec.member != contractsv1.ContextFabricStructureNeedSubjectCandidate || rec.source != "engine" || rec.count != 2 {
		t.Errorf("record = %+v, want member=subject_candidate source=engine count=2", rec)
	}
}

// TestRecordStructureNeedsTelemetry_NilNeedsRecordsNothing mirrors
// composeStructureNeeds' own nil-means-nothing-in-play convention.
func TestRecordStructureNeedsTelemetry_NilNeedsRecordsNothing(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	recordStructureNeedsTelemetry(context.Background(), telemetry, reusePrincipal(), nil)
	if len(telemetry.structureNeedsDisclosed) != 0 || len(telemetry.structureOfferCounts) != 0 {
		t.Error("nil StructureNeeds recorded a non-empty telemetry call")
	}
}

// TestCHAOS3972_ExplicitKind_MCPSurface_EntersInferredDefault is the
// DP12(b) surface-split pin for CHAOS-3972 P3's own explicit fields: an
// MCP-surface request.ExpectedKinds value NEVER mints question_stated by
// itself -- it enters at inferred_default/explicit_unattributed, and does
// NOT bypass tryReuse (only Confirmed does, per DP11).
func TestCHAOS3972_ExplicitKind_MCPSurface_EntersInferredDefault(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectPullRequest}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Fatalf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Confirmed) != 0 {
		t.Errorf("canon.Confirmed = %+v, want empty -- an explicit field is never receipt-confirmed", canon.Confirmed)
	}
	if len(canon.Explicit) != 1 {
		t.Fatalf("canon.Explicit = %+v, want exactly 1 entry", canon.Explicit)
	}
	entry := canon.Explicit[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedExpectedKind || entry.AppliedValue != string(SubjectPullRequest) {
		t.Errorf("canon.Explicit[0] = %+v, want member=expected_kind applied_value=pull_request", entry)
	}
	if entry.Source != contractsv1.ContextFabricStructureSourceExplicitUnattributed {
		t.Errorf("entry.Source = %q, want explicit_unattributed (MCP surface)", entry.Source)
	}
	if entry.Provenance != contractsv1.ContextFabricStructureInferredDefault {
		t.Errorf("entry.Provenance = %q, want inferred_default -- DP12(b): MCP never mints question_stated from a bare explicit field", entry.Provenance)
	}
}

// TestCHAOS3972_ExplicitKind_NonMCPSurface_EntersQuestionStated is the
// other half of the surface split: a panel/web_assertion caller's explicit
// field keeps 3900 v5.2's ordinary question_stated rule, untouched by
// DP12(b).
func TestCHAOS3972_ExplicitKind_NonMCPSurface_EntersQuestionStated(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	request := validInvestigationRequest() // Consumer.Surface = "workbench" (model_test.go)
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectPullRequest}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if len(canon.Explicit) != 1 {
		t.Fatalf("canon.Explicit = %+v, want exactly 1 entry", canon.Explicit)
	}
	entry := canon.Explicit[0]
	if entry.Source != contractsv1.ContextFabricStructureSourceExplicit {
		t.Errorf("entry.Source = %q, want explicit (non-MCP surface)", entry.Source)
	}
	if entry.Provenance != contractsv1.ContextFabricStructureQuestionStated {
		t.Errorf("entry.Provenance = %q, want question_stated -- 3900 v5.2's ordinary rule, untouched off MCP", entry.Provenance)
	}
}

// TestCHAOS3972_ExplicitKind_MultiValue_NoSingleEcho pins the documented
// scope decision (resolveExplicitStructure's own doc comment): a
// multi-valued explicit field has no single applied value to echo, so it
// produces NO ConfirmedStructureEntry -- it still drives census-narrowing/
// offer-shaping (ResolveSubjects/kindOfferMaterial), just not this echo.
// Never a veto, never an error.
func TestCHAOS3972_ExplicitKind_MultiValue_NoSingleEcho(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectPullRequest, SubjectWorkItem}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Fatalf("canon.Veto = %q, want structureVetoNone", canon.Veto)
	}
	if len(canon.Explicit) != 0 {
		t.Errorf("canon.Explicit = %+v, want empty -- a multi-valued explicit field echoes nothing", canon.Explicit)
	}
}

// TestCHAOS3972_ExplicitKind_AgreesWithReceipt_ReceiptWinsNoDuplicateEcho
// pins design brief §2.1's explicit-vs-receipt AGREEMENT case: when a
// kindr_ receipt and an explicit expected_kinds value name the SAME kind,
// the receipt's own Confirmed entry stands and no separate Explicit entry
// duplicates it.
func TestCHAOS3972_ExplicitKind_AgreesWithReceipt_ReceiptWinsNoDuplicateEcho(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0005"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{{
			ReceiptID: "kindr_confirm00001", OptionID: "opt_kind", Label: "a pull request",
			Kind: SubjectPullRequest, OfferSource: "engine",
		}},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm00001"}}
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectPullRequest}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Fatalf("canon.Veto = %q, want structureVetoNone (agreeing explicit + receipt)", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Errorf("canon.Confirmed = %+v, want exactly 1 (the receipt)", canon.Confirmed)
	}
	if len(canon.Explicit) != 0 {
		t.Errorf("canon.Explicit = %+v, want empty -- an agreeing explicit value is not separately echoed", canon.Explicit)
	}
}

// TestCHAOS3972_ExplicitKind_ConflictsWithReceipt_Vetoes pins the
// disagreement half of the same rule: an explicit expected_kinds value
// that EXCLUDES the receipt-confirmed kind is a conflict, atomically
// vetoing the whole batch -- mirrors the window's own explicit-vs-receipt
// conflict rule exactly.
func TestCHAOS3972_ExplicitKind_ConflictsWithReceipt_Vetoes(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0006"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"expected_kind"},
		KindOptions: []KindOption{{
			ReceiptID: "kindr_confirm00002", OptionID: "opt_kind", Label: "a pull request",
			Kind: SubjectPullRequest, OfferSource: "engine",
		}},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{Results: store})

	request := validInvestigationRequest()
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "kindr_confirm00002"}}
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectWorkItem}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationConflict {
		t.Fatalf("canon.Veto = %q, want structureVetoConfirmationConflict", canon.Veto)
	}
	if len(canon.Confirmed) != 0 || len(canon.Explicit) != 0 {
		t.Errorf("a vetoed batch must apply nothing: canon = %+v", canon)
	}
	// CHAOS-3963: the receipt-confirmed kind that the explicit field
	// disagreed with is exactly what triggered this conflict -- it must
	// still be echoed (re-dispositioned to vetoed_conflict), not silently
	// dropped the way a pre-CHAOS-3963 veto would.
	if len(canon.VetoedEntries) != 1 {
		t.Fatalf("len(canon.VetoedEntries) = %d, want 1 (the receipt-confirmed kind the explicit field conflicted with)", len(canon.VetoedEntries))
	}
	if entry := canon.VetoedEntries[0]; entry.Member != "expected_kind" || entry.AppliedValue != string(SubjectPullRequest) || entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
		t.Errorf("canon.VetoedEntries[0] = %+v, want member=expected_kind applied_value=%q disposition=vetoed_conflict", entry, SubjectPullRequest)
	}
}

// TestCHAOS3963_PartialExplicit_EchoedWhenALaterExplicitMemberConflicts pins
// codex xhigh review's round-1 MEDIUM finding: resolveExplicitStructure
// builds its `explicit` slice across TWO independent blocks (expected_kind
// then subject_handle); a conflict in the SECOND block used to `return
// nil`, silently discarding a FIRST-block explicit member it had already
// resolved cleanly -- the exact "member A was fine, member B is why the
// batch was rejected" gap CHAOS-3963 exists to close, on the explicit side
// this time. An explicit expected_kind (no matching receipt -> resolves as
// explicit) must survive a LATER explicit subject_handle's conflict with a
// receipt-confirmed handle, re-dispositioned to vetoed_conflict alongside
// the receipt-confirmed member that actually triggered the veto.
func TestCHAOS3963_PartialExplicit_EchoedWhenALaterExplicitMemberConflicts(t *testing.T) {
	t.Parallel()

	handlePrior, request := handleReceiptTestSetup() // confirms subject_handle = (pull_request, "532")
	store := &staticResultStore{results: map[string]InvestigationResult{handlePrior.ResultID: handlePrior}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(context.Context, string, contractsv1.ContextFabricSubjectKind, string, string) (bool, HandleVerificationReason) {
			return true, HandleVerificationValid
		},
	})
	// Explicit expected_kind: no PriorKindReceipts sent, so this resolves
	// as an EXPLICIT member (not receipt-confirmed) -- the first block in
	// resolveExplicitStructure, and must succeed cleanly.
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{SubjectPullRequest}
	// Explicit subject_handle DISAGREES with the receipt-confirmed handle
	// (work_item/999 vs the confirmed pull_request/532) -- the second
	// block, which must veto as a conflict.
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectWorkItem, Value: "999"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationConflict {
		t.Fatalf("canon.Veto = %q, want structureVetoConfirmationConflict", canon.Veto)
	}
	if len(canon.VetoedEntries) != 2 {
		t.Fatalf("len(canon.VetoedEntries) = %d, want 2 (the receipt-confirmed handle that triggered the conflict + the already-built explicit kind): %+v", len(canon.VetoedEntries), canon.VetoedEntries)
	}
	var sawHandle, sawKind bool
	for _, entry := range canon.VetoedEntries {
		if entry.Disposition != contractsv1.ContextFabricStructureDispositionVetoedConflict {
			t.Errorf("entry %+v: Disposition = %q, want vetoed_conflict", entry, entry.Disposition)
		}
		switch entry.Member {
		case "subject_handle":
			sawHandle = true
			if entry.AppliedValue != "532" || entry.Source != contractsv1.ContextFabricStructureSourceReceipt {
				t.Errorf("handle entry = %+v, want applied_value=532 source=receipt (the receipt-confirmed value)", entry)
			}
		case "expected_kind":
			sawKind = true
			if entry.AppliedValue != string(SubjectPullRequest) || entry.Source != contractsv1.ContextFabricStructureSourceExplicit {
				t.Errorf("kind entry = %+v, want applied_value=%q source=explicit (the already-built explicit member)", entry, SubjectPullRequest)
			}
		default:
			t.Errorf("unexpected entry member %q", entry.Member)
		}
		if err := entry.Validate(); err != nil {
			t.Errorf("entry %+v fails Validate(): %v", entry, err)
		}
	}
	if !sawHandle || !sawKind {
		t.Errorf("canon.VetoedEntries = %+v, want both subject_handle and expected_kind present", canon.VetoedEntries)
	}
}

// TestCHAOS3972_ExplicitHandle_SingleValueEchoed mirrors the kind tests
// above for subject_handle -- the SAME single-value echo rule.
func TestCHAOS3972_ExplicitHandle_SingleValueEchoed(t *testing.T) {
	t.Parallel()

	engine := mustReuseTestEngine(t, EngineDependencies{Results: &resultStoreStub{}})
	request := validInvestigationRequest()
	request.Consumer.Surface = "mcp"
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if len(canon.Explicit) != 1 {
		t.Fatalf("canon.Explicit = %+v, want exactly 1 entry", canon.Explicit)
	}
	entry := canon.Explicit[0]
	if entry.Member != contractsv1.ContextFabricStructureNeedSubjectHandle || entry.AppliedValue != "532" {
		t.Errorf("canon.Explicit[0] = %+v, want member=subject_handle applied_value=532", entry)
	}
}

// TestCHAOS3972_ExplicitHandle_SameValueDifferentKindConflicts is the
// codex xhigh review round-1 finding 3 regression pin: a stored
// (pull_request, "532") handr_ receipt must NOT be read as "agreeing"
// with an explicit (work_item, "532") request naming a DIFFERENT subject
// that merely shares the same numeric string -- value alone is not a safe
// agreement test, kind must match too.
func TestCHAOS3972_ExplicitHandle_SameValueDifferentKindConflicts(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0007"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_handle"},
		HandleOptions: []HandleOption{{
			ReceiptID: "handr_confirm00001", OptionID: "opt_handle", Label: "pull request #532",
			Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532", SourceColumn: "git_pull_requests.number",
			OfferSource: "engine",
		}},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			return true, HandleVerificationValid
		},
	})

	request := validInvestigationRequest()
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "handr_confirm00001"}}
	// Same VALUE ("532"), but a DIFFERENT kind -- must conflict, not agree.
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectWorkItem, PatternID: "some_other_pattern", Value: "532"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoConfirmationConflict {
		t.Fatalf("canon.Veto = %q, want structureVetoConfirmationConflict -- same value, different kind, must never be read as agreement", canon.Veto)
	}
	if len(canon.Confirmed) != 0 || len(canon.Explicit) != 0 {
		t.Errorf("a vetoed batch must apply nothing: canon = %+v", canon)
	}
}

// TestCHAOS3972_ExplicitHandle_SameKindAndValueAgrees is the positive twin:
// a receipt and an explicit handle naming the SAME (kind, value) pair
// genuinely agree, and the receipt's own confirmation stands with no
// duplicate echo.
func TestCHAOS3972_ExplicitHandle_SameKindAndValueAgrees(t *testing.T) {
	t.Parallel()

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_structure_0008"
	priorResult.StructureNeeds = &StructureNeeds{
		Missing: []StructureNeedKind{"subject_handle"},
		HandleOptions: []HandleOption{{
			ReceiptID: "handr_confirm00002", OptionID: "opt_handle", Label: "pull request #532",
			Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532", SourceColumn: "git_pull_requests.number",
			OfferSource: "engine",
		}},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results: store,
		HandleVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, patternID, value string) (bool, HandleVerificationReason) {
			return true, HandleVerificationValid
		},
	})

	request := validInvestigationRequest()
	request.PriorHandleReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "handr_confirm00002"}}
	request.SubjectHandles = []contractsv1.ContextFabricRequestedHandle{{Kind: SubjectPullRequest, PatternID: "pull_request_number", Value: "532"}}

	canon := engine.canonicalizeStructure(context.Background(), reusePrincipal(), request, ResolvedGraphBinding{})
	if canon.Veto != structureVetoNone {
		t.Fatalf("canon.Veto = %q, want structureVetoNone (agreeing kind AND value)", canon.Veto)
	}
	if len(canon.Confirmed) != 1 {
		t.Errorf("canon.Confirmed = %+v, want exactly 1 (the receipt)", canon.Confirmed)
	}
	if len(canon.Explicit) != 0 {
		t.Errorf("canon.Explicit = %+v, want empty -- an agreeing explicit value is not separately echoed", canon.Explicit)
	}
}
