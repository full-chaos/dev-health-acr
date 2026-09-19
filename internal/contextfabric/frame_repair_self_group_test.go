package contextfabric

import (
	"context"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// selfGroupFrame is a self-group proposal: member kind and group kind are the
// same kind. No question text is carried, only its columns.
func selfGroupFrame(kind SubjectKind) QuestionFrame {
	return QuestionFrame{
		Goals:             []InvestigationGoal{GoalCountOrAggregate},
		SubjectExpression: groupedExpression(kind, kind),
		Temporal:          TemporalIntentCurrent,
	}
}

func selfGroupByRepoFrame() QuestionFrame { return selfGroupFrame(SubjectRepository) }

func selfGroupAppliedLine(kind, source string) map[string]any {
	return map[string]any{
		"outcome": "repaired", "failed_invariant": "", "frame_gate": "passed", "repair_decision": "applied",
		"repair": "self_group_flat_cohort", "repair_invariant": "i6",
		"repair_kind_before": "grouped_members", "repair_kind_after": "discovered_kind",
		"repair_member_kind": kind, "repair_terms_match": "not_evaluated", "repair_attempts": float64(1),
		"group_hint_source": source,
	}
}

type selfGroupCell struct {
	cell     string
	receipt  func(*ModelExecutionReceipt)
	frame    func() QuestionFrame
	repaired bool
	wantLine map[string]any
}

func selfGroupCells() []selfGroupCell {
	return []selfGroupCell{
		{
			cell:     "the flat group hint names the self-group kind: collapsed to a flat cohort",
			receipt:  func(*ModelExecutionReceipt) {},
			frame:    selfGroupByRepoFrame,
			repaired: true,
			wantLine: selfGroupAppliedLine("repository", "model"),
		},
		{
			cell:     "the group kind is named only by the frame: adopted, collapsed, and the line says so",
			receipt:  func(r *ModelExecutionReceipt) { r.GroupKind = "" },
			frame:    selfGroupByRepoFrame,
			repaired: true,
			wantLine: selfGroupAppliedLine("repository", "frame"),
		},
		{
			cell:     "a stated member hint equal to the self-group kind is not a second level",
			receipt:  func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectRepository },
			frame:    selfGroupByRepoFrame,
			repaired: true,
			wantLine: selfGroupAppliedLine("repository", "model"),
		},
		{
			cell:     "a member hint of another kind names a second level: stays refused",
			receipt:  func(r *ModelExecutionReceipt) { r.RequestedSubjectKind = SubjectIncident },
			frame:    selfGroupByRepoFrame,
			wantLine: selfGroupDeclinedLine("declined_two_level_request"),
		},
		{
			cell:     "a member hint the sanitizer dropped may name a second level: stays refused",
			receipt:  func(r *ModelExecutionReceipt) { r.RequestedSubjectKindUnrecognized = true },
			frame:    selfGroupByRepoFrame,
			wantLine: selfGroupDeclinedLine("declined_two_level_request"),
		},
		{
			cell:     "a group hint the sanitizer dropped gives no evidence of a per-kind request: stays refused",
			receipt:  func(r *ModelExecutionReceipt) { r.GroupKind = ""; r.GroupKindUnrecognized = true },
			frame:    selfGroupByRepoFrame,
			wantLine: selfGroupDeclinedLine("declined_group_kind_mismatch"),
		},
		{
			cell:     "a flat group hint of another kind than the frame's stays refused",
			receipt:  func(r *ModelExecutionReceipt) { r.GroupKind = SubjectTeam },
			frame:    selfGroupByRepoFrame,
			wantLine: selfGroupDeclinedLine("declined_group_kind_mismatch"),
		},
		{
			cell:    "a self-group kind no discovery arm serves is never collapsed into",
			receipt: func(r *ModelExecutionReceipt) { r.GroupKind = SubjectDeployment },
			frame:   func() QuestionFrame { return selfGroupFrame(SubjectDeployment) },
			wantLine: func() map[string]any {
				return selfGroupDeclinedLine("declined_group_kind_unservable")
			}(),
		},
		{
			// The fact-alias shape (member kind metric, group repository) is
			// the sibling repair's, never this one's.
			cell:     "a fact-alias member over a servable group routes to the fact-alias repair",
			receipt:  func(*ModelExecutionReceipt) {},
			frame:    groupedMetricByRepoFrame,
			repaired: true,
			wantLine: withGroupHintSource(memberKindAliasAppliedLine(), "model"),
		},
		{
			// The repaired frame meets the SAME validation: a compare goal
			// over a discovered cohort fails I7 after the repair, and the
			// turn is refused with that failure, the line saying a repair ran.
			cell:    "a repaired frame that fails another invariant is refused after the repair",
			receipt: func(*ModelExecutionReceipt) {},
			frame: func() QuestionFrame {
				frame := selfGroupByRepoFrame()
				frame.Goals = []InvestigationGoal{GoalCompare}
				return frame
			},
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i7", "repair_decision": "refused_after_repair",
				"repair": "self_group_flat_cohort", "repair_invariant": "i6", "repair_attempts": float64(1),
				"repair_kind_after": "discovered_kind", "repair_member_kind": "repository",
			},
		},
		{
			cell:    "a genuine two-level grouping is untouched",
			receipt: func(*ModelExecutionReceipt) {},
			frame: func() QuestionFrame {
				frame := selfGroupByRepoFrame()
				frame.SubjectExpression = groupedExpression(SubjectIncident, SubjectRepository)
				return frame
			},
			repaired: false,
			wantLine: map[string]any{"outcome": "valid", "repair_decision": "not_applicable", "repair": "none"},
		},
		{
			cell:    "a direct proposal of the flat shape is refused exactly as before",
			receipt: func(*ModelExecutionReceipt) {},
			frame: func() QuestionFrame {
				frame := selfGroupByRepoFrame()
				frame.SubjectExpression = discoveredExpression(SubjectRepository)
				return frame
			},
			wantLine: map[string]any{
				"outcome": "refused_invalid", "failed_invariant": "i6", "failure_detail": "requested_group_axis_not_expressed",
				"repair_decision": "not_applicable", "repair": "none",
			},
		},
	}
}

