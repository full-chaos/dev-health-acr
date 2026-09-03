package contextfabric

import (
	"context"
	"errors"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// kindRecordingGraphReader embeds graphReaderStub and records the
// *ConfirmedExpectedKind each ResolveSubjects call received. The engine's
// own seam is exactly that argument -- everything downstream (the pool
// filter, and through it the kind offer's cardinality suppression) hangs
// off it -- so a test that asserts on it is asserting on the thing the
// carry actually changes, not on a stub's scripted reply.
type kindRecordingGraphReader struct {
	graphReaderStub
	seen []*ConfirmedExpectedKind
}

func (g *kindRecordingGraphReader) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.seen = append(g.seen, confirmedKind)
	return g.graphReaderStub.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, scopeAnchorKind)
}

// TestKindCarry_TurnThatIsOfferedNothingStillResolvesUnderTheConfirmedKind
// is this lane's test 2: the replay of the measured r3 chain shape, reduced
// to the one turn that matters.
//
// THE MEASURED SHAPE (leg :3046 -> :18096, question "Which repositories does
// the Ops Team own?"): turn 1 raises expected_kind and window; turn 2 answers
// both and its response then offers NEITHER; turn 3, an honest offer-driven
// client having nothing left to re-present, is asked the same two needs
// again. Turns-to-terminal on identical input measured 1, 5 and >8, with one
// replicate never terminating.
//
// This test is turn 3 of a LINKED chain -- the request still names turn 2
// through a candidate receipt, which is the case the carry can serve. The
// unlinked case (turn 3 names nothing at all, measured miss_no_reference on
// three separate request_ids) is NOT fixed by this mechanism and is not
// pretended to be here: it needs chain identity the client can supply
// without a receipt, which is a contract question ruled elsewhere.
//
// What this pins: turn 3 resolves under the kind turn 2 confirmed, rather
// than under nil, and says so on the wire. The consequence -- that the
// expected_kind need is then not re-raised -- follows from the pool filter
// narrowing to a single kind, which is graphrank's own cardinality
// suppression and has its own coverage there; this test pins the seam that
// feeds it.
func TestKindCarry_TurnThatIsOfferedNothingStillResolvesUnderTheConfirmedKind(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_ops", Label: "Ops Team"}

	// Turn 2's persisted result: it confirmed expected_kind=team AND a
	// window by redeeming the receipts turn 1 offered, and its own response
	// then offered neither axis again.
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_two"
	priorResult.Status = InvestigationClarificationRequired
	priorResult.EffectiveEvidenceWindow = &contractsv1.ContextFabricEffectiveEvidenceWindow{
		RelativeID: RelativeWindowTrailing90D, Provenance: WindowClarificationConfirmed,
	}
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_one", "kindr_turn_one_01"),
	}
	priorResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_turn_two_candidate", Subject: team, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_turn_two": priorResult}}
	telemetry := &recordingTelemetry{}
	graph := &kindRecordingGraphReader{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{{
				ReceiptID: "receipt_turn_three_candidate", Subject: team, State: ResolutionCommitted,
				MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 0.97,
			}},
			Committed: []SubjectRef{team},
		},
		context: emptyGraphContext(),
		bases:   provenCommitBases(team),
	}}

	engine, err := NewEngine(EngineDependencies{
		Interpreter: &countingInterpreter{interpretation: bootstrapInterpretation()},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "The Ops Team owns two repositories.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "The Ops Team owns two repositories.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "structure-axis-carry-test",
		Now:            func() time.Time { return time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_turn_three" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	// Turn 3 as an honest offer-driven client sends it: no expected_kind of
	// its own, no kindr_ receipt (turn 2 offered none to redeem), and the
	// chain named only through the candidate offer turn 2 DID make.
	request := validInvestigationRequest()
	request.ExpectedKinds = nil
	// A SUBJECT candidate receipt, which is literally what the measured
	// driver sent on these turns (lane-4973's receipt column): turn 2 offered
	// three subject candidates and no kind or window options, so a candidate
	// pick is the only thing an offer-driven client had to send.
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_two", ReceiptID: "receipt_turn_two_candidate"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if len(graph.seen) == 0 {
		t.Fatal("ResolveSubjects was never called; the test proves nothing about the seam")
	}
	for i, seen := range graph.seen {
		if seen == nil {
			t.Fatalf("ResolveSubjects call %d received a nil *ConfirmedExpectedKind, want the carried team: turn 3 must not resolve as though nothing had been confirmed", i)
		}
		if seen.Kind != contractsv1.ContextFabricSubjectTeam {
			t.Fatalf("ResolveSubjects call %d received kind %q, want %q", i, seen.Kind, contractsv1.ContextFabricSubjectTeam)
		}
	}

	wantEntry := contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_turn_two",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	found := false
	for _, entry := range result.ConfirmedStructure {
		if entry == wantEntry {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ConfirmedStructure = %#v, want an entry %#v: a carry the caller cannot see is a silent inheritance", result.ConfirmedStructure, wantEntry)
	}

	if len(telemetry.kindCarries) != 1 || telemetry.kindCarries[0] != (kindCarryRecord{KindCarryHit, 0}) {
		t.Fatalf("telemetry.kindCarries = %#v, want exactly one hit at depth 0", telemetry.kindCarries)
	}
}

// TestKindCarry_ThisTurnsOwnReceiptWinsOverACarriedKind pins the precedence
// rule: a caller redeeming a kindr_ receipt on THIS request is stating a
// kind now, and a value inherited from an earlier turn must never override
// what the caller just said. The carry fills a silence; it does not argue
// with a statement. Without this, a caller could never change its mind
// about the kind for the rest of a conversation.
func TestKindCarry_ThisTurnsOwnReceiptWinsOverACarriedKind(t *testing.T) {
	t.Parallel()
	own := []confirmedStructureMember{{
		Member:       contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue: string(contractsv1.ContextFabricSubjectProject),
	}}
	carry := kindCarryResult{Kind: contractsv1.ContextFabricSubjectTeam, SourceResultID: "result_turn_a", Outcome: KindCarryHit}

	got := effectiveConfirmedKind(own, carry)
	if got == nil || got.Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("effectiveConfirmedKind(own=project, carried=team) = %#v, want project: this turn's own receipt wins", got)
	}
	if got := effectiveConfirmedKind(nil, carry); got == nil || got.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("effectiveConfirmedKind(no own receipt, carried=team) = %#v, want team", got)
	}
	if got := effectiveConfirmedKind(nil, kindCarryResult{Outcome: KindCarryMissNoReference}); got != nil {
		t.Fatalf("effectiveConfirmedKind(no own receipt, miss) = %#v, want nil", got)
	}
}

