package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE EXIT DECIDES `served`. The cover lines used to be published from emit,
// which runs before the final budget assertion, validation and persistence --
// so an answer refused after evaluation logged served=true for a document the
// caller never received (an adversarial round executed it: a final budget
// refusal with a `Pass:0 Served:true` line). They are now published once, at
// Investigate's exit: served only when the evaluated answer is returned, and
// answer_withheld on every line otherwise. These pins drive each kind of exit
// through Engine.Investigate on the production JSON sink at Info.

func jsonSink() (*bytes.Buffer, EngineTelemetry) {
	var buf bytes.Buffer
	return &buf, NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

func allCoverLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var line map[string]any
		if json.Unmarshal(raw, &line) != nil || line["msg"] != "context fabric observation cover" {
			continue
		}
		if line["level"] != "INFO" {
			t.Fatalf("cover line at level %v, want INFO", line["level"])
		}
		lines = append(lines, line)
	}
	return lines
}

// exitDeriver declares ONE single-subject read requirement over health at
// team, so every finalization evaluates it and emits its line.
type exitDeriver struct{}

func (exitDeriver) DeriveRequirements(QuestionFrame) []DerivedRequirement {
	return []DerivedRequirement{{
		RequirementCoordinate: RequirementCoordinate{Obligation: ObligationState, Role: SubjectRoleSubject, Subject: SubjectTeam},
		Kind:                  ObligationKindRead,
		FactKinds:             []FactKind{FactHealth},
		Scope:                 CompletionScopeSingleSubject,
		Quantifier:            CompletionQuantifierAtLeastOne,
	}}
}

// finalAssertionEngine is the reviewer's shape: a byte ceiling calibrated
// between the pre-label document stage 3 measures and the final document the
// last assertion measures, so the answer survives stage 3 and is refused at
// the very end, after emit.
func finalAssertionEngine(t *testing.T, maxBytes int64, telemetry EngineTelemetry) *Engine {
	t.Helper()
	options := budgetStageOptions(500, 0)
	options.MaxSerializedBytes = maxBytes
	calls := 0
	engine := budgetStageEngine(t, budgetStageCohort(6), 2, options, &calls)
	engine.telemetry = telemetry
	engine.graph.(*capturingGraphReader).context.Coverage.Sources = []SourceObservation{{Source: "graph", State: SourceAvailable}}
	frame := teamStateFrame(t)
	engine.interpreter = familyInterpreter{
		interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}},
		outcome:     QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
	}
	engine.requirements = exitDeriver{}
	original := engine.synthesizer
	engine.synthesizer = synthesizerFunc(func(ctx context.Context, principal storage.Principal, input SynthesisInput) (InvestigationResult, error) {
		result, err := original.Synthesize(ctx, principal, input)
		result.EvidenceRefIDs = []string{"evidence_status_0001"}
		result.Coverage.Sources = []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}
		return result, err
	})
	return engine
}

func TestAnAnswerRefusedAtTheFinalBudgetAssertionMarksNoLineServed(t *testing.T) {
	t.Parallel()
	// CONTROL: no byte ceiling -- the answer is served, and its line says so.
	controlBuf, controlSink := jsonSink()
	served, err := finalAssertionEngine(t, 0, controlSink).Investigate(context.Background(), storage.Principal{OrgID: "org_exit"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("control Investigate() error = %v", err)
	}
	control := allCoverLines(t, controlBuf)
	if len(control) == 0 {
		t.Fatal("control emitted no cover line; the pin would be vacuous")
	}
	last := control[len(control)-1]
	if last["served"] != true || last["answer_withheld"] != false {
		t.Fatalf("control: served=%v answer_withheld=%v on the served answer's line, want true/false", last["served"], last["answer_withheld"])
	}

	// Calibrate a ceiling the pre-label document fits and the final one does not.
	post, err := contractsv1.MeasureContextFabricResponse(served)
	if err != nil {
		t.Fatalf("measure served: %v", err)
	}
	pre, err := contractsv1.MeasureContextFabricResponse(stripPostMeasurementLabels(served))
	if err != nil {
		t.Fatalf("measure pre-label: %v", err)
	}
	if post.Bytes <= pre.Bytes+1 {
		t.Fatalf("calibration left no byte interval: pre=%d post=%d", pre.Bytes, post.Bytes)
	}
	budget := pre.Bytes + (post.Bytes-pre.Bytes)/2

	buf, sink := jsonSink()
	_, err = finalAssertionEngine(t, budget, sink).Investigate(context.Background(), storage.Principal{OrgID: "org_exit"}, validInvestigationRequestWithConfirmedWindow())
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want the FINAL AnswerBudgetRefusal at a %d-byte ceiling", err, budget)
	}
	lines := allCoverLines(t, buf)
	if len(lines) == 0 {
		t.Fatal("a refused answer published no cover line; its evaluation must still be visible")
	}
	for _, line := range lines {
		if line["served"] != false || line["answer_withheld"] != true {
			t.Fatalf("line pass=%v served=%v answer_withheld=%v after a final budget refusal, want served=false answer_withheld=true",
				line["pass"], line["served"], line["answer_withheld"])
		}
	}
}

