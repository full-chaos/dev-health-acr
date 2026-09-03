package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// confirmedKindEntry builds the ConfirmedStructure entry a turn that
// REDEEMED a kindr_ receipt persists -- the only shape resolveCarriedKind is
// allowed to carry forward.
func confirmedKindEntry(kind contractsv1.ContextFabricSubjectKind, priorResultID, receiptID string) contractsv1.ContextFabricConfirmedStructureEntry {
	return contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(kind),
		Source:        contractsv1.ContextFabricStructureSourceReceipt,
		PriorResultID: priorResultID,
		ReceiptID:     receiptID,
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
}

// TestResolveCarriedKind_CarriesAConfirmedKindNamedByACandidateReceipt is
// this lane's test 1. Turn A confirmed expected_kind=team by redeeming a
// kindr_ receipt. Turn B states no kind of its own and names turn A through
// a CANDIDATE receipt -- a different namespace entirely, which is the point:
// the chain link is the prior result id, not the receipt's own axis. Turn B
// must inherit team rather than be asked again.
func TestResolveCarriedKind_CarriesAConfirmedKindNamedByACandidateReceipt(t *testing.T) {
	t.Parallel()
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_a"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_turn_a": priorResult}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_a", ReceiptID: "candr_turn_a_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryHit {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q: turn A's receipt-confirmed kind must carry", got.Outcome, KindCarryHit)
	}
	if got.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("resolveCarriedKind().Kind = %q, want %q", got.Kind, contractsv1.ContextFabricSubjectTeam)
	}
	// The result that CARRIES the confirmation, not the older result whose
	// offer was redeemed to make it -- the same convention resolveCarriedWindow
	// uses for a directly-referenced hit (prior.ResultID).
	if got.SourceResultID != "result_turn_a" {
		t.Fatalf("resolveCarriedKind().SourceResultID = %q, want %q", got.SourceResultID, "result_turn_a")
	}
	if got.ChainDepth != 0 {
		t.Fatalf("resolveCarriedKind().ChainDepth = %d, want 0 (directly referenced)", got.ChainDepth)
	}
}

// TestComposeCarriedKindEntry_DisclosesTheCarryOnTheWire pins the
// disclosure half of test 1: a carry the caller cannot see is the silent
// drop reborn. Source=carried names the origin result and carries NO
// receipt id, which is the shape the v1 validator already admits for any
// member (see TestConfirmedStructureEntry_AdmitsACarriedExpectedKind).
func TestComposeCarriedKindEntry_DisclosesTheCarryOnTheWire(t *testing.T) {
	t.Parallel()
	entry := composeCarriedKindEntry(kindCarryResult{
		Kind: contractsv1.ContextFabricSubjectTeam, SourceResultID: "result_turn_a", Outcome: KindCarryHit,
	})
	if entry == nil {
		t.Fatal("composeCarriedKindEntry() = nil, want an entry disclosing the carry")
	}
	want := contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(contractsv1.ContextFabricSubjectTeam),
		Source:        contractsv1.ContextFabricStructureSourceCarried,
		PriorResultID: "result_turn_a",
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   contractsv1.ContextFabricStructureDispositionApplied,
	}
	if *entry != want {
		t.Fatalf("composeCarriedKindEntry() = %#v, want %#v", *entry, want)
	}
	if err := entry.Validate(); err != nil {
		t.Fatalf("composeCarriedKindEntry().Validate() = %v, want nil", err)
	}
	// A miss must compose nothing at all -- never an entry claiming a carry
	// that did not happen.
	if got := composeCarriedKindEntry(kindCarryResult{Outcome: KindCarryMissNoReference}); got != nil {
		t.Fatalf("composeCarriedKindEntry(miss) = %#v, want nil", got)
	}
}

// TestResolveCarriedKind_NewChainInheritsNothing is this lane's test 3 and
// the guard on the whole design. A fresh conversation names no prior result,
// so there is nothing to walk and nothing may be inherited -- in particular
// NOT via (org_id, question_hash), the one other key that could plausibly
// find turn A's confirmation. If a future implementation reaches for that
// shortcut, this test is what fails.
func TestResolveCarriedKind_NewChainInheritsNothing(t *testing.T) {
	t.Parallel()
	// Chain A: a confirmed team, sitting in the same org's store, reachable
	// by question_hash but NOT by any chain link this request names.
	chainA := validInvestigationResult()
	chainA.ResultID = "result_chain_a"
	chainA.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_chain_a_prior", "kindr_chain_a_01"),
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_chain_a": chainA}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest() // names no prior result in ANY of the six receipt fields

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryMissNoReference {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q: an unlinked turn has nothing to walk", got.Outcome, KindCarryMissNoReference)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty: a new chain must never inherit another chain's axis", got.Kind)
	}
}

