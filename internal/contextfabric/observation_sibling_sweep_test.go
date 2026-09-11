package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestTheSiblingSweepIsExecuted is the CLASS SWEEP for each guard fix in this
// change, executed rather than argued: every sibling of a fixed site that a
// reader might dismiss by reasoning gets a cell here, run in one pass, with
// its outcome printed and asserted.
//
//   - DISTINCT KINDS. The construction bound and the cover line's builder were
//     fixed to count distinct kinds. The producer of a requirement's kind list
//     (the obligation seed) and the contract that validates it are the
//     remaining siblings: they must never hand the counters a kind twice.
//   - PUBLISHED ROW <=> COVER LINE. The zero-observation fix made an evaluated
//     zero emit its line. Every other branch of the evaluator either publishes
//     a row AND its line, or publishes neither; none may publish one without
//     the other.
//   - ONE PUBLICATION PATH. The per-pass fix moved the cover line out of
//     finalizeResult into emit. finalizeResult must publish nothing itself.
//
// NOT PARALLEL: three branches below write a WARN through slog.Default(),
// which is process-global, so this test owns the default logger for its run
// (the rule captureDefaultLogger states).
func TestTheSiblingSweepIsExecuted(t *testing.T) {
	logs := captureDefaultJSONLogger(t)

	type row struct{ sibling, cell, got, want string }
	var table []row
	check := func(sibling, cell, got, want string) {
		table = append(table, row{sibling, cell, got, want})
		if got != want {
			t.Errorf("%s/%s = %s, want %s", sibling, cell, got, want)
		}
	}

	// ------------------------------------------------------------ distinct kinds
	health := contractsv1.ContextFabricFactHealth
	twice := GenerateObligationSeed([]FactCapability{
		stateCapability("health", FactHealth),
		stateCapability("health again", FactHealth),
	})
	check("GenerateObligationSeed", "one kind declared by two capabilities",
		fmt.Sprintf("kinds=%v", twice.KindsFor(ObligationState, SubjectTeam)), "kinds=[health]")

	valid := readRequirement(CompletionQuantifierAtLeastOne)
	check("ContextFabricPlanRequirement.Validate", "canonical requirement", verdictOf(valid.Validate()), "accepted")
	duplicated := readRequirement(CompletionQuantifierAtLeastOne)
	duplicated.FactKinds = []FactKind{health, health}
	err := duplicated.Validate()
	got := verdictOf(err)
	if err != nil && !strings.Contains(err.Error(), "twice") {
		got = "refused for another reason: " + err.Error()
	}
	check("ContextFabricPlanRequirement.Validate", "the same fact kind twice", got, "refused")

	// ------------------------------------------------ published row <=> cover line
	published := func(requirement contractsv1.ContextFabricPlanRequirement, coverage Coverage, populations readPopulationEvidence) string {
		rows, events, _ := appendReadRequirementEvaluationsWithCover(nil,
			[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage, populations)
		assembled := 0
		for _, r := range rows {
			if r.Requirement == requirement.Requirement && r.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
				assembled++
			}
		}
		lines := 0
		for _, e := range events {
			if e.Requirement == requirement.Requirement {
				lines++
			}
		}
		return fmt.Sprintf("rows=%d lines=%d", assembled, lines)
	}
	single := readRequirement(CompletionQuantifierAtLeastOne)
	workload := contractsv1.ContextFabricFactWorkload
	check("readRequirementOutcomeRow", "zero observed (evaluated zero)",
		published(single, factCoverage(), readPopulationEvidence{}), "rows=1 lines=1")
	check("readRequirementOutcomeRow", "kinds observed and served",
		published(single, factCoverage(health, SourceAvailable), readPopulationEvidence{}), "rows=1 lines=1")
	check("readRequirementOutcomeRow", "every declared kind pruned",
		published(single, factCoverage(health, SourcePruned, workload, SourcePruned), readPopulationEvidence{}), "rows=0 lines=0")
	const undeclared = contractsv1.ContextFabricCoverageDetailCode("fact_invented_by_a_future_producer")
	check("readRequirementOutcomeRow", "undeclared cause code",
		published(single, codedCoverage(undeclared, health, health, SourceUnavailable), readPopulationEvidence{}), "rows=0 lines=0")

	flow := contractsv1.ContextFabricFactFlow
	operand := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)
	operandCoverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	check("readRequirementOutcomeRow", "distributive, no population evidence threaded",
		published(operand, operandCoverage, readPopulationEvidence{}), "rows=0 lines=0")
	check("readRequirementOutcomeRow", "distributive, evidence present but no population owner",
		published(operand, operandCoverage, readPopulationEvidence{Present: true}), "rows=0 lines=0")
	alpha := teamRef("team_alpha")
	owned := readPopulationEvidenceFrom(namedOperandFrame(SubjectTeam, SubjectTeam),
		InvestigationResult{SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []SubjectRef{alpha}}, Coverage: operandCoverage},
		AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{operand}},
		factsFor(alpha, kindList(flow, health)), teamAssignment())
	check("readRequirementOutcomeRow", "distributive with its population",
		published(operand, operandCoverage, owned), "rows=1 lines=1")

	// ---------------------------------------------------- one publication path
	sink := &recordingTelemetry{}
	engine := &Engine{requirements: registryDeriver{}, telemetry: sink}
	frame := teamStateFrame(t)
	pending := &assemblyTelemetry{}
	engine.finalizeResult(context.Background(), storage.Principal{OrgID: "org_sibling"}, InvestigationResult{
		Status: InvestigationComplete, ResultID: "result_sibling", Coverage: factCoverage(health, SourceAvailable),
	}, AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{single}}, &frame, CanonicalFactBundle{}, pending, answerPassFirst)
	before := len(sink.readRequirementObservationCovers)
	engine.emit(context.Background(), storage.Principal{OrgID: "org_sibling"}, *pending)
	check("finalizeResult then emit", "cover lines published by finalizeResult / by emit",
		fmt.Sprintf("%d / %d", before, len(sink.readRequirementObservationCovers)-before), "0 / 1")

	t.Logf("%-40s %-58s %-14s %s", "SIBLING", "CELL", "GOT", "WANT")
	for _, r := range table {
		t.Logf("%-40s %-58s %-14s %s", r.sibling, r.cell, r.got, r.want)
	}
	t.Logf("SIBLING CELLS EXECUTED: %d", len(table))
	if len(table) < 11 {
		t.Fatalf("only %d sibling cells executed", len(table))
	}
	// The WARN-logging branches must still say why they dropped the row.
	for _, line := range []string{
		"context fabric read requirement dropped for an undeclared coverage code",
		"context fabric distributive read requirement reached the evaluator with no population evidence",
		"context fabric distributive read requirement has no population owner",
	} {
		if !strings.Contains(logs.String(), line) {
			t.Errorf("a no-row branch dropped its row without its WARN %q", line)
		}
	}
}

func verdictOf(err error) string {
	if err != nil {
		return "refused"
	}
	return "accepted"
}