// TestTheSelfGroupRepairIsBounded executes every clause of the self-group
// repair's bound through the production interpreter.
func TestTheSelfGroupRepairIsBounded(t *testing.T) {
	for _, testCase := range selfGroupCells() {
		t.Run(testCase.cell, func(t *testing.T) {
			receipt := groupedMetricByRepoReceipt()
			testCase.receipt(&receipt)
			run := interpretForRepair(t, receipt, testCase.frame(), []string{repairAnchorTerm})
			assertRepairLine(t, run.line, testCase.wantLine)
			isRepaired := run.receipt.FrameOutcome == FrameValidationOutcomeRepaired
			if testCase.repaired != isRepaired {
				t.Errorf("receipt frame outcome = %q, repaired want %t", run.receipt.FrameOutcome, testCase.repaired)
			}
			if run.line["repair"] == string(FrameRepairSelfGroupFlatCohort) && testCase.repaired {
				frame := run.outcome.Frame
				if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionDiscoveredKind ||
					frame.SubjectExpression.Discovered == nil || frame.SubjectExpression.Discovered.MemberKind != SubjectRepository ||
					frame.CollapsedGroupAxisMemberKind != SubjectRepository {
					t.Errorf("carried frame = %+v, want discovered_kind repository stamped with its provenance", frame)
				}
				if run.outcome.Gate.Refuses() {
					t.Errorf("gate %s refuses a repaired turn", run.outcome.Gate.Observable())
				}
			}
			if !testCase.repaired && run.line["repair"] == string(FrameRepairSelfGroupFlatCohort) && !run.outcome.Gate.Refuses() {
				t.Errorf("a declined self-group turn must stay refused; gate %s", run.outcome.Gate.Observable())
			}
		})
	}
}