// flakyResultStore fails the FIRST Get of a given result id and succeeds on
// every later one -- the transient-failure shape the per-request load memo
// must not freeze in place.
type flakyResultStore struct {
	staticResultStore
	failed map[string]bool
	calls  int
}

func (s *flakyResultStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	s.calls++
	if s.failed == nil {
		s.failed = map[string]bool{}
	}
	if !s.failed[resultID] {
		s.failed[resultID] = true
		return StoredInvestigationResult{}, errors.New("transient store failure")
	}
	return s.staticResultStore.Get(ctx, principal, resultID)
}

// TestCarryResultCache_DoesNotMemoiseFailures is codex round 1's memo finding.
// Caching the error would replay the FIRST axis's transient failure to the
// second, so one axis's miss would suppress the other's hit -- contradicting
// the rule this mechanism states about the two axes being independent.
// InvestigationResultStore.Get promises nothing about error stability within a
// request, so only successful, immutable reads may be memoised.
func TestCarryResultCache_DoesNotMemoiseFailures(t *testing.T) {
	t.Parallel()
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_a"
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	store := &flakyResultStore{staticResultStore: staticResultStore{
		results: map[string]InvestigationResult{"result_turn_a": priorResult},
	}}
	engine := buildCarryTestEngine(t, store)
	ctx := withCarryResultCache(context.Background())

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_a", ReceiptID: "candr_turn_a_01"}}

	// First axis: the store fails, so this axis correctly misses.
	first := engine.resolveCarriedKind(ctx, acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if first.Outcome != KindCarryMissUnloadable {
		t.Fatalf("first attempt Outcome = %q, want %q", first.Outcome, KindCarryMissUnloadable)
	}
	// Second axis, SAME request context: the failure must not have been
	// memoised, so this load reaches the store again and succeeds.
	second := engine.resolveCarriedKind(ctx, acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if second.Outcome != KindCarryHit || second.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("second attempt = %#v, want a hit carrying team: a transient failure must not be frozen into the memo", second)
	}
	if store.calls != 2 {
		t.Fatalf("store.calls = %d, want 2 (the failure retried, the success then memoised)", store.calls)
	}
	// Third attempt proves the SUCCESS is memoised -- the memo still does its job.
	if third := engine.resolveCarriedKind(ctx, acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0}); third.Outcome != KindCarryHit {
		t.Fatalf("third attempt = %#v, want a hit", third)
	}
	if store.calls != 2 {
		t.Fatalf("store.calls = %d after a third attempt, want still 2: a successful read must be memoised", store.calls)
	}
}

