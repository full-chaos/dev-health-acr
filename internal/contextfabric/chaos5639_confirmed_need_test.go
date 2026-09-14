package contextfabric

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
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

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
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

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerMissNoReference {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerMissNoReference)
	}
}

func TestResolveConfirmedNeedLedger_MissUnloadable(t *testing.T) {
	t.Parallel()
	engine := buildCarryTestEngine(t, &staticResultStore{results: map[string]InvestigationResult{}})
	request := validInvestigationRequest()
	request.ParentResultID = "result_does_not_exist"

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), request, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerMissUnloadable {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerMissUnloadable)
	}
}

// TestResolveConfirmedNeedLedger_MissUnloadableWhenResultsUnconfigured pins
// the DISTINCT reason an unset Results dependency reports: not
// miss_no_reference (a false basis -- the request DID name a parent, the
// engine simply cannot read anything), but the same miss_unloadable a read
// failure reports.
func TestResolveConfirmedNeedLedger_MissUnloadableWhenResultsUnconfigured(t *testing.T) {
	t.Parallel()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("Interpret must not be called by a direct resolveConfirmedNeedLedger test")
			return InterpretedQuestion{}, nil
		}),
		Graph: neverProjectedGraphReader{t: t},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called by a direct resolveConfirmedNeedLedger test")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called by a direct resolveConfirmedNeedLedger test")
			return InvestigationResult{}, nil
		}),
		// Results deliberately unset.
	}, EngineOptions{ServiceVersion: "chaos-5639-unit-test", Now: func() time.Time { return time.Unix(400, 0).UTC() }, NewResultID: func() string { return "result_chaos_5639_unit_test" }})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	request := validInvestigationRequest()
	request.ParentResultID = "result_turn_two"

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), request, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerMissUnloadable {
		t.Fatalf("Outcome = %q, want %q: a named parent with no Results dependency cannot be a \"no reference\" miss", got.Outcome, ConfirmedNeedLedgerMissUnloadable)
	}
}

// TestValidConfirmedNeedLedgerOutcome_Membership pins the closed vocabulary:
// every declared member reports valid, and nothing else does.
func TestValidConfirmedNeedLedgerOutcome_Membership(t *testing.T) {
	t.Parallel()
	for _, member := range confirmedNeedLedgerOutcomes() {
		if !ValidConfirmedNeedLedgerOutcome(member) {
			t.Errorf("ValidConfirmedNeedLedgerOutcome(%q) = false, want true", member)
		}
	}
	for _, bad := range []ConfirmedNeedLedgerOutcome{"", "not_a_member", "HIT"} {
		if ValidConfirmedNeedLedgerOutcome(bad) {
			t.Errorf("ValidConfirmedNeedLedgerOutcome(%q) = true, want false", bad)
		}
	}
}

// TestResolveConfirmedNeedLedger_DropsOnDifferentQuestion pins the
// same-question containment directly: the request-identity digest never
// covers request.Question, so a same-question check is required BESIDE it,
// not implied by it. Without this check, a turn naming a parent result but
// asking something else entirely would inherit that parent's confirmed need.
func TestResolveConfirmedNeedLedger_DropsOnDifferentQuestion(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	childRequest.Question = "Which repositories does the Platform team own?"
	// No conversation at all -- an ordinary shape for a turn that only names
	// a parent, and the one that reduces the digest to scope and options
	// alone with nothing else to disambiguate the question by.
	childRequest.Conversation = nil
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerDroppedQuestionChanged {
		t.Fatalf("Outcome = %q, want %q: an unrelated question naming the same parent must never inherit its ledger", got.Outcome, ConfirmedNeedLedgerDroppedQuestionChanged)
	}
	if got.Entries != nil {
		t.Fatalf("Entries = %#v, want nil on a dropped ledger", got.Entries)
	}
}

// TestResolveConfirmedNeedLedger_DropsOnIndeterminateQuestion pins the third
// state carryOriginSameQuestionVerdict's own sibling check distinguishes:
// a question consisting only of terminal punctuation canonicalizes to the
// empty string, so equal hashes there prove nothing -- neither same nor
// different, dropped either way.
func TestResolveConfirmedNeedLedger_DropsOnIndeterminateQuestion(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentResult.Question = "?"
	childRequest.Question = "?"
	childRequest.Conversation = nil
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerDroppedQuestionIndeterminate {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerDroppedQuestionIndeterminate)
	}
}

