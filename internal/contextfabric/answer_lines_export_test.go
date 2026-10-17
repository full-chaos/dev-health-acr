package contextfabric

import (
	"context"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// StoredAnswerabilityDriversForTest returns the stored answerability values the
// production decision takes across every step, determination and reading.
func StoredAnswerabilityDriversForTest(t *testing.T) []StoredAnswerability {
	t.Helper()
	project := SubjectProject
	named := storedStateFor(t, chaos5660NamedFrame(&project), "")
	organization := storedStateFor(t, roleOrgFrameWithGoals(nil, GoalAssessState), "")
	gateRow := storedClarification()
	gateRow.StructureNeeds = &contractsv1.ContextFabricStructureNeeds{Missing: []contractsv1.ContextFabricStructureNeedKind{contractsv1.ContextFabricStructureNeedWindow}, WindowOptions: []contractsv1.ContextFabricWindowOption{{}}}
	windowOnly := storedClarification()
	windowOnly.WindowClarification = &contractsv1.ContextFabricWindowClarification{Options: []contractsv1.ContextFabricWindowOption{{}}}
	drivers := []StoredAnswerability{
		DecideStoredAnswerability(InvestigationResult{Status: InvestigationComplete}, nil, SemanticStateReadAbsent),
		DecideStoredAnswerability(gateRow, named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), organization, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(roleCandidate("subr_project", SubjectProject, "project:1")), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(storedClarification(append(storedCIRunCandidates(), roleCandidate("subr_project", SubjectProject, "project:1"), roleCandidate("subr_project_2", SubjectProject, "project:2"))...), named, SemanticStateReadAvailable),
		DecideStoredAnswerability(windowOnly, nil, SemanticStateReadAbsent),
		DecideStoredAnswerability(storedClarification(), nil, SemanticStateReadAbsent),
	}
	for _, read := range []SemanticStateReadStatus{SemanticStateReadAbsent, SemanticStateReadUnsupportedVersion, SemanticStateReadMalformed, SemanticStateReadOversized, ""} {
		drivers = append(drivers, DecideStoredAnswerability(storedClarification(storedCIRunCandidates()...), nil, read))
	}
	return drivers
}

// SubjectlessTerminalReasonsForTest returns every reason the production
// classifier returns, driven through its inputs.
func SubjectlessTerminalReasonsForTest() []string {
	refusing := FrameGate{Outcome: FrameGateRejectedInvalid}
	passed := FrameGate{Outcome: FrameGatePassed}
	pool := SubjectResolution{Candidates: []SubjectCandidate{{}}}
	reasons := []string{
		subjectlessTerminalReason(refusing, SubjectResolution{}, 0, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{}, 0, declaredKindDecision{Unsatisfiable: true}),
		subjectlessTerminalReason(passed, pool, 0, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{ClarificationPrompt: "pick"}, 0, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{GraphNotProjected: true}, 0, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{InactiveHints: []contractsv1.ContextFabricInactiveHint{{}}}, 0, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{}, 1, declaredKindDecision{}),
		subjectlessTerminalReason(passed, SubjectResolution{}, 0, declaredKindDecision{}),
	}
	marked := context.WithValue(context.Background(), unboundScopeAnchorContextKey{}, true)
	return append(reasons, unboundAnchorAwareTerminalReason(marked, passed, pool, 0, declaredKindDecision{}))
}

// ObservableKindsForTest renders a kind list as the subjectless terminal line does.
func ObservableKindsForTest(kinds []SubjectKind) string { return observableKindList(kinds) }

// ObservableRefusalBasisForTest renders a refusal basis as the subjectless terminal line does.
func ObservableRefusalBasisForTest(basis contractsv1.ContextFabricRefusalBasis) string {
	return observableRefusalBasis(basis)
}

// PriorSubjectReceiptSkipReasonsForTest drives the engine's receipt skip
// recorder through every disposition and returns what it wrote to the sink.
func PriorSubjectReceiptSkipReasonsForTest() (reasons []string, skipped []int, epochDeltas map[string]int64) {
	rec := &recordingTelemetry{}
	engine := &Engine{telemetry: rec}
	dispositions := []contractsv1.ContextFabricPriorSubjectReceiptEntry{
		{Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedUnloadable},
		{Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedNoMatch},
		{Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedStaleGraphEpoch},
		{Disposition: contractsv1.ContextFabricPriorSubjectReceiptSkippedFailedReauth},
	}
	engine.recordPriorSubjectReceiptSkips(context.Background(), storage.Principal{OrgID: "org_1"}, dispositions, 7)
	epochDeltas = map[string]int64{}
	for _, record := range rec.priorSubjectReceiptSkipReasons {
		reasons = append(reasons, record.reason)
		epochDeltas[record.reason] = record.epochDelta
	}
	return reasons, rec.priorSubjectReceiptsSkipped, epochDeltas
}

// SynthesisFloorOutcomesForTest drives the status floor over a committed
// subject whose asked fact row was read beside a row nobody asked for, and
// over one whose only row was unasked, and returns what each wrote.
func SynthesisFloorOutcomesForTest() (matchedBesideUnasked, onlyUnasked SynthesisStatusOverrideOutcome) {
	build := func(kinds ...FactKind) (*InvestigationResult, CanonicalFactBundle) {
		result := affirmationResult()
		result.Status = InvestigationNoMatch
		result.SubjectResolution = SubjectResolution{Committed: []SubjectRef{affirmationSubject}}
		result.Interpretation.FactRequirements = []FactRequirement{{Kind: FactStatus}}
		facts := emptyAffirmationFacts()
		for _, kind := range kinds {
			facts.Facts = append(facts.Facts, CanonicalFact{
				Kind: kind, Subject: affirmationSubject, Fields: map[string]FactValue{}, EvidenceRefIDs: []string{affirmationSubjectRef},
				SourceState: SourceAvailable, Source: "test", SourceVersion: "v1",
			})
		}
		return &result, facts
	}
	result, facts := build(FactStatus, FactWorkload)
	matched := applyServerStatusFloor(result, emptyAffirmationGraph(), facts, nil)
	result, facts = build(FactWorkload)
	only := applyServerStatusFloor(result, emptyAffirmationGraph(), facts, nil)
	return *matched, *only
}