// TestResolveCarriedKind_FailsClosedWhenADepthCouldNotBeFullyScanned is codex
// round 1's finding 2. The conflict check can only see candidates it actually
// visited, so a hit found alongside a sibling that could NOT be read is a hit
// whose uniqueness is unproven -- the unread sibling may carry a different
// kind. Returning it would resolve an ambiguity to a value, which is the one
// thing this mechanism must never do.
func TestResolveCarriedKind_FailsClosedWhenADepthCouldNotBeFullyScanned(t *testing.T) {
	t.Parallel()
	good := validInvestigationResult()
	good.ResultID = "result_readable"
	good.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_zero", "kindr_turn_zero_01"),
	}
	// The sibling at the SAME depth is refused by the epoch taint gate, so its
	// kind is never read. It is deliberately given a DIFFERENT kind to make
	// the stakes concrete: if this test ever goes green while returning a hit,
	// the engine answered under `team` while an unread sibling said `project`.
	stale := validInvestigationResult()
	stale.ResultID = "result_stale_sibling"
	stale.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectProject, "result_turn_zero", "kindr_turn_zero_02"),
	}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{
			"result_readable": good, "result_stale_sibling": stale,
		}},
		epochs: map[string]int64{"result_readable": 0, "result_stale_sibling": 7},
	}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_readable", ReceiptID: "candr_readable_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_stale_sibling", ReceiptID: "kindr_stale_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome == KindCarryHit {
		t.Fatalf("resolveCarriedKind() = %#v, want a miss: a depth with an unreadable sibling cannot prove the hit is unique", got)
	}
	if got.Outcome != KindCarryMissStaleGraphEpoch {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q (what truncated the scan)", got.Outcome, KindCarryMissStaleGraphEpoch)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty", got.Kind)
	}
}

// perResultEpochStore reports a different GraphEpoch per result id, so one
// sibling can pass the taint gate while another is refused.
type perResultEpochStore struct {
	staticResultStore
	epochs map[string]int64
}

func (s *perResultEpochStore) Get(ctx context.Context, principal storage.Principal, resultID string) (StoredInvestigationResult, error) {
	stored, err := s.staticResultStore.Get(ctx, principal, resultID)
	if err != nil {
		return stored, err
	}
	if epoch, ok := s.epochs[resultID]; ok {
		stored.GraphEpoch = &epoch
	}
	return stored, nil
}