func TestResolveConfirmedNeedLedger_MissEmpty(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentState.ConfirmedNeeds = []ConfirmedNeedEntry{}
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
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

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerDroppedIdentityIncomparable {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerDroppedIdentityIncomparable)
	}
}

// TestAppliedNeedLedgerEntries_ExcludesWhatThisTurnAlreadyConfirmed pins the
// single authority every consumer reads: a real receipt this turn for
// expected_kind always wins and the ledger's own entry for that member never
// applies, regardless of value.
func TestAppliedNeedLedgerEntries_ExcludesWhatThisTurnAlreadyConfirmed(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
	}
	confirmedThisTurn := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectProject)},
	}
	got := appliedNeedLedgerEntries(remembered, confirmedThisTurn, validInvestigationRequest())
	if _, ok := got[contractsv1.ContextFabricStructureNeedExpectedKind]; ok {
		t.Fatalf("applied = %#v, expected_kind must be excluded: this turn's own receipt already confirmed it", got)
	}
}

// TestAppliedNeedLedgerEntries_AppliesSubjectAnchor pins the other half of
// TestAppliedNeedLedgerEntries_ExcludesWhatThisTurnAlreadyConfirmed: a
// remembered subject_anchor DOES apply once it reaches this map -- by the
// time it does, resolveConfirmedNeedLedger has already reverified it through
// reverifyAnchorClaim (this file's own header comment), so
// appliedNeedLedgerEntries trusts it exactly as it trusts expected_kind,
// without re-deriving that check.
func TestAppliedNeedLedgerEntries_AppliesSubjectAnchor(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectTeam, AppliedValue: "team_remembered"},
	}
	got := appliedNeedLedgerEntries(remembered, nil, validInvestigationRequest())
	entry, ok := got[contractsv1.ContextFabricStructureNeedSubjectAnchor]
	if !ok || entry.AppliedValue != "team_remembered" {
		t.Fatalf("applied = %#v, want subject_anchor=team_remembered", got)
	}
}

// TestResolveConfirmedNeedLedger_DropsUnreverifiableAnchorKeepsOtherMembers
// pins the security boundary: a remembered subject_anchor is reverified
// through the SAME choke point (reverifyAnchorClaim) a fresh ancr_ receipt
// redemption already goes through, on the carrier's OWN
// schema_version -- an unwired verifier (the fail-closed default,
// AnchorVerifier's own doc comment) drops JUST that member, never the whole
// ledger: expected_kind survives untouched beside it.
func TestResolveConfirmedNeedLedger_DropsUnreverifiableAnchorKeepsOtherMembers(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentState.ConfirmedNeeds = append(parentState.ConfirmedNeeds, ConfirmedNeedEntry{
		Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectTeam,
		AppliedValue: "team_stale", MatchedTermHash: "hash_abc123",
	})
	engine := buildCarryTestEngine(t, ledgerTestStore(parentResult, parentState))
	// buildCarryTestEngine wires no anchorVerifier/anchorMembershipVerifier --
	// the fail-closed default every reverify dependency in this package uses.

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerHit {
		t.Fatalf("Outcome = %q, want %q: an unreverifiable anchor drops only itself, not the whole ledger", got.Outcome, ConfirmedNeedLedgerHit)
	}
	for _, entry := range got.Entries {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			t.Fatalf("Entries = %#v, want subject_anchor dropped: no verifier is wired to reverify it", got.Entries)
		}
	}
	want := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)}}
	if !reflect.DeepEqual(got.Entries, want) {
		t.Fatalf("Entries = %#v, want %#v", got.Entries, want)
	}
}

