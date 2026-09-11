package contextfabric

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE CARRIED PASS. A candidate-narrowing re-finalization serves a document
// that CARRIES the earlier pass's assembled-result row for a read requirement
// instead of evaluating it again (one row per identity). It used to emit no
// cover line for that requirement at all, so the only line on the trace was
// the earlier pass's -- and emit marked it served, describing a document
// nobody received. An adversarial round reproduced it through
// Engine.Investigate: `Pass:0 Served:true` for a candidate-rescued answer.
//
// Now every finalization speaks for every served read requirement: the pass
// that carries the row re-states the decision it carries, under its own pass
// number, with evaluated_pass naming the pass that actually evaluated it.
// Both re-finalization sites are pinned here, through Engine.Investigate, on
// the production JSON sink at Info:
//
//   - the candidate narrowing after the FIRST pass (requirement_outcomes.go,
//     reached from stage 3's first planCandidateNarrowing), and
//   - the candidate narrowing after the RETRY (the second planCandidateNarrowing).

const carryRequirement = "state/subject/team"

func coverLinesFor(t *testing.T, buf *bytes.Buffer, requirement string) []map[string]any {
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
		if line["requirement"] == requirement {
			lines = append(lines, line)
		}
	}
	return lines
}

// passShape reads pass / evaluated_pass / served off an emitted line as the
// compact "pass:evaluated:served" a reader would reconstruct from the trace.
func passShape(line map[string]any) string {
	pass, _ := line["pass"].(float64)
	evaluated, okEvaluated := line["evaluated_pass"].(float64)
	served, okServed := line["served"].(bool)
	if !okEvaluated || !okServed {
		return "missing evaluated_pass or served"
	}
	return fmtShape(int(pass), int(evaluated), served)
}

func fmtShape(pass, evaluated int, served bool) string {
	b, _ := json.Marshal([]any{pass, evaluated, served})
	return string(b)
}

// firstEvaluatorRow is the assembled-result row the read evaluator wrote for
// the identity: the FIRST one, because the evaluator appends at the pass it
// evaluates and a later candidate-reduction row, if any, is appended after it.
func firstEvaluatorRow(t *testing.T, result InvestigationResult, requirement string) RequirementOutcomeRow {
	t.Helper()
	for _, row := range result.Completeness.Outcomes {
		if row.Requirement == requirement && row.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
			return row
		}
	}
	t.Fatalf("the served document carries no assembled-result row for %q", requirement)
	return RequirementOutcomeRow{}
}

func TestACandidateRescuedAnswerMarksItsOwnPassServedAndSaysItCarriedTheRow(t *testing.T) {
	t.Parallel()
	calls := 0
	engine := outcomeAssemblySingleSubjectEngineWithDrivers(t, 18, 12, 5, budgetStageOptions(30, 0), &calls, &recordingTelemetry{})
	var buf bytes.Buffer
	engine.telemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	frame := teamStateFrame(t)
	engine.interpreter = familyInterpreter{
		interpreted: InterpretedQuestion{Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}},
		outcome:     QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
	}
	engine.requirements = registryDeriver{capabilities: []FactCapability{stateCapability("health", FactHealth)}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("synthesizer called %d times, want 1 -- this pin is the candidate-only rescue, no retry", calls)
	}

	lines := coverLinesFor(t, &buf, carryRequirement)
	if len(lines) != 2 {
		t.Fatalf("%d cover lines for %q, want 2 (the evaluating pass and the re-finalizing pass):\n%s", len(lines), carryRequirement, buf.String())
	}
	// [pass, evaluated_pass, served]: the discarded first pass evaluated;
	// the served second pass CARRIED the first pass's row.
	if got, want := passShape(lines[0]), fmtShape(answerPassFirst, answerPassFirst, false); got != want {
		t.Fatalf("first line = %s, want %s", got, want)
	}
	if got, want := passShape(lines[1]), fmtShape(answerPassSecond, answerPassFirst, true); got != want {
		t.Fatalf("served line = %s, want %s -- the served document is the re-finalized one, and it carried pass %d's row", got, want, answerPassFirst)
	}
	// A carried line re-states the SAME decision: every measured field equals
	// the evaluating pass's, and the served line describes the row the caller
	// actually received.
	for _, field := range []string{"threshold", "observed_kinds", "served_kinds", "observed_cover", "served_cover", "collapsed_observations", "tainted_observations", "declared", "meets_threshold", "declared_raised_to_standard"} {
		if lines[0][field] != lines[1][field] {
			t.Fatalf("%s = %v on the evaluating pass but %v on the carrying pass; a carried decision must be re-stated unchanged", field, lines[0][field], lines[1][field])
		}
	}
	row := firstEvaluatorRow(t, result, carryRequirement)
	if lines[1]["served_cover"] != float64(row.Served) || lines[1]["declared"] != float64(row.Declared) {
		t.Fatalf("served line served_cover/declared = %v/%v, but the served document's row reads %d/%d",
			lines[1]["served_cover"], lines[1]["declared"], row.Served, row.Declared)
	}
}

func TestARetryRescuedByTheReductionMarksTheThirdPassServedAndNamesTheRetryAsItsEvaluator(t *testing.T) {
	t.Parallel()
	calls := 0
	// The retry halves the cohort and still does not fit; the candidate cut
	// then fits it (see TestARetryThatIsRescuedByTheReductionPlansNoRefusal).
	engine := outcomeCohortEngineWithCandidates(t, budgetStageCohort(6), 2, 6, budgetStageOptions(12, time.Second), &calls, &recordingTelemetry{})
	var buf bytes.Buffer
	engine.telemetry = NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	frame := teamStateFrame(t)
	engine.interpreter = familyInterpreter{
		interpreted: InterpretedQuestion{Shape: ShapeDiscoveredCohort, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{{Kind: FactStatus}}},
		outcome:     QuestionFamilyOutcome{Frame: &frame, Family: QuestionFamilyUnclassified, Source: QuestionFamilySourceModel},
	}
	engine.requirements = registryDeriver{capabilities: []FactCapability{stateCapability("health", FactHealth)}}

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, validInvestigationRequestWithConfirmedWindow())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesizer called %d times, want 2 -- one retry, then the candidate cut", calls)
	}

	lines := coverLinesFor(t, &buf, carryRequirement)
	if len(lines) != 3 {
		t.Fatalf("%d cover lines for %q, want 3 (first pass, retry, re-finalized retry):\n%s", len(lines), carryRequirement, buf.String())
	}
	want := []string{
		fmtShape(answerPassFirst, answerPassFirst, false),   // discarded: evaluated the first document
		fmtShape(answerPassSecond, answerPassSecond, false), // discarded: the retry evaluated its own document
		fmtShape(answerPassThird, answerPassSecond, true),   // served: carried the RETRY's row, not the first pass's
	}
	for i := range want {
		if got := passShape(lines[i]); got != want[i] {
			t.Fatalf("line %d = %s, want %s", i, got, want[i])
		}
	}
	row := firstEvaluatorRow(t, result, carryRequirement)
	if lines[2]["served_cover"] != float64(row.Served) || lines[2]["declared"] != float64(row.Declared) {
		t.Fatalf("served line served_cover/declared = %v/%v, but the served document's row reads %d/%d",
			lines[2]["served_cover"], lines[2]["declared"], row.Served, row.Declared)
	}
}