// TestKindCarry_IsDisclosedOnTheClassDefaultWindowGate is codex round 1's
// finding 1. On the class-default window path the request never reaches a
// decisive resolution -- it returns through the gate's own terminal -- but the
// carried kind HAS already narrowed the offers-only pool that shapes what the
// caller is offered. A result that omitted the carry entry would let an
// inherited value change the offer set while telling the caller nothing about
// it, which is the silent inheritance this whole mechanism exists to prevent.
//
// The fixture deliberately gives the prior result NO carriable window, so the
// window carry misses, the effective window stays inferred_default, and the
// gate fires -- which is the only way to reach that terminal.
func TestKindCarry_IsDisclosedOnTheClassDefaultWindowGate(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_ops", Label: "Ops Team"}

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_two"
	priorResult.Status = InvestigationClarificationRequired
	priorResult.EffectiveEvidenceWindow = nil // nothing to carry on the window axis
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_turn_one", "kindr_turn_one_01"),
	}
	priorResult.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{{
			ReceiptID: "receipt_turn_two_candidate", Subject: team, State: ResolutionCommitted,
			MatchReasons: []string{"Exact canonical subject hint matched the organization graph."}, Confidence: 1,
		}},
		Committed: []SubjectRef{},
	}
	store := &staticResultStore{results: map[string]InvestigationResult{"result_turn_two": priorResult}}
	telemetry := &recordingTelemetry{}
	graph := &kindRecordingGraphReader{graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    emptyGraphContext(),
	}, nil}

	engine, err := NewEngine(EngineDependencies{
		Interpreter: &countingInterpreter{interpretation: bootstrapInterpretation()},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not run: the class-default gate returns before any fact read")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not run: the class-default gate returns before synthesis")
			return InvestigationResult{}, nil
		}),
		Results: store, Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "structure-axis-carry-gate-test",
		Now:            func() time.Time { return time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_turn_three" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest() // no EvidenceWindow: the class default gates it
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_two", ReceiptID: "receipt_turn_two_candidate"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(telemetry.kindCarries) != 1 || telemetry.kindCarries[0].outcome != KindCarryHit {
		t.Fatalf("telemetry.kindCarries = %#v, want exactly one hit -- without a carry this test proves nothing", telemetry.kindCarries)
	}
	want := contractsv1.ContextFabricConfirmedStructureEntry{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_turn_two",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}
	for _, entry := range result.ConfirmedStructure {
		if entry == want {
			return
		}
	}
	t.Fatalf("ConfirmedStructure = %#v, want an entry %#v: the gate's own terminal must disclose a carry that shaped its offers", result.ConfirmedStructure, want)
}

// TestResolveCarriedKind_DoesNotDescendPastATruncatedDepth is codex round 2's
// first finding. Rejecting a hit found AT a truncated depth is not enough: if
// that depth yields no hit at all, the walk used to descend and return a
// deeper one. Both rest on the same unproven assumption -- that the
// candidates we could not read carry nothing that matters.
//
// It matters twice over here. "Nearest confirmation wins" is this walk's own
// rule, so an unread sibling at depth 0 may hold a NEARER confirmation than
// the depth-1 hit, and it may hold a CONFLICTING one. The fixture below makes
// both true at once: the unreadable depth-0 sibling says `project`, the
// reachable depth-1 result says `team`. A regression answers `team` while a
// nearer, unread result said otherwise.
func TestResolveCarriedKind_DoesNotDescendPastATruncatedDepth(t *testing.T) {
	t.Parallel()
	// Depth 0, readable, confirms nothing itself but breadcrumbs to depth 1.
	breadcrumb := validInvestigationResult()
	breadcrumb.ResultID = "result_depth0_breadcrumb"
	breadcrumb.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedWindow, AppliedValue: "trailing_30d",
		Source: contractsv1.ContextFabricStructureSourceReceipt, PriorResultID: "result_depth1",
		ReceiptID: "winr_depth1_01", Provenance: contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	// Depth 0, UNREADABLE (refused by the epoch taint gate), and it holds a
	// conflicting kind that the walk must never get to ignore.
	unreadable := validInvestigationResult()
	unreadable.ResultID = "result_depth0_unreadable"
	unreadable.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectProject, "result_older", "kindr_older_01"),
	}
	// Depth 1, readable, confirms a DIFFERENT kind.
	deeper := validInvestigationResult()
	deeper.ResultID = "result_depth1"
	deeper.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_older", "kindr_older_02"),
	}
	store := &perResultEpochStore{
		staticResultStore: staticResultStore{results: map[string]InvestigationResult{
			"result_depth0_breadcrumb": breadcrumb,
			"result_depth0_unreadable": unreadable,
			"result_depth1":            deeper,
		}},
		epochs: map[string]int64{
			"result_depth0_breadcrumb": 0,
			"result_depth0_unreadable": 7, // refused
			"result_depth1":            0,
		},
	}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_depth0_breadcrumb", ReceiptID: "candr_depth0_01"}}
	request.PriorKindReceipts = []BoundSubjectReceipt{{ResultID: "result_depth0_unreadable", ReceiptID: "kindr_depth0_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome == KindCarryHit {
		t.Fatalf("resolveCarriedKind() = %#v, want a miss: the walk must not descend past a depth it could not finish reading", got)
	}
	if got.Kind != "" {
		t.Fatalf("resolveCarriedKind().Kind = %q, want empty", got.Kind)
	}
	if got.Outcome != KindCarryMissStaleGraphEpoch {
		t.Fatalf("resolveCarriedKind().Outcome = %q, want %q -- the reason the depth was truncated, reported at that depth", got.Outcome, KindCarryMissStaleGraphEpoch)
	}
}