// TestResolveConfirmedNeedLedger_AdmitsAnchorWhenTheVerifierConfirmsIt is the
// positive control: a wired AnchorVerifier that reports the claim still
// valid lets the remembered subject_anchor through, carrying the SAME
// matched_term_hash the resolver replayed to it.
func TestResolveConfirmedNeedLedger_AdmitsAnchorWhenTheVerifierConfirmsIt(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	parentResult.SchemaVersion = InvestigationResultSchemaV1
	parentState.ConfirmedNeeds = append(parentState.ConfirmedNeeds, ConfirmedNeedEntry{
		Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectTeam,
		AppliedValue: "team_confirmed", MatchedTermHash: "hash_abc123",
	})
	store := ledgerTestStore(parentResult, parentState)
	var gotOrgID, gotCanonicalID, gotMatchedTermHash string
	var gotKind contractsv1.ContextFabricSubjectKind
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			t.Fatal("Interpret must not be called by a direct resolveConfirmedNeedLedger test")
			return InterpretedQuestion{}, nil
		}),
		Graph: neverProjectedGraphReader{t: t},
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called by a direct resolveConfirmedNeedLedger test")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called by a direct resolveConfirmedNeedLedger test")
			return InvestigationResult{}, nil
		}),
		Results: store,
		AnchorVerifier: func(ctx context.Context, orgID string, kind contractsv1.ContextFabricSubjectKind, canonicalID, matchedTermHash string) (bool, AnchorVerificationReason) {
			gotOrgID, gotKind, gotCanonicalID, gotMatchedTermHash = orgID, kind, canonicalID, matchedTermHash
			return true, AnchorVerificationValid
		},
	}, EngineOptions{ServiceVersion: "chaos-5639-unit-test", Now: func() time.Time { return time.Unix(400, 0).UTC() }, NewResultID: func() string { return "result_chaos_5639_unit_test" }})
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
	if got.Outcome != ConfirmedNeedLedgerHit {
		t.Fatalf("Outcome = %q, want %q", got.Outcome, ConfirmedNeedLedgerHit)
	}
	found := false
	for _, entry := range got.Entries {
		if entry.Member == contractsv1.ContextFabricStructureNeedSubjectAnchor {
			found = true
			if entry.AppliedValue != "team_confirmed" || entry.AppliedKind != contractsv1.ContextFabricSubjectTeam {
				t.Fatalf("anchor entry = %#v, want the confirmed value/kind through unchanged", entry)
			}
		}
	}
	if !found {
		t.Fatalf("Entries = %#v, want subject_anchor admitted: the verifier confirmed the claim", got.Entries)
	}
	if gotOrgID != acceptancePrincipal().OrgID || gotKind != contractsv1.ContextFabricSubjectTeam || gotCanonicalID != "team_confirmed" || gotMatchedTermHash != "hash_abc123" {
		t.Fatalf("AnchorVerifier called with (org=%q kind=%q canonical_id=%q matched_term_hash=%q), want the ledger's own persisted values replayed exactly", gotOrgID, gotKind, gotCanonicalID, gotMatchedTermHash)
	}
}

// TestAppliedNeedLedgerEntries_ExcludesEmptyValuesAndUnappliableMembers pins
// two invariants at once: an entry with no value never applies (a resolution
// parameter must never receive an empty-but-non-nil selection), and the
// window member is never in this map -- its own consumer (decideLedgerWindow)
// decides it after the window carry has run.
func TestAppliedNeedLedgerEntries_ExcludesEmptyValuesAndUnappliableMembers(t *testing.T) {
	t.Parallel()
	remembered := []confirmedStructureMember{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: ""},
		{Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: "trailing_30d"},
	}
	got := appliedNeedLedgerEntries(remembered, nil, validInvestigationRequest())
	if len(got) != 0 {
		t.Fatalf("applied = %#v, want empty: an empty-valued kind and the window member (decided by its own consumer) must both be excluded", got)
	}
}

