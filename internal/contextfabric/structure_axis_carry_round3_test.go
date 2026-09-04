package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Round-3 review findings, FIXED in the same change that added these tests.
// They were written red first, against the defects, and now pin the fixes.

// TestKindCarry_PluralExplicitExpectedKindsBlocksTheCarry pins round 3's
// finding 1, which was a precedence violation on a reachable request shape.
//
// An explicit expected_kind is echoed onto the result ONLY when the caller
// states exactly one (resolveExplicitStructure, structure.go) -- a plural
// value narrows and shapes offers but has no single value to echo. The carry's
// block used to read that ECHO, so a caller who stated TWO kinds was treated
// as having stated none, and an inherited kind from a linked prior result
// overrode them both. It now reads request.ExpectedKinds directly.
//
// Note what this is NOT: plural has no echo, so it never collides into the
// duplicate-entry validation failure round 2 fixed. Nothing fails loudly. The
// caller simply gets a pool narrowed to a kind they did not ask for on this
// turn, which is exactly the silent override the carry is supposed to refuse.
func TestKindCarry_PluralExplicitExpectedKindsBlocksTheCarry(t *testing.T) {
	t.Parallel()
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team_ops", Label: "Ops Team"}

	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_turn_two"
	priorResult.Status = InvestigationClarificationRequired
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
	graph := &kindRecordingGraphReader{graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    emptyGraphContext(),
	}, nil}

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
				Status: InvestigationComplete, DirectJudgment: "ok", CurrentState: "ok",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "ok", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store,
	}, EngineOptions{
		ServiceVersion: "structure-axis-carry-r3-f1",
		Now:            func() time.Time { return time.Date(2026, 9, 3, 20, 0, 0, 0, time.UTC) },
		NewResultID:    func() string { return "result_turn_three" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}

	request := validInvestigationRequest()
	// TWO kinds stated on THIS turn. Neither is echoed, by design.
	request.ExpectedKinds = []contractsv1.ContextFabricSubjectKind{
		contractsv1.ContextFabricSubjectTeam, contractsv1.ContextFabricSubjectProject,
	}
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_two", ReceiptID: "receipt_turn_two_candidate"}}

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	for i, seen := range graph.seen {
		if seen != nil {
			t.Fatalf("ResolveSubjects call %d received a carried ConfirmedExpectedKind %q; want nil: the caller stated two kinds on THIS turn, so nothing may be inherited over them", i, seen.Kind)
		}
	}
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			t.Fatalf("ConfirmedStructure carries %#v; want no carried expected_kind when the caller stated kinds this turn", entry)
		}
	}
}