// TestKindCarry_ExplicitExpectedKindBlocksTheCarry is codex round 2's second
// finding, and it is a VALIDITY bug rather than a disclosure one. An explicit
// ExpectedKinds value is echoed on the result by composeConfirmedStructure; a
// carried entry appended alongside it puts TWO expected_kind entries on one
// result, and the v1 validator rejects that outright ("one entry per member").
// So a request that both states a kind and names a carryable prior result
// would fail validation rather than answer.
//
// Blocking the carry is also the right precedence independently: the caller
// stated a kind on THIS turn, and a carry fills a silence.
func TestKindCarry_ExplicitExpectedKindBlocksTheCarry(t *testing.T) {
	t.Parallel()
	canon := requestStructureCanonicalization{
		Explicit: []explicitStructureMember{{
			Member:       contractsv1.ContextFabricStructureNeedExpectedKind,
			AppliedValue: string(contractsv1.ContextFabricSubjectProject),
			Source:       contractsv1.ContextFabricStructureSourceExplicitUnattributed,
			Provenance:   contractsv1.ContextFabricStructureInferredDefault,
		}},
	}
	if !statedExpectedKindThisTurn(InvestigationRequest{}, canon) {
		t.Fatal("statedExpectedKindThisTurn(explicit expected_kind) = false, want true: an explicit kind must block the carry, or the result carries two expected_kind entries and fails validation")
	}
	// A receipt-confirmed kind blocks it too (the pre-existing precedence).
	confirmedOnly := requestStructureCanonicalization{
		Confirmed: []confirmedStructureMember{{
			Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		}},
	}
	if !statedExpectedKindThisTurn(InvestigationRequest{}, confirmedOnly) {
		t.Fatal("statedExpectedKindThisTurn(receipt-confirmed kind) = false, want true")
	}
	// An explicit member for a DIFFERENT axis must not block it.
	otherAxis := requestStructureCanonicalization{
		Explicit: []explicitStructureMember{{
			Member: contractsv1.ContextFabricStructureNeedSubjectHandle, AppliedValue: "acr-123",
		}},
	}
	if statedExpectedKindThisTurn(InvestigationRequest{}, otherAxis) {
		t.Fatal("statedExpectedKindThisTurn(explicit subject_handle) = true, want false: only the kind axis blocks the kind carry")
	}
	if statedExpectedKindThisTurn(InvestigationRequest{}, requestStructureCanonicalization{}) {
		t.Fatal("statedExpectedKindThisTurn(nothing stated) = true, want false")
	}
	// A PLURAL explicit value has no echoed member at all, so reading
	// canon.Explicit alone treats a caller who named two kinds as having
	// named none (codex round 3). The request field is the authority.
	plural := InvestigationRequest{ExpectedKinds: []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject,
	}}
	if !statedExpectedKindThisTurn(plural, requestStructureCanonicalization{}) {
		t.Fatal("statedExpectedKindThisTurn(two stated kinds, no echo) = false, want true: a plural explicit value is still the caller stating a kind this turn")
	}
}