// TestResolveCarriedKind_ARememberedEntryWinsOverAnExistingLegacyCarryHit pins
// the ledger's own precedence over the older, multi-hop carry walk: it is the
// more precise, identity-verified mechanism M2 built to replace it for a
// directly-referenced parent, and it is checked FIRST, inside the SAME gated
// producer TestCarryGateClosure_EveryHitIsConstructedInsideAGatedProducer
// requires -- see resolveCarriedKind's own doc comment for why the ledger
// consult has to live there rather than in a separate helper.
func TestResolveCarriedKind_ARememberedEntryWinsOverAnExistingLegacyCarryHit(t *testing.T) {
	t.Parallel()
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedExpectedKind: {Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
	}
	engine := buildCarryTestEngine(t, &staticResultStore{results: map[string]InvestigationResult{}})
	request := validInvestigationRequest() // no receipt/parent seeds at all -- the legacy walk would miss on its own
	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{}, applied, "result_parent")
	if got.Outcome != KindCarryHit || got.Kind != contractsv1.ContextFabricSubjectTeam || got.SourceResultID != "result_parent" {
		t.Fatalf("resolveCarriedKind() = %#v, want a hit for team sourced from result_parent", got)
	}
	if got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{}, nil, "result_parent"); got.Outcome != KindCarryMissNoReference {
		t.Fatalf("resolveCarriedKind() with no applied entry and no legacy reference = %#v, want %q", got, KindCarryMissNoReference)
	}
}

// TestKindCarryGates_ARememberedKindNeverArguesWithTheCallersOwnStatement
// pins the two gates a remembered kind must flow through in engine.go: it
// never overrides what the caller stated THIS turn
// (statedExpectedKindThisTurn), and a subject-axis receipt naming a
// different kind still stands it down (applyCarryDrop) exactly as it would a
// legacy-walk value.
func TestKindCarryGates_ARememberedKindNeverArguesWithTheCallersOwnStatement(t *testing.T) {
	t.Parallel()
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedExpectedKind: {Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
	}
	t.Run("caller stated an explicit kind this turn", func(t *testing.T) {
		t.Parallel()
		request := validInvestigationRequest()
		request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectRepository}
		canon := requestStructureCanonicalization{}
		if !statedExpectedKindThisTurn(request, canon) {
			t.Fatal("fixture defect: statedExpectedKindThisTurn must be true for an explicit kind")
		}
		// engine.go's own gate: resolveCarriedKind (and so the ledger check
		// inside it) is never even called when this is true, so
		// effectiveConfirmedKind sees only confirmed (empty here) and a
		// zero-value kindCarry -- applied is never consulted regardless of
		// what it holds.
		if got := effectiveConfirmedKind(canon.Confirmed, kindCarryResult{}); got != nil {
			t.Fatalf("effectiveConfirmedKind() = %#v, want nil: an explicit kind this turn must never be overridden by a remembered one", got)
		}
	})
	t.Run("a subject-axis receipt this turn redeemed a different kind", func(t *testing.T) {
		t.Parallel()
		confirmedThisTurn := []confirmedStructureMember{
			{Member: contractsv1.ContextFabricStructureNeedSubjectCandidate, AppliedKind: contractsv1.ContextFabricSubjectRepository},
		}
		engine := buildCarryTestEngine(t, &staticResultStore{results: map[string]InvestigationResult{}})
		remembered := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), validInvestigationRequest(), nil, ResolvedGraphBinding{}, applied, "result_parent")
		dropped := applyCarryDrop(confirmedThisTurn, remembered)
		if dropped.Outcome != KindCarryDroppedRedeemedKindDiffers {
			t.Fatalf("applyCarryDrop(remembered) outcome = %q, want %q: a redeemed candidate naming repository must stand the remembered team down", dropped.Outcome, KindCarryDroppedRedeemedKindDiffers)
		}
		if got := effectiveConfirmedKind(nil, dropped); got != nil {
			t.Fatalf("effectiveConfirmedKind(dropped) = %#v, want nil", got)
		}
	})
}

