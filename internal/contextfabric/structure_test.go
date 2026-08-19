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