func TestAStage3RefusalPublishesEveryEvaluatedPassWithheld(t *testing.T) {
	t.Parallel()
	calls := 0
	// 20 claims per member exceeds a 4-item budget even for one member, so the
	// retry cannot rescue it and stage 3 refuses (see
	// TestStage3RefusesWithAnExplanationWhenTheRetryStillDoesNotFit).
	engine := budgetStageEngine(t, budgetStageCohort(4), 20, budgetStageOptions(4, time.Second), &calls)
	buf, sink := jsonSink()
	engine.telemetry = sink
	frame := teamStateFrame(t)
	engine.interpreter = familyInterpreter{
		interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}},
		outcome:     QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
	}
	engine.requirements = exitDeriver{}

	_, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_exit"}, validInvestigationRequestWithConfirmedWindow())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want the stage-3 refusal", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- a retry was attempted and still refused", calls)
	}
	lines := allCoverLines(t, buf)
	passes := map[float64]bool{}
	for _, line := range lines {
		passes[line["pass"].(float64)] = true
		if line["served"] != false || line["answer_withheld"] != true {
			t.Fatalf("line pass=%v served=%v answer_withheld=%v after a stage-3 refusal, want served=false answer_withheld=true",
				line["pass"], line["served"], line["answer_withheld"])
		}
	}
	if !passes[float64(answerPassFirst)] || !passes[float64(answerPassSecond)] {
		t.Fatalf("passes on the trace = %v, want both the first pass and the retry -- a refusal must still show every pass it evaluated", passes)
	}
}

func reusedCandidateWithReadRequirement() (SubjectRef, InvestigationResult) {
	project, candidate := reusableCandidate()
	candidate.AnswerPlan = &AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{{
		Requirement: "state/subject/project", Obligation: string(ObligationState), Role: string(SubjectRoleSubject),
		Subject: SubjectProject, Kind: string(ObligationKindRead), FactKinds: []FactKind{FactHealth},
		Scope: string(CompletionScopeSingleSubject), Quantifier: string(CompletionQuantifierAtLeastOne),
	}}}
	candidate.Coverage = Coverage{Sources: []SourceObservation{{Source: "canonical_fact:health", State: SourceAvailable}}}
	candidate.Completeness.Outcomes = []RequirementOutcomeRow{
		{Stage: contractsv1.ContextFabricOutcomeStagePlanning, Requirement: "state/subject/project", Obligation: string(ObligationState), Outcome: contractsv1.ContextFabricRequirementSatisfied},
		{Stage: contractsv1.ContextFabricOutcomeStageAssembledResult, Requirement: "state/subject/project", Obligation: string(ObligationState), Outcome: contractsv1.ContextFabricRequirementSatisfied, Served: 1, Declared: 1},
	}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	return project, candidate
}

func TestAReusedAnswerStatesTheStoredDecisionItServes(t *testing.T) {
	t.Parallel()
	project, candidate := reusedCandidateWithReadRequirement()
	buf, sink := jsonSink()
	result, err := buildReuseEngineWithBudget(t, project, candidate, 500, sink).Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("reuse Investigate() error = %v", err)
	}
	if !result.Reused {
		t.Fatal("premise: result.Reused = false")
	}
	lines := allCoverLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("%d cover lines for a reused answer with one read requirement, want 1:\n%s", len(lines), buf.String())
	}
	line := lines[0]
	for field, want := range map[string]any{
		"requirement": "state/subject/project", "reused": true, "served": true, "answer_withheld": false,
		"row_withheld": "none", "pass": float64(0), "evaluated_pass": float64(0),
		"served_cover": float64(1), "declared": float64(1),
	} {
		if line[field] != want {
			t.Fatalf("%s = %v on the reused answer's line, want %v", field, line[field], want)
		}
	}
}

func TestAReusedAnswerRefusedByTheCurrentBudgetMarksItsLinesWithheld(t *testing.T) {
	t.Parallel()
	project, candidate := reusedCandidateWithReadRequirement()
	buf, sink := jsonSink()
	// Squeezed on the BYTE axis, the stored row left as built -- the shape
	// TestReuseHitIsRevalidatedAgainstTheCurrentBudget uses, because changing
	// the row instead makes the reuse gate miss.
	stored, err := contractsv1.MeasureContextFabricResponse(candidate)
	if err != nil {
		t.Fatalf("measure stored row: %v", err)
	}
	_, err = buildReuseEngineWithByteBudget(t, project, candidate, stored.Bytes/2, sink).Investigate(context.Background(), reusePrincipal(), validInvestigationRequest())
	if !errors.Is(err, ErrAnswerExceedsBudget) {
		t.Fatalf("error = %v, want the reuse re-validation refusal", err)
	}
	lines := allCoverLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("%d cover lines for a refused reuse, want 1:\n%s", len(lines), buf.String())
	}
	if lines[0]["reused"] != true || lines[0]["served"] != false || lines[0]["answer_withheld"] != true {
		t.Fatalf("refused reuse line reused=%v served=%v answer_withheld=%v, want true/false/true",
			lines[0]["reused"], lines[0]["served"], lines[0]["answer_withheld"])
	}
}