// TestConfirmedAnchorSelection_AppliedMapPrecedence is
// TestKindCarryGates_ARememberedKindNeverArguesWithTheCallersOwnStatement's
// own sibling for subject_anchor (no legacy carry, no drop rule for this
// member -- own receipt then the applied map is the whole precedence).
func TestConfirmedAnchorSelection_AppliedMapPrecedence(t *testing.T) {
	t.Parallel()
	own := []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectProject, AppliedValue: "project_own"}}
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectTeam, AppliedValue: "team_remembered"},
	}

	if got := confirmedAnchorSelection(own, applied); got == nil || got.CanonicalID != "project_own" {
		t.Fatalf("own wins: confirmedAnchorSelection() = %#v, want project_own", got)
	}
	if got := confirmedAnchorSelection(nil, applied); got == nil || got.CanonicalID != "team_remembered" {
		t.Fatalf("applied entry used with no own receipt: confirmedAnchorSelection() = %#v, want team_remembered", got)
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

// TestComposeCarriedNeedEntry_DisclosesAnAppliedAnchorNeverASilentOne pins the
// anchor-axis disclosure directly: present in applied -> a valid, carried
// entry; absent -> nil, never a guessed one.
func TestComposeCarriedNeedEntry_DisclosesAnAppliedAnchorNeverASilentOne(t *testing.T) {
	t.Parallel()
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: "team_remembered"},
	}
	got := composeCarriedNeedEntry(contractsv1.ContextFabricStructureNeedSubjectAnchor, applied, "result_parent")
	want := &contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedValue: "team_remembered",
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_parent",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	if got == nil || *got != *want {
		t.Fatalf("composeCarriedNeedEntry() = %#v, want %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("composeCarriedNeedEntry().Validate() = %v, want nil", err)
	}
	if got := composeCarriedNeedEntry(contractsv1.ContextFabricStructureNeedSubjectAnchor, nil, "result_parent"); got != nil {
		t.Fatalf("composeCarriedNeedEntry(no applied entry) = %#v, want nil", got)
	}
}

// TestObservableAppliedNeedMembers pins the log-line rendering: "none" for
// zero applied members (never an empty, key-shaped string), a comma join
// otherwise.
func TestObservableAppliedNeedMembers(t *testing.T) {
	t.Parallel()
	if got := observableAppliedNeedMembers(nil); got != "none" {
		t.Fatalf("observableAppliedNeedMembers(nil) = %q, want %q", got, "none")
	}
	got := observableAppliedNeedMembers([]contractsv1.ContextFabricStructureNeedKind{
		contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectAnchor,
	})
	if want := "expected_kind,subject_anchor"; got != want {
		t.Fatalf("observableAppliedNeedMembers() = %q, want %q", got, want)
	}
}

// TestRecordConfirmedNeedLedger_ForwardsSourceAndAppliedKinds pins the
// telemetry line's own content end to end: outcome, source_result_id and
// both applied kind values reach the sink exactly as computed.
func TestRecordConfirmedNeedLedger_ForwardsSourceAndAppliedKinds(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Results:   &staticResultStore{results: map[string]InvestigationResult{}},
		Telemetry: telemetry,
	})
	applied := map[contractsv1.ContextFabricStructureNeedKind]confirmedStructureMember{
		contractsv1.ContextFabricStructureNeedExpectedKind:  {Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
		contractsv1.ContextFabricStructureNeedSubjectAnchor: {Member: contractsv1.ContextFabricStructureNeedSubjectAnchor, AppliedKind: contractsv1.ContextFabricSubjectProject, AppliedValue: "p"},
	}
	ledger := confirmedNeedLedgerResult{Outcome: ConfirmedNeedLedgerHit, SourceResultID: "result_parent"}
	engine.recordConfirmedNeedLedger(context.Background(), acceptancePrincipal(), ledger, applied)

	if len(telemetry.confirmedNeedLedgers) != 1 {
		t.Fatalf("confirmedNeedLedgers = %#v, want exactly one record", telemetry.confirmedNeedLedgers)
	}
	got := telemetry.confirmedNeedLedgers[0]
	if got.Outcome != ConfirmedNeedLedgerHit || got.SourceResultID != "result_parent" ||
		got.AppliedExpectedKind != contractsv1.ContextFabricSubjectTeam || got.AppliedAnchorKind != contractsv1.ContextFabricSubjectProject ||
		got.AppliedAnchorValueHash != confirmedNeedValueHash("p") {
		t.Fatalf("recorded = %#v, want outcome=hit source=result_parent expected_kind=team anchor_kind=project anchor_value_hash=hash(p)", got)
	}
	if len(got.AppliedMembers) != 2 {
		t.Fatalf("AppliedMembers = %v, want both members reported", got.AppliedMembers)
	}
}

