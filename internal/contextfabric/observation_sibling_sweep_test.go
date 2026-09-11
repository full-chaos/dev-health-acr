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
//   - EVERY EVALUATION STATES ITSELF. The zero-observation fix made an
//     evaluated zero emit its line, and round 3 found the same silence on the
//     branches that publish NO row. Every branch of the evaluator now emits
//     exactly one line, and a branch that withholds its row names why on that
//     line (row_withheld); a row is published iff the reason is `none`.
//   - ONE PUBLICATION PATH, AT THE EXIT. Neither finalizeResult nor emit
//     publishes a cover line; Investigate's exit does, once, when it knows
//     whether the answer was returned.
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
	seenReasons := map[RowWithheldReason]bool{}
	published := func(requirement contractsv1.ContextFabricPlanRequirement, coverage Coverage, populations readPopulationEvidence) string {
		rows, events, _ := appendReadRequirementEvaluationsWithCover(nil,
			[]contractsv1.ContextFabricPlanRequirement{requirement}, coverage, populations)
		assembled := 0
		for _, r := range rows {
			if r.Requirement == requirement.Requirement && r.Stage == contractsv1.ContextFabricOutcomeStageAssembledResult {
				assembled++
			}
		}
		lines, reason := 0, ""
		for _, e := range events {
			if e.Requirement == requirement.Requirement {
				lines++
				reason = string(e.RowWithheld)
				seenReasons[e.RowWithheld] = true
			}
		}
		return fmt.Sprintf("rows=%d lines=%d withheld=%s", assembled, lines, reason)
	}
	single := readRequirement(CompletionQuantifierAtLeastOne)
	workload := contractsv1.ContextFabricFactWorkload
	check("readRequirementOutcomeRow", "zero observed (evaluated zero)",
		published(single, factCoverage(), readPopulationEvidence{}), "rows=1 lines=1 withheld=none")
	check("readRequirementOutcomeRow", "kinds observed and served",
		published(single, factCoverage(health, SourceAvailable), readPopulationEvidence{}), "rows=1 lines=1 withheld=none")
	check("readRequirementOutcomeRow", "every declared kind pruned",
		published(single, factCoverage(health, SourcePruned, workload, SourcePruned), readPopulationEvidence{}), "rows=0 lines=1 withheld=all_pruned")
	const undeclared = contractsv1.ContextFabricCoverageDetailCode("fact_invented_by_a_future_producer")
	check("readRequirementOutcomeRow", "undeclared cause code",
		published(single, codedCoverage(undeclared, health, health, SourceUnavailable), readPopulationEvidence{}), "rows=0 lines=1 withheld=undeclared_cause")

	flow := contractsv1.ContextFabricFactFlow
	operand := operandRequirement(SubjectTeam, CompletionQuantifierCorroborated, flow, health)
	operandCoverage := factCoverage(flow, SourceAvailable, health, SourceAvailable)
	check("readRequirementOutcomeRow", "distributive, no population evidence threaded",
		published(operand, operandCoverage, readPopulationEvidence{}), "rows=0 lines=1 withheld=no_population_evidence")
	check("readRequirementOutcomeRow", "distributive, evidence present but no population owner",
		published(operand, operandCoverage, readPopulationEvidence{Present: true}), "rows=0 lines=1 withheld=no_population_owner")
	alpha := teamRef("team_alpha")
	owned := readPopulationEvidenceFrom(namedOperandFrame(SubjectTeam, SubjectTeam),
		InvestigationResult{SubjectResolution: contractsv1.ContextFabricSubjectResolution{Committed: []SubjectRef{alpha}}, Coverage: operandCoverage},
		AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{operand}},
		factsFor(alpha, kindList(flow, health)), teamAssignment())
	check("readRequirementOutcomeRow", "distributive with its population",
		published(operand, operandCoverage, owned), "rows=1 lines=1 withheld=none")

	// ---------------------------------------------------- one publication path
	sink := &recordingTelemetry{}
	engine := &Engine{requirements: registryDeriver{}, telemetry: sink}
	frame := teamStateFrame(t)
	pending := &assemblyTelemetry{}
	engine.finalizeResult(context.Background(), storage.Principal{OrgID: "org_sibling"}, InvestigationResult{
		Status: InvestigationComplete, ResultID: "result_sibling", Coverage: factCoverage(health, SourceAvailable),
	}, AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{single}}, &frame, CanonicalFactBundle{}, pending, answerPassFirst)
	afterFinalize := len(sink.readRequirementObservationCovers)
	engine.emit(context.Background(), storage.Principal{OrgID: "org_sibling"}, *pending)
	afterEmit := len(sink.readRequirementObservationCovers)
	engine.publishObservationCover(context.Background(), storage.Principal{OrgID: "org_sibling"}, pending.ObservationCover, true)
	check("finalizeResult, emit, exit publish", "cover lines published by finalizeResult / emit / the exit",
		fmt.Sprintf("%d / %d / %d", afterFinalize, afterEmit-afterFinalize, len(sink.readRequirementObservationCovers)-afterEmit), "0 / 0 / 1")

	// EVERY WITHHELD REASON HAS AN EXECUTED PRODUCTION DRIVER. The evaluator
	// branches above drive all but the reuse-only member; the reuse path's own
	// builder drives that one, on a stored document with no row for a
	// requirement it serves.
	for _, event := range reusedObservationCoverEvents(InvestigationResult{
		AnswerPlan: &AnswerPlan{Requirements: []contractsv1.ContextFabricPlanRequirement{single}},
		Coverage:   factCoverage(health, SourceAvailable),
	}, nil) {
		seenReasons[event.RowWithheld] = true
	}
	missing := []string{}
	for _, reason := range RowWithheldReasonVocabulary() {
		if !seenReasons[reason] {
			missing = append(missing, string(reason))
		}
	}
	check("RowWithheldReasonVocabulary", "members with no executed driver", fmt.Sprintf("%v", missing), "[]")

	t.Logf("%-40s %-58s %-14s %s", "SIBLING", "CELL", "GOT", "WANT")
	for _, r := range table {
		t.Logf("%-40s %-58s %-14s %s", r.sibling, r.cell, r.got, r.want)
	}
	t.Logf("SIBLING CELLS EXECUTED: %d", len(table))
	if len(table) < 12 {
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
