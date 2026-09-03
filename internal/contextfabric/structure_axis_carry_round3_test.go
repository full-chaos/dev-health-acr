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
