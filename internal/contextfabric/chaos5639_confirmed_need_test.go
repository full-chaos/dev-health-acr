package contextfabric

import (
	"context"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// parentAndChildForLedgerTest builds a parent result (turn two, which
// confirmed expected_kind=team by receipt and saved a ledger recording it)
// and a child request (turn three) that continues it with NO receipt of its
// own -- the CHAOS-5639 scenario: "confirmed need persists across turns
// without re-echo".
//
// The child's conversation carries exactly the parent's own question and an
// answer, appended after the parent's OWN (empty) conversation, so
// SemanticRequestIdentityOf(child, parent.Question) drops that one exchange
// and recomputes to the SAME digest the parent stored (SemanticRequestIdentityOf
// dropped nothing, since the parent's own turn was not a window-only
// continuation -- see this file's own header comment).
func parentAndChildForLedgerTest(t *testing.T) (parentResult InvestigationResult, parentState *PersistedSemanticState, childRequest InvestigationRequest) {
	t.Helper()
	parentRequest := validInvestigationRequest()
	parentRequest.RequestID = "request_turn_two"
	parentIdentity := SemanticRequestIdentityOf(parentRequest, "")

	parentResult = validInvestigationResult()
	parentResult.ResultID = "result_turn_two"
	parentResult.Question = parentRequest.Question

	parentState = &PersistedSemanticState{
		FormatVersion:                SemanticStateFormatVersion,
		Family:                       QuestionFamilyGroupedCohortStatus,
		FamilySource:                 QuestionFamilySourceModel,
		FamilyTableVersion:           QuestionFamilyTableVersion,
		FrameVersion:                 QuestionFrameVersion,
		Roles:                        []SemanticRoleSlot{},
		Requirements:                 []SemanticRequirement{},
		RequirementDerivationVersion: RequirementDerivationVersion,
		RequestIdentity:              parentIdentity,
		Validation:                   SemanticStateValidation{EmittedShape: ShapeOpen, GateOutcome: FrameGateNotEvaluated},
		ConfirmedNeeds: []ConfirmedNeedEntry{
			{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
		},
	}

	childRequest = validInvestigationRequest()
	childRequest.RequestID = "request_turn_three"
	childRequest.ParentResultID = parentResult.ResultID
	childRequest.Conversation = []contractsv1.ContextFabricConversationTurn{
		{Role: contractsv1.ContextFabricConversationUser, Content: parentRequest.Question},
		{Role: contractsv1.ContextFabricConversationAssistant, Content: "Ask Dev is healthy."},
	}
	return parentResult, parentState, childRequest
}

func ledgerTestStore(parentResult InvestigationResult, parentState *PersistedSemanticState) *staticResultStore {
	return &staticResultStore{
		results: map[string]InvestigationResult{parentResult.ResultID: parentResult},
		states:  map[string]*PersistedSemanticState{parentResult.ResultID: parentState},
	}
}

// TestResolveConfirmedNeedLedger_HitsWhenIdentityMatches pins the core claim:
// a need confirmed on turn two is available to turn three under the SAME
// question, with no receipt redeemed on turn three at all.
func TestResolveConfirmedNeedLedger_HitsWhenIdentityMatches(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest)
	if got.Outcome != ConfirmedNeedLedgerHit {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerHit)
	}
	want := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)}}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("Entries = %#v, want %#v", got.Entries, want)
	}
}

// TestResolveConfirmedNeedLedger_DropsOnIdentityChange pins the other half:
// a turn that changes something the digest covers earns a fresh need, not a
// partially-remembered one.
func TestResolveConfirmedNeedLedger_DropsOnIdentityChange(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	childRequest.RequestedScope.RepositorySlugs = []string{"full-chaos/dev-health-acr"}
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest)
	if got.Outcome != ConfirmedNeedLedgerDroppedIdentityChanged {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerDroppedIdentityChanged)
	}
	if got.Entries != nil {
		t.Fatalf("Entries = %#v, want nil on a dropped ledger", got.Entries)
	}
}

func TestResolveConfirmedNeedLedger_MissNoReference(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	childRequest.ParentResultID = ""
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest)
	if got.Outcome != ConfirmedNeedLedgerMissNoReference {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerMissNoReference)
	}
}

func TestResolveConfirmedNeedLedger_MissUnloadable(t *testing.T) {
	t.Parallel()
	engine := buildCarryTestEngine(t, &staticResultStore{results: map[string]InvestigationResult{}})
	request := validInvestigationRequest()
	request.ParentResultID = "result_does_not_exist"

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), request)
	if got.Outcome != ConfirmedNeedLedgerMissUnloadable {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerMissUnloadable)
	}
}

func TestResolveConfirmedNeedLedger_MissEmpty(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentState.ConfirmedNeeds = []ConfirmedNeedEntry{}
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest)
	if got.Outcome != ConfirmedNeedLedgerMissEmpty {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerMissEmpty)
	}
}

// TestResolveConfirmedNeedLedger_DroppedIncomparable pins the fail-closed
// side: a carrier that never computed a comparable identity (a pre-M2 row,
// in production; a version-mismatched one here) never admits, whatever its
// ledger holds.
func TestResolveConfirmedNeedLedger_DroppedIncomparable(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentState.RequestIdentity = SemanticRequestIdentity{}
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest)
	if got.Outcome != ConfirmedNeedLedgerDroppedIdentityIncomparable {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerDroppedIdentityIncomparable)
	}
}