// TestKindCarry_SaveTimeSupersessionVetoStillDisclosesTheCarry pins round 3's
// finding 2, which was a re-find of round 1's disclosure class one path over.
//
// The decisive path, the subjectless terminal and the class-default window
// gate all append the per-axis carry disclosures. The three Save-time
// supersession race paths returned through structureSupersessionVetoResult,
// which rendered only the stale receipt entries -- so a turn that genuinely
// inherited a kind, and resolved under it, reported nothing about it on that
// terminal.
//
// COVERAGE LIMIT, stated rather than left to be discovered: this pins the
// WINDOW race site (window.go's own call). The other two call sites
// (engine.go's decisive path and unresolved.go's subjectless terminal) pass
// the same entries through the same helper and are NOT independently pinned.
//
// The shape below is the window race (window.go's own call site): the request
// redeems a winr_ receipt from a prior result that ALSO carries a
// receipt-confirmed expected_kind, so the kind carry hits, and then the Save
// loses the window claim race.
func TestKindCarry_SaveTimeSupersessionVetoStillDisclosesTheCarry(t *testing.T) {
	t.Parallel()

	frozenStart := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	frozenEnd := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	priorResult := validInvestigationResult()
	priorResult.ResultID = "result_prior_carry_0007"
	priorResult.WindowClarification = &WindowClarification{Options: []WindowOption{
		{ReceiptID: "winr_confirm0007", OptionID: "opt_90d", Label: "the last 90 days", RelativeID: RelativeWindowTrailing90D, Start: &frozenStart, End: &frozenEnd},
	}}
	// The linked prior ALSO carries a receipt-confirmed kind, so this turn's
	// carry hits while the window receipt is what loses the race.
	priorResult.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_prior_carry_0006", "kindr_confirm0006"),
	}
	store := &supersessionRacingResultStore{
		staticResultStore: &staticResultStore{results: map[string]InvestigationResult{priorResult.ResultID: priorResult}},
		conflictMembers:   []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow},
	}
	telemetry := &recordingTelemetry{}

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
		Results:   store,
		Telemetry: telemetry,
	})

	request := validInvestigationRequest()
	request.PriorWindowReceipts = []BoundSubjectReceipt{{ResultID: priorResult.ResultID, ReceiptID: "winr_confirm0007"}}

	result, err := engine.Investigate(context.Background(), reusePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	// Guard first: without a carry hit this test proves nothing about the
	// disclosure, so a change that stops the carry firing must not read as
	// a pass here.
	hit := false
	for _, c := range telemetry.kindCarries {
		if c.outcome == KindCarryHit {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("telemetry.kindCarries = %#v, want a hit -- the fixture must actually carry a kind for the disclosure to be at stake", telemetry.kindCarries)
	}
	carried := false
	for _, entry := range result.ConfirmedStructure {
		if entry.Member == contractsv1.ContextFabricStructureNeedExpectedKind &&
			entry.Source == contractsv1.ContextFabricStructureSourceCarried {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("ConfirmedStructure = %+v, want a source=carried expected_kind entry: a carry that applied must be disclosed on the supersession-veto terminal too, not only on the decisive and gate paths", result.ConfirmedStructure)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result fails Validate(): %v", err)
	}
	_ = storage.Principal{}
}

// TestKindCarry_AReceiptThatStatesAKindThisTurnBlocksTheCarry is the design
// review's S2, and it is the sharpest of the carry's failure modes because the
// value it can override is one the caller picked EXPLICITLY on this turn.
//
// Every receipt redeemed this turn records the offer's own kind on the
// confirmed member (`AppliedKind`, structure.go) -- not just kind receipts.
// A candidate receipt (candr_) names a specific ranked subject, and an anchor
// receipt (ancr_) names a scope anchor; both carry that subject's kind. But
// the carry's block only looked at the expected_kind member and at
// request.ExpectedKinds, so neither counted as the caller stating a kind.
//
// The shape that bites: the caller picks a TEAM candidate from a prior result
// whose own kind was never confirmed. The walk finds no kind at that result,
// descends, and inherits `project` from further back. That carried `project`
// then narrows the pool -- and filters out the very team candidate the caller
// just chose. The caller's own explicit pick loses to an inherited value.
func TestKindCarry_AReceiptThatStatesAKindThisTurnBlocksTheCarry(t *testing.T) {
	t.Parallel()
	// A candidate receipt redeemed this turn, naming a team subject.
	candidatePick := requestStructureCanonicalization{
		Confirmed: []confirmedStructureMember{{
			Member:       contractsv1.ContextFabricStructureNeedSubjectCandidate,
			AppliedValue: "team_ops",
			AppliedKind:  contractsv1.ContextFabricSubjectTeam,
		}},
	}
	if !statedExpectedKindThisTurn(InvestigationRequest{}, candidatePick) {
		t.Fatal("statedExpectedKindThisTurn(candidate receipt naming a team) = false, want true: the caller picked a subject OF A KIND this turn, and an inherited kind must not be allowed to filter that very pick out of the pool")
	}
	// The same for an anchor receipt, which names the scope anchor's kind.
	anchorPick := requestStructureCanonicalization{
		Confirmed: []confirmedStructureMember{{
			Member:       contractsv1.ContextFabricStructureNeedSubjectAnchor,
			AppliedValue: "team_ops",
			AppliedKind:  contractsv1.ContextFabricSubjectTeam,
		}},
	}
	if !statedExpectedKindThisTurn(InvestigationRequest{}, anchorPick) {
		t.Fatal("statedExpectedKindThisTurn(anchor receipt naming a team) = false, want true")
	}
	// A confirmed member carrying NO kind must still not block. This shape is
	// NOT reachable in production and the case is deliberately defensive: the
	// contract FORBIDS it, because every option validator rejects an empty
	// kind (validate_context_fabric_structure.go), so a redeemed member always
	// carries one. The guard and this case exist so that if that invariant is
	// ever relaxed, a kindless member fails open into "states nothing to
	// protect" rather than silently blocking every carry -- and so a reader
	// does not mistake the guard for a live branch.
	kindlessHandle := requestStructureCanonicalization{
		Confirmed: []confirmedStructureMember{{
			Member:       contractsv1.ContextFabricStructureNeedSubjectHandle,
			AppliedValue: "acr-123",
		}},
	}
	if statedExpectedKindThisTurn(InvestigationRequest{}, kindlessHandle) {
		t.Fatal("statedExpectedKindThisTurn(handle receipt with no kind) = true, want false: a member that states no kind states nothing to protect")
	}
}

// vetoedKindEntry builds the shape a turn PERSISTS when its kind receipt lost
// the save-time supersession race: receipt-sourced, expected_kind, and marked
// vetoed_stale. This exact shape is produced by the supersession-veto path and
// is asserted by that path's own test, so it is a real stored artefact rather
// than a hypothetical.
func vetoedKindEntry(kind contractsv1.ContextFabricSubjectKind, disposition contractsv1.ContextFabricStructureDisposition) contractsv1.ContextFabricConfirmedStructureEntry {
	return contractsv1.ContextFabricConfirmedStructureEntry{
		Member:        contractsv1.ContextFabricStructureNeedExpectedKind,
		AppliedValue:  string(kind),
		Source:        contractsv1.ContextFabricStructureSourceReceipt,
		PriorResultID: "result_turn_zero",
		ReceiptID:     "kindr_turn_zero_01",
		Provenance:    contractsv1.ContextFabricStructureClarificationConfirmed,
		Disposition:   disposition,
	}
}

// TestResolveCarriedKind_RefusesAVetoedCarrier is the design review's critical
// finding: authority laundering.
//
// A kind the engine REFUSED could come back a turn later as caller authority.
// The eligibility check tested Member and Source only, and a receipt-sourced
// expected_kind entry marked `vetoed_stale` satisfies both -- so the walk
// carried it forward and composed a NEW entry with Disposition=applied. The
// refusal became an application, one hop later.
//
// This is reachable, not theoretical: the save-time supersession path persists
// exactly this shape, and this branch's own supersession test asserts it.
// Every vetoed disposition in the vocabulary is checked here, because the
// contract admits all three and the laundering works identically for each.
//
// SCOPE: this pins the NO-ANCESTOR shape only -- a vetoed carrier with nothing
// carriable behind it. A vetoed carrier MARKS the walk but does not STOP it,
// so where an eligible ancestor exists deeper in the chain the walk still
// reaches it and returns a hit; `miss_vetoed_carrier` is reported only when no
// hit was found anywhere. Whether a vetoed carrier should halt the walk
// outright is a separate design question, ticketed, and deliberately not
// decided here.
func TestResolveCarriedKind_RefusesAVetoedCarrier(t *testing.T) {
	t.Parallel()
	for _, disposition := range []contractsv1.ContextFabricStructureDisposition{
		contractsv1.ContextFabricStructureDispositionVetoedStale,
		contractsv1.ContextFabricStructureDispositionVetoedConflict,
		contractsv1.ContextFabricStructureDispositionVetoedUnresolved,
	} {
		t.Run(string(disposition), func(t *testing.T) {
			t.Parallel()
			prior := validInvestigationResult()
			prior.ResultID = "result_turn_a"
			prior.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
				vetoedKindEntry(contractsv1.ContextFabricSubjectTeam, disposition),
			}
			// The vetoed entry names the result whose offer it redeemed, so the
			// walk follows that breadcrumb. It is stocked here deliberately:
			// leaving it dangling would make the load FAIL and report
			// miss_unloadable, which would hide the veto behind an unrelated
			// read error and let this test pass for the wrong reason.
			breadcrumb := validInvestigationResult()
			breadcrumb.ResultID = "result_turn_zero"
			breadcrumb.ConfirmedStructure = nil
			store := &staticResultStore{results: map[string]InvestigationResult{
				"result_turn_a": prior, "result_turn_zero": breadcrumb,
			}}
			engine := buildCarryTestEngine(t, store)

			request := validInvestigationRequest()
			request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_turn_a", ReceiptID: "candr_turn_a_01"}}

			got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
			if got.Outcome == KindCarryHit {
				t.Fatalf("resolveCarriedKind() = %#v, want a miss: a %s entry is a kind the engine REFUSED, and carrying it forward relabels that refusal as applied caller authority", got, disposition)
			}
			if got.Kind != "" {
				t.Fatalf("resolveCarriedKind().Kind = %q, want empty", got.Kind)
			}
			// The miss must NAME the veto rather than look like an ordinary
			// "nothing to carry" -- an operator reading the telemetry has to be
			// able to tell a refused carrier from an absent one.
			if got.Outcome != KindCarryMissVetoedCarrier {
				t.Fatalf("resolveCarriedKind().Outcome = %q, want %q: a refused carrier must be disclosed as such, not folded into a generic miss", got.Outcome, KindCarryMissVetoedCarrier)
			}
		})
	}
}

// TestResolveCarriedKind_NamesTheOriginalConfirmationAcrossHops is the design
// review's provenance finding.
//
// A carried entry's prior_result_id is contractually the ORIGINAL confirmation,
// not whichever result happened to carry it last. The window carry flattens
// that (carriedWindowOrigin, chaos4360_carry.go); the kind carry did not, so a
// second hop pointed at the intermediate carrier and the disclosure quietly
// meant something different from what the contract says it means.
func TestResolveCarriedKind_NamesTheOriginalConfirmationAcrossHops(t *testing.T) {
	t.Parallel()
	// The ORIGIN: where the caller actually redeemed the kind receipt.
	origin := validInvestigationResult()
	origin.ResultID = "result_origin"
	origin.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{
		confirmedKindEntry(contractsv1.ContextFabricSubjectTeam, "result_before_origin", "kindr_origin_01"),
	}
	// The CARRIER: a later turn that inherited it and re-disclosed it as
	// carried, naming the origin. This is what every successful carry persists.
	carrier := validInvestigationResult()
	carrier.ResultID = "result_carrier"
	carrier.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_origin",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	store := &staticResultStore{results: map[string]InvestigationResult{
		"result_origin": origin, "result_carrier": carrier,
	}}
	engine := buildCarryTestEngine(t, store)

	request := validInvestigationRequest()
	request.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_carrier", ReceiptID: "candr_carrier_01"}}

	got := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request, nil, ResolvedGraphBinding{Epoch: 0})
	if got.Outcome != KindCarryHit || got.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("resolveCarriedKind() = %#v, want a hit carrying team", got)
	}
	if got.SourceResultID != "result_origin" {
		t.Fatalf("resolveCarriedKind().SourceResultID = %q, want %q -- the disclosure names the ORIGINAL confirmation, never the result that merely carried it last", got.SourceResultID, "result_origin")
	}
	entry := composeCarriedKindEntry(got)
	if entry == nil || entry.PriorResultID != "result_origin" {
		t.Fatalf("composeCarriedKindEntry() = %#v, want prior_result_id = result_origin", entry)
	}

	// THREE hops, which is the induction step rather than a second example.
	// Two hops can be satisfied by a rule that reaches back exactly one level;
	// only a third shows the origin is preserved by every hop rather than
	// merely recovered by the first. Each carrier here re-discloses the SAME
	// origin, which is what a correct implementation persists, so a rule that
	// walked back a fixed distance would land on result_carrier here.
	secondCarrier := validInvestigationResult()
	secondCarrier.ResultID = "result_carrier_two"
	secondCarrier.ConfirmedStructure = []contractsv1.ContextFabricConfirmedStructureEntry{{
		Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(contractsv1.ContextFabricSubjectTeam),
		Source: contractsv1.ContextFabricStructureSourceCarried, PriorResultID: "result_origin",
		Provenance: contractsv1.ContextFabricStructureClarificationConfirmed, Disposition: contractsv1.ContextFabricStructureDispositionApplied,
	}}
	store.results["result_carrier_two"] = secondCarrier
	request3 := validInvestigationRequest()
	request3.PriorCandidateReceipts = []BoundSubjectReceipt{{ResultID: "result_carrier_two", ReceiptID: "candr_carrier_two_01"}}

	got3 := engine.resolveCarriedKind(context.Background(), acceptancePrincipal(), request3, nil, ResolvedGraphBinding{Epoch: 0})
	if got3.Outcome != KindCarryHit || got3.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("three-hop resolveCarriedKind() = %#v, want a hit carrying team", got3)
	}
	if got3.SourceResultID != "result_origin" {
		t.Fatalf("three-hop SourceResultID = %q, want %q: the origin must survive EVERY hop, not just the first", got3.SourceResultID, "result_origin")
	}
}