// TestResolveConfirmedNeedLedger_DropsOnStaleGraphEpoch pins the CHAOS-3898
// §2.2 ingress taint gate applied to the ledger: a parent saved at a
// DIFFERENT graph epoch than this turn's own binding must never
// admit its ledger, exactly as walkCarriedKind already refuses a stale-epoch
// carrier for the legacy chain walk (structure_axis_carry.go).
func TestResolveConfirmedNeedLedger_DropsOnStaleGraphEpoch(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	store := ledgerTestStore(parentResult, parentState)
	staleEpoch := int64(7)
	store.graphEpoch = &staleEpoch
	engine := buildCarryTestEngine(t, store)

	got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{Epoch: 8})
	if got.Outcome != ConfirmedNeedLedgerDroppedStaleGraphEpoch {
		t.Fatalf("Outcome = %q, want %q: a parent from a different graph epoch must never admit its ledger", got.Outcome, ConfirmedNeedLedgerDroppedStaleGraphEpoch)
	}
	if got.Entries != nil {
		t.Fatalf("Entries = %#v, want nil on a stale-epoch drop", got.Entries)
	}
	// Same epoch on both sides -- the ordinary case every other test in this
	// file exercises -- must still hit.
	if got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{Epoch: 7}); got.Outcome != ConfirmedNeedLedgerHit {
		t.Fatalf("Outcome = %q, want %q when the epochs agree", got.Outcome, ConfirmedNeedLedgerHit)
	}
}

// TestResolveConfirmedNeedLedger_MissUnloadableForMalformedSnapshot pins the
// distinction Info telemetry must preserve: an unreadable snapshot
// (malformed, oversized, unsupported-version, or unreported) is a DIFFERENT
// fact than a clean read that simply has no ledger -- miss_unloadable, never
// miss_empty.
func TestResolveConfirmedNeedLedger_MissUnloadableForMalformedSnapshot(t *testing.T) {
	t.Parallel()
	for _, status := range []SemanticStateReadStatus{
		SemanticStateReadMalformed, SemanticStateReadOversized, SemanticStateReadUnsupportedVersion,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
			store := ledgerTestStore(parentResult, parentState)
			store.stateReads = map[string]SemanticStateReadStatus{parentResult.ResultID: status}
			engine := buildCarryTestEngine(t, store)

			got := engine.resolveConfirmedNeedLedger(context.Background(), acceptancePrincipal(), childRequest, ResolvedGraphBinding{})
			if got.Outcome != ConfirmedNeedLedgerMissUnloadable {
				t.Fatalf("Outcome = %q, want %q for a %s snapshot -- not miss_empty, which means a CLEAN read with nothing confirmed", got.Outcome, ConfirmedNeedLedgerMissUnloadable, status)
			}
		})
	}
}

// TestWithoutSupersededConfirmedNeeds_DropsOnlyTheRefusedMember pins the
// invariant: a receipt whose atomic supersession claim just lost the race
// must never reach the veto result's own persisted
// ledger, or a later turn naming that veto result as parent would admit a
// confirmation this exact Save call refused. Every OTHER member survives
// untouched.
func TestWithoutSupersededConfirmedNeeds_DropsOnlyTheRefusedMember(t *testing.T) {
	t.Parallel()
	entries := []ConfirmedNeedEntry{
		{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)},
		{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: contractsv1.ContextFabricSubjectPullRequest, AppliedValue: "42"},
	}
	got := withoutSupersededConfirmedNeeds(entries, []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind})
	want := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedKind: contractsv1.ContextFabricSubjectPullRequest, AppliedValue: "42"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withoutSupersededConfirmedNeeds() = %#v, want %#v", got, want)
	}
	if got := withoutSupersededConfirmedNeeds(entries, nil); !reflect.DeepEqual(got, entries) {
		t.Fatalf("withoutSupersededConfirmedNeeds(nil) = %#v, want the input unchanged: %#v", got, entries)
	}
}