// TestTheSelfGroupRepairOverEveryServableKind crosses every subject kind with
// the self-group shape: a kind a discovery arm serves is collapsed, any other
// is declined as unservable, and none is ever refused after the repair.
func TestTheSelfGroupRepairOverEveryServableKind(t *testing.T) {
	for _, kind := range contractsv1.ContextFabricSubjectKindVocabulary() {
		t.Run(string(kind), func(t *testing.T) {
			receipt := groupedMetricByRepoReceipt()
			receipt.GroupKind = kind
			result := validateProposedFrame(receipt, selfGroupFrame(kind), ShapeSingleSubject, nil)
			_, _, reason := CohortMemberKindFor(discoveredExpression(kind))
			if reason == CohortDiscoverable {
				if result.Outcome != FrameValidationOutcomeRepaired || result.Repair.Decision != FrameRepairApplied ||
					result.Repair.Name != FrameRepairSelfGroupFlatCohort || result.Repair.MemberKind != kind ||
					result.Frame.CollapsedGroupAxisMemberKind != kind {
					t.Fatalf("servable kind %q: result = %+v", kind, result)
				}
				return
			}
			if result.Outcome != FrameValidationOutcomeRefusedInvalid || result.Repair.Decision != FrameRepairDeclinedGroupKindUnservable {
				t.Fatalf("unservable kind %q: outcome %q decision %q, want refused_invalid/declined_group_kind_unservable", kind, result.Outcome, result.Repair.Decision)
			}
		})
	}
}

// TestTheSelfGroupRepairRunsAtMostOnce holds the bound on attempts.
func TestTheSelfGroupRepairRunsAtMostOnce(t *testing.T) {
	t.Parallel()
	for _, attempts := range []int{0, frameRepairBound} {
		result := selfGroupRepairAtTheBound(t, attempts)
		if attempts == 0 {
			if result.Outcome != FrameValidationOutcomeRepaired || result.Repair.Decision != FrameRepairApplied || result.Repair.Attempts != 1 {
				t.Fatalf("control: outcome/decision/attempts = %q/%q/%d, want repaired/applied/1", result.Outcome, result.Repair.Decision, result.Repair.Attempts)
			}
			continue
		}
		if result.Outcome != FrameValidationOutcomeRefusedInvalid || result.Repair.Decision != FrameRepairDeclinedBoundReached || result.Repair.Attempts != frameRepairBound {
			t.Fatalf("at the bound: outcome/decision/attempts = %q/%q/%d, want refused_invalid/declined_bound_reached/%d", result.Outcome, result.Repair.Decision, result.Repair.Attempts, frameRepairBound)
		}
	}
}

func selfGroupRepairAtTheBound(t *testing.T, attempts int) FrameValidationResult {
	t.Helper()
	receipt := groupedMetricByRepoReceipt()
	proposal := selfGroupByRepoFrame()
	base := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
	if base.Failure.Detail != FrameFailureGroupEqualsMember {
		t.Fatalf("fixture defect: the self-group proposal failed with %q", base.Failure.Detail)
	}
	base.Repair.Attempts = attempts
	return repairSelfGroupFlatCohort(receipt, proposal, ShapeSingleSubject, nil, base)
}

// TestTheSelfGroupRepairPassesEveryOtherFailureThrough holds that the repair
// never touches a failure other than the self-group one: a different I6
// detail on a grouped frame, and a different invariant on a flat frame.
func TestTheSelfGroupRepairPassesEveryOtherFailureThrough(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*QuestionFrame){
		"another I6 detail on a grouped frame": func(f *QuestionFrame) { f.SubjectExpression = groupedExpression("", SubjectRepository) },
		"another invariant": func(f *QuestionFrame) {
			f.Goals = []InvestigationGoal{}
			f.SubjectExpression = discoveredExpression(SubjectRepository)
		},
	} {
		t.Run(name, func(t *testing.T) {
			receipt := groupedMetricByRepoReceipt()
			proposal := selfGroupByRepoFrame()
			mutate(&proposal)
			base := validateAgainstInterpretation(receipt, proposal, ShapeSingleSubject)
			if base.Outcome != FrameValidationOutcomeRefusedInvalid || base.Failure.Detail == FrameFailureGroupEqualsMember {
				t.Fatalf("fixture defect: outcome %q detail %q", base.Outcome, base.Failure.Detail)
			}
			result := repairSelfGroupFlatCohort(receipt, proposal, ShapeSingleSubject, nil, base)
			if result.Repair.Decision != FrameRepairNotApplicable || result.Repair.Name != "" || result.Outcome != base.Outcome {
				t.Fatalf("result = %+v, want the failure untouched and not_applicable", result)
			}
		})
	}
}