// TestResolveCarriedKind_SiblingChainInTheSameOrgIsNotAChainLink is test 3's
// second half. This request DOES name a prior result -- so the walk runs --
// but that result belongs to a different chain and carries no confirmation
// of its own, and it does not breadcrumb back to chain A. The confirmed team
// sitting one lookup away in the same org must stay invisible.
func TestResolveCarriedKind_SiblingChainInTheSameOrgIsNotAChainLink(t *testing.T) {
	t.Parallel()
	chainA := validInvestigationResult()
	chainA.ResultID = "result_chain_a"
	chainA.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_chain_a_prior", "kindr_chain_a_01"),
	}
	chainB := validInvestigationResult()
	chainB.ResultID = "result_chain_b"
	chainB.ConfirmedStructure = nil // no confirmation, no breadcrumb to chain A
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_chain_a": chainA, "result_chain_b": chainB,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_chain_b", ReceiptID: "candr_chain_b_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryMissNoConfirmedKind {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q", got.Outcome, KindCarryMissNoConfirmedKind)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty: chain B must not inherit chain A's kind", got.Kind)
	}
}

// TestResolveCarriedKind_RefusesACarrierFromADifferentGraphEpoch is this
// lane's test 4. A rebuild between turns can legitimately change what a kind
// even denotes, so the CHAOS-3898 §2.2 ingress taint gate refuses a carrier
// from another epoch outright rather than trusting it partially -- the same
// fail-closed check resolveCarriedWindow and resolvePriorSubjectHints apply.
func TestResolveCarriedKind_RefusesACarrierFromADifferentGraphEpoch(t *testing.T) {
	t.Parallel()
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_a"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	staleEpoch := int64(7)
	store := &staticResultStore{
		results:    map[string]InvestigationResult{"result_turn_a": priorResult},
		graphEpoch: &staleEpoch,
	}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_a", ReceiptID: "candr_turn_a_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryMissStaleGraphEpoch {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q", got.Outcome, KindCarryMissStaleGraphEpoch)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty: a carrier from another epoch is never partially trusted", got.Kind)
	}
}

// TestResolveCarriedKind_FailsClosedOnTwoDisagreeingCarriers is this lane's
// test 5. The six receipt fields validate independently of one another, so
// one request can legitimately name two prior results at the same depth. If
// they confirmed DIFFERENT kinds, answering under whichever loaded first
// would silently pick one of two real, disagreeing confirmations.
func TestResolveCarriedKind_FailsClosedOnTwoDisagreeingCarriers(t *testing.T) {
	t.Parallel()
	teamResult := validInvestigationResult()
	teamResult.ResultID = "result_turn_team"
	teamResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	projectResult := validInvestigationResult()
	projectResult.ResultID = "result_turn_project"
	projectResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectProject, "result_turn_zero", "kindr_turn_zero_02"),
	}
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_turn_team": teamResult, "result_turn_project": projectResult,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_team", ReceiptID: "candr_turn_team_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_project", ReceiptID: "kindr_turn_project_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryMissConflictingKinds {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q", got.Outcome, KindCarryMissConflictingKinds)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty: a genuine conflict fails closed", got.Kind)
	}
}

// TestResolveCarriedKind_IgnoresANonReceiptSourcedKind pins the
// kind-insensitivity boundary (CHAOS-3972 §2.0): only a RECEIPT-confirmed
// kind is caller authority and may narrow the census scope. An explicit or
// explicit_unattributed entry on a prior turn is an inferred-tier value, and
// carrying it forward would launder it into caller authority a turn later --
// exactly what ConfirmedExpectedKind's type-level tripwire exists to
// prevent.
func TestResolveCarriedKind_IgnoresANonReceiptSourcedKind(t *testing.T) {
	t.Parallel()
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_a"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member:       contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		Source:       contractsv1.ContextFabricStructureSourceExplicitUnattributed,
		Provenance:   contractsv1.ContextFabricStructureInferredDefault,
		Disposition:  contractsv1.ContextFabricStructureDispositionApplied,
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_turn_a": priorResult}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_a", ReceiptID: "candr_turn_a_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryMissNoConfirmedKind {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q: an explicit-tier kind is not caller authority", got.Outcome, KindCarryMissNoConfirmedKind)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty", got.Kind)
	}
}
