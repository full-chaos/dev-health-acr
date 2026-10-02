package contextfabric

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func assertInterpretedTerminalVersions(t *testing.T, name string, versions VersionSet) {
	t.Helper()
	if versions.InterpretationVersion != "schema-v1" || versions.ModelIdentity != "test-provider/test-model" {
		t.Errorf("%s: Versions = %#v, want interpretation_version and model_identity stamped from the interpretation receipt", name, versions)
	}
	if versions.SynthesisVersion != SynthesisVersionNotSynthesized {
		t.Errorf("%s: synthesis_version = %q, want %q", name, versions.SynthesisVersion, SynthesisVersionNotSynthesized)
	}
}

func interpretedStamp() QuestionFamilyOutcome {
	return QuestionFamilyOutcome{Interpretation: interpretationStampOf(validModelReceiptFixture(ModelOperationInterpret))}
}

func TestTerminalVersionsReadUnwiredOnlyWhenNoInterpretCallRan(t *testing.T) {
	t.Parallel()
	engine := buildTerminalEngine(t, &acceptanceGraphReader{resolution: ambiguousResolution("Which one?"), context: emptyGraphContext()}, nil)

	bare := engine.terminalVersions(context.Background())
	if bare.InterpretationVersion != "unwired" || bare.SynthesisVersion != "unwired" || bare.ModelIdentity != "unwired" {
		t.Fatalf("Versions = %#v, want unwired when no interpret call ran", bare)
	}
	stamped := engine.terminalVersions(withInterpretationStamp(context.Background(), interpretedStamp().Interpretation))
	assertInterpretedTerminalVersions(t, "stamped context", stamped)
}

func TestTerminalNoMatchStampsVersionsFromTheInterpretationReceipt(t *testing.T) {
	t.Parallel()
	runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), receipt: acceptanceReceipt()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, RuntimeQuestionInterpreter{Runtime: runtime}, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("Status = %q, want no_match", result.Status)
	}
	assertInterpretedTerminalVersions(t, "no_match", result.Versions)
}

func TestWindowGatedTerminalStampsVersionsFromTheInterpretationReceipt(t *testing.T) {
	t.Parallel()
	interpreter := &countingInterpreter{interpretation: bootstrapInterpretation(), family: interpretedStamp()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.WindowClarification == nil {
		t.Fatalf("result = %#v, want the window-gated terminal", result)
	}
	assertInterpretedTerminalVersions(t, "window gated", result.Versions)
}

func TestInterpretedTimeBoundRefusalStampsVersionsFromTheInterpretationReceipt(t *testing.T) {
	t.Parallel()
	zero := TimeContext{Axis: TemporalValidTime}
	question := bootstrapInterpretation()
	question.TimeContext = zero
	interpreter := &countingInterpreter{interpretation: question, family: interpretedStamp()}
	graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
	engine := buildWindowGateEngine(t, interpreter, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch || graph.resolveCalls != 0 {
		t.Fatalf("Status = %q resolve=%d, want the time-bound refusal before any resolve", result.Status, graph.resolveCalls)
	}
	assertInterpretedTerminalVersions(t, "time bound refusal", result.Versions)
}