// TestTheSelfGroupTurnIsServedAsAFlatRepositoryCohort drives Investigate end
// to end: retrieval receives the repaired discovered_kind frame and the turn
// is refused at neither the frame seam nor the plan seam.
func TestTheSelfGroupTurnIsServedAsAFlatRepositoryCohort(t *testing.T) {
	engine, graph := newSelfGroupRepairEngine(t, groupedMetricByRepoReceipt(), selfGroupByRepoFrame())
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_self_group_repair_01"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationComplete {
		t.Fatalf("status = %q (basis %q), want complete", result.Status, result.RefusalBasis)
	}
	if len(graph.frames) == 0 {
		t.Fatal("retrieval was never called for the repaired turn")
	}
	for call, frame := range graph.frames {
		if frame == nil || frame.SubjectExpression.Kind != SubjectExpressionDiscoveredKind ||
			frame.SubjectExpression.Discovered == nil || frame.SubjectExpression.Discovered.MemberKind != SubjectRepository {
			t.Fatalf("retrieval call %d received %+v, want a discovered_kind repository cohort", call, frame)
		}
	}
}

// TestATwoLevelSelfGroupTurnIsNotServed holds the control: the same engine
// path with a second level requested refuses the turn.
func TestATwoLevelSelfGroupTurnIsNotServed(t *testing.T) {
	receipt := groupedMetricByRepoReceipt()
	receipt.RequestedSubjectKind = SubjectIncident
	engine, graph := newSelfGroupRepairEngine(t, receipt, selfGroupByRepoFrame())
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestID = "request_self_group_repair_02"
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_repair"}, request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status == InvestigationComplete || result.RefusalBasis != contractsv1.ContextFabricRefusalBasisFrameInvariantViolated {
		t.Fatalf("status %q basis %q, want a frame_invariant_violated refusal", result.Status, result.RefusalBasis)
	}
	if len(graph.frames) != 0 {
		t.Fatalf("retrieval ran %d time(s) for a refused frame", len(graph.frames))
	}
}

func newSelfGroupRepairEngine(t *testing.T, receipt ModelExecutionReceipt, proposal QuestionFrame) (*Engine, *retrievalRecordingGraph) {
	t.Helper()
	logs := captureEngineLogger(t)
	receipt.QuestionFrame = &proposal
	graph := &retrievalRecordingGraph{graphReaderStub: graphReaderStub{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context: GraphContext{
			Cohort: countingCohort(SubjectRepository, 3), Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{},
			FactRequirements: []FactRequirement{}, EvidenceRefIDs: []string{},
			Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: RuntimeQuestionInterpreter{
			Runtime:        fakeModelRuntime{interpreted: repairInterpretation([]string{repairAnchorTerm}), receipt: receipt},
			Sink:           &fakeReceiptSink{},
			FrameTelemetry: logs.telemetry,
			Requirements:   registryDeriver{},
		},
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Assessed.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts: []ClaimedFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Assessed.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results:      &staticResultStore{results: map[string]InvestigationResult{}},
		Telemetry:    &recordingTelemetry{},
		Requirements: registryDeriver{},
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(600, 0).UTC() },
		NewResultID:    func() string { return "result_self_group_repair_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine, graph
}

// TestTheSelfGroupRepairNeverReadsAFrameWithoutAGroupedExpression holds that
// the repair's own guard, not the validator's failure alone, decides that the
// proposal is grouped: a self-group failure paired with a proposal that has no
// grouped expression is passed through, never dereferenced.
func TestTheSelfGroupRepairNeverReadsAFrameWithoutAGroupedExpression(t *testing.T) {
	t.Parallel()
	receipt := groupedMetricByRepoReceipt()
	proposal := selfGroupByRepoFrame()
	proposal.SubjectExpression = discoveredExpression(SubjectRepository)
	failed := FrameValidationResult{
		Outcome: FrameValidationOutcomeRefusedInvalid,
		Failure: FrameValidationFailure{Invariant: FrameInvariantI6, Phase: FrameValidationPhaseA1, Detail: FrameFailureGroupEqualsMember},
	}
	result := repairSelfGroupFlatCohort(receipt, proposal, ShapeSingleSubject, nil, failed)
	if result.Repair.Decision != FrameRepairNotApplicable || result.Outcome != FrameValidationOutcomeRefusedInvalid {
		t.Fatalf("result = %+v, want the failure untouched and not_applicable", result)
	}
}