// TestEffectiveConfirmedKind_Precedence pins the three-way precedence
// effectiveConfirmedKind's own doc comment states: this turn's own receipt,
// then the CHAOS-5639 ledger, then the legacy carry walk.
func TestEffectiveConfirmedKind_Precedence(t *testing.T) {
	t.Parallel()
	own := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject)}}
	remembered := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)}}
	legacyCarry := kindCarryResult{Outcome: KindCarryHit, Kind: contractsv1.ContextFabricSubjectRepository}

	if got := effectiveConfirmedKind(own, remembered, legacyCarry); got == nil || got.Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("own wins: effectiveConfirmedKind() = %#v, want project", got)
	}
	if got := effectiveConfirmedKind(nil, remembered, legacyCarry); got == nil || got.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("remembered wins over legacy carry: effectiveConfirmedKind() = %#v, want team", got)
	}
	if got := effectiveConfirmedKind(nil, nil, legacyCarry); got == nil || got.Kind != contractsv1.ContextFabricSubjectRepository {
		t.Fatalf("legacy carry still applies with no ledger entry: effectiveConfirmedKind() = %#v, want repository", got)
	}
	if got := effectiveConfirmedKind(nil, nil, kindCarryResult{}); got != nil {
		t.Fatalf("nothing confirmed anywhere: effectiveConfirmedKind() = %#v, want nil", got)
	}
}

// TestConfirmedAnchorSelection_Precedence is TestEffectiveConfirmedKind_Precedence's
// own sibling for subject_anchor (no legacy carry for this member).
func TestConfirmedAnchorSelection_Precedence(t *testing.T) {
	t.Parallel()
	own := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectProject, AppliedValue: "project_own"}}
	remembered := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectTeam, AppliedValue: "team_remembered"}}

	if got := confirmedAnchorSelection(own, remembered); got == nil || got.CanonicalID != "project_own" {
		t.Fatalf("own wins: confirmedAnchorSelection() = %#v, want project_own", got)
	}
	if got := confirmedAnchorSelection(nil, remembered); got == nil || got.CanonicalID != "team_remembered" {
		t.Fatalf("remembered applies with no own receipt: confirmedAnchorSelection() = %#v, want team_remembered", got)
	}
	if got := confirmedAnchorSelection(nil, nil); got != nil {
		t.Fatalf("nothing confirmed anywhere: confirmedAnchorSelection() = %#v, want nil", got)
	}
}

// TestMergeConfirmedNeedsLedger_ThisTurnWinsOverRemembered pins the outgoing
// ledger's own precedence: a real receipt this turn replaces an inherited
// entry for the same member; every other inherited member carries forward.
func TestMergeConfirmedNeedsLedger_ThisTurnWinsOverRemembered(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: contractsv1.ContextFabricSubjectPullRequest, AppliedValue: "42"},
	}
	confirmedThisTurn := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject)},
	}
	got := mergeConfirmedNeedsLedger(remembered, confirmedThisTurn)
	want := []ConfirmedNeedEntry{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: contractsv1.ContextFabricSubjectPullRequest, AppliedValue: "42"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeConfirmedNeedsLedger() = %#v, want %#v", got, want)
	}
}

// TestMergeConfirmedNeedsLedger_OrderIsTheVocabularysOwn pins determinism:
// EncodeSemanticState's canonical encoding requires two calls that resolve
// to the same set to produce the same array order, so the merge must never
// carry insertion (map) order through.
func TestMergeConfirmedNeedsLedger_OrderIsTheVocabularysOwn(t *testing.T) {
	t.Parallel()
	reverseOrder := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedValue: "c"},
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: "k"},
	}
	got := mergeConfirmedNeedsLedger(reverseOrder, nil)
	if len(got) != 2 || got[0].Member != contractsv1.ContextFabricStructureNeedExpectedKind || got[1].Member != contractsv1.ContextFabricStructureNeedSubjectCandidate {
		t.Fatalf("mergeConfirmedNeedsLedger() = %#v, want expected_kind before subject_candidate (the vocabulary's own order)", got)
	}
}

// TestReuseBypassReason_ARememberedNeedNeverReportsAsConfirmedStructure pins
// DP11's own boundary precisely: reuseBypassReason (answer_reuse.go) ALREADY
// bypasses reuse for any request naming a parent result at all
// (AnswerReuseBypassPriorResultReference, unconditional on Confirmed) -- so a
// turn the CHAOS-5639 ledger will consult is never reuse-eligible regardless
// of this feature. What this feature must never do is ALSO report that
// bypass as AnswerReuseBypassConfirmedStructure, which specifically means
// "this request's OWN receipts resolved a structure member this turn" (DP11)
// -- a claim a remembered-only turn does not make. structureCanon.Confirmed
// empty (no receipt this turn) must read PriorResultReference, never
// ConfirmedStructure, however the ledger elsewhere applies it.
func TestReuseBypassReason_ARememberedNeedNeverReportsAsConfirmedStructure(t *testing.T) {
	t.Parallel()
	request := validInvestigationRequest()
	request.ParentResultID = "result_turn_two"  // names the parent the ledger would consult
	canon := requestStructureCanonicalization{} // no receipt of its own this turn

	got := reuseBypassReason(request, canon)
	if got != AnswerReuseBypassPriorResultReference {
		t.Fatalf("reuseBypassReason() = %q, want %q: a remembered-only turn must never report the confirmed-structure bypass", got, AnswerReuseBypassPriorResultReference)
	}
}