// capturingKindGraphReader is graphReaderStub's own shape plus the ONE thing
// it discards (engine_test.go's own graphReaderStub.ResolveSubjects takes
// *ConfirmedExpectedKind as `_`): the confirmed kind actually threaded into
// ResolveSubjects, so a test can prove a remembered kind reached the real
// resolution parameter, not merely that some internal helper computed one.
type capturingKindGraphReader struct {
	resolution      SubjectResolution
	bases           CommitBasisSet
	confirmedKinds  []*contractsv1.ContextFabricSubjectKind
	confirmedKindsN int
}

func (g *capturingKindGraphReader) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "capturing-kind-key", Epoch: 0}, nil
}

func (g *capturingKindGraphReader) ResolveSubjects(_ context.Context, _ storage.Principal, _ InvestigationRequest, _ InterpretedQuestion, _ ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, _ *ConfirmedAnchorSelection, _ *QuestionFrame, _ SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.confirmedKindsN++
	if confirmedKind != nil {
		kind := confirmedKind.Kind
		g.confirmedKinds = append(g.confirmedKinds, &kind)
	} else {
		g.confirmedKinds = append(g.confirmedKinds, nil)
	}
	return g.resolution, StructureOfferMaterial{}, g.bases, nil, nil
}

func (g *capturingKindGraphReader) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return emptyGraphContext(), nil
}

// TestInvestigate_ConfirmedNeedLedgerAppliesThroughThePublicEntryPoint closes
// a gap every other test in this file leaves open: they call
// resolveConfirmedNeedLedger/appliedNeedLedgerEntries/confirmedAnchorSelection
// directly, so a defect in how Investigate WIRES them together (an argument
// swapped, a gate ordered wrong, a value dropped between the ledger consult
// and the real ResolveSubjects call) could pass the whole package suite
// while the feature does nothing for a real caller. This test drives the
// SAME turn-two-confirms/turn-three-continues scenario
// TestResolveConfirmedNeedLedger_HitsWhenIdentityMatches pins, but through
// Engine.Investigate itself: turn three sends NO receipt at all, and the
// remembered expected_kind=team must reach graph.ResolveSubjects's own
// confirmed-kind parameter.
func TestInvestigate_ConfirmedNeedLedgerAppliesThroughThePublicEntryPoint(t *testing.T) {
	t.Parallel()
	parentResult, parentState, childRequest := parentAndChildForLedgerTest(t)
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	store := ledgerTestStore(parentResult, parentState)
	graph := &capturingKindGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		bases:      provenCommitBases(project),
	}
	fresh := validInvestigationResult()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}}, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}, Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{}}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return fresh, nil
		}),
		Results: store,
	}, EngineOptions{
		ServiceVersion: "chaos-5639-e2e-test",
		Now:            func() time.Time { return time.Unix(500, 0).UTC() },
		NewResultID:    func() string { return "result_turn_three" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	// request.Validate() (called from Investigate, never from a direct
	// resolveConfirmedNeedLedger call the way every other test in this file
	// exercises it) requires each conversation turn to carry a turn id and a
	// timestamp -- parentAndChildForLedgerTest's fixture omits both because
	// none of its other callers reach Validate.
	for i := range childRequest.Conversation {
		childRequest.Conversation[i].TurnID = fmt.Sprintf("turn_%d", i)
		childRequest.Conversation[i].CreatedAt = time.Unix(int64(490+i), 0).UTC()
	}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), childRequest)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.confirmedKindsN != 1 {
		t.Fatalf("ResolveSubjects called %d times, want exactly 1", graph.confirmedKindsN)
	}
	if got := graph.confirmedKinds[0]; got == nil || *got != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("ResolveSubjects saw confirmed kind %v, want team -- the remembered ledger from turn two must reach the real resolution call with NO receipt redeemed on turn three", got)
	}
	if store.savedSemantic == nil || store.savedSemantic.State == nil {
		t.Fatalf("fixture defect: turn three must have saved a semantic state")
	}
	wantSaved := []ConfirmedNeedEntry{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam)}}
	if !reflect.DeepEqual(store.savedSemantic.State.ConfirmedNeeds, wantSaved) {
		t.Fatalf("saved ConfirmedNeeds = %#v, want %#v: the ledger must carry forward for a turn four to consult", store.savedSemantic.State.ConfirmedNeeds, wantSaved)
	}
	_ = result
}
