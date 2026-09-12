package contextfabric

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE CLAIM, EXECUTED END TO END. The unit pins next door prove the
// predicate and the status function agree in isolation; they cannot prove
// that a caller posing the measured question receives the terminal, because
// the frame reaches the decision through the interpreter's own outcome and
// the offers reach it through GraphReader. Both seams are exercised here by
// driving Engine.Investigate exactly as the API route does, and reading the
// SERVED document and the EMITTED line rather than a function's return.

// chaos5660Interpreter is a QuestionInterpreter double that carries a
// VALIDATED frame out on its outcome, which the shared interpreterFunc
// double deliberately does not (it reports unclassified/none). The frame is
// the whole input to this decision, so a double that could not carry one
// could not exercise it.
type chaos5660Interpreter struct{ frame *QuestionFrame }

func (i chaos5660Interpreter) Interpret(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, QuestionFamilyOutcome, error) {
	return InterpretedQuestion{
		Shape:             ShapeOpen,
		RequestedJudgment: "status",
		TimeContext:       TimeContext{Axis: TemporalCurrent},
	}, QuestionFamilyOutcome{
		Family: QuestionFamilyUnclassified,
		Source: QuestionFamilySourceNone,
		Frame:  i.frame,
		Gate:   FrameGate{Outcome: FrameGatePassed},
	}, nil
}

// chaos5660Graph returns the measured resolution and offer material
// verbatim, and fails the test if facts are ever read -- a turn that
// terminates without a subject must not reach the fact readers.
type chaos5660Graph struct {
	resolution SubjectResolution
	material   StructureOfferMaterial
}

func (g chaos5660Graph) ResolveInvestigationBinding(context.Context, storage.Principal) (ResolvedGraphBinding, error) {
	return ResolvedGraphBinding{GraphKey: "chaos5660-key", Epoch: 0}, nil
}

func (g chaos5660Graph) ResolveSubjects(context.Context, storage.Principal, InvestigationRequest, InterpretedQuestion, ResolvedGraphBinding, *ConfirmedExpectedKind, *ConfirmedAnchorSelection, *QuestionFrame, SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	return g.resolution, g.material, nil, nil, nil
}

func (g chaos5660Graph) DiscoverContext(context.Context, storage.Principal, GraphDiscoveryRequest) (GraphContext, error) {
	return GraphContext{Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}}}, nil
}

func chaos5660Engine(t *testing.T, frame *QuestionFrame, graph GraphReader, telemetry EngineTelemetry) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineDependencies{
		Interpreter: chaos5660Interpreter{frame: frame},
		Graph:       graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			t.Fatal("ReadFacts must not be called on a subjectless terminal")
			return CanonicalFactBundle{}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("Synthesize must not be called on a subjectless terminal")
			return InvestigationResult{}, nil
		}),
		Telemetry: telemetry,
	}, EngineOptions{
		ServiceVersion: "acr-test",
		Now:            func() time.Time { return time.Unix(300, 0).UTC() },
		NewResultID:    func() string { return "result_chaos5660_01" },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// chaos5660MeasuredOffers is the offer material both rows carried on the
// measured turns: handle and candidate options of ci_pipeline_run and
// pull_request, and never the declared kind.
func chaos5660MeasuredOffers() StructureOfferMaterial {
	return StructureOfferMaterial{
		Missing: []contractsv1.ContextFabricStructureNeedKind{
			contractsv1.ContextFabricStructureNeedSubjectCandidate,
		},
		HandleOptions: []contractsv1.ContextFabricHandleOption{
			{ReceiptID: "handr_chaos5660a", OptionID: "opt_handle_a", Label: "CI run 29213415002", Kind: contractsv1.ContextFabricSubjectCIRun, PatternID: "pr_number", Value: "29213415002", SourceColumn: "ci_pipeline_runs.run_id", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
			{ReceiptID: "handr_chaos5660b", OptionID: "opt_handle_b", Label: "PR #42", Kind: contractsv1.ContextFabricSubjectPullRequest, PatternID: "pr_number", Value: "42", SourceColumn: "pull_requests.number", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
		CandidateOptions: []contractsv1.ContextFabricCandidateOption{
			{ReceiptID: "candr_chaos5660a", OptionID: "opt_cand_a", Label: "CI run 29213415002", Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run.v2:x:29213415002", OfferSource: contractsv1.ContextFabricStructureOfferEngine},
		},
	}
}

// chaos5660CIRunCandidates builds n uncommitted ci_pipeline_run candidates --
// the pool shape the measured rows carried, every one of the wrong kind.
func chaos5660CIRunCandidates(n int) []SubjectCandidate {
	var candidates []SubjectCandidate
	for index := range n {
		candidates = append(candidates, SubjectCandidate{
			ReceiptID:    fmt.Sprintf("subr_chaos5660_%02d", index),
			Subject:      SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: fmt.Sprintf("ci_pipeline_run.v2:x:2921341500%d", index), Label: fmt.Sprintf("CI run 2921341500%d", index)},
			State:        contractsv1.ContextFabricResolutionAmbiguous,
			MatchReasons: []string{"semantic"},
			Confidence:   0.5,
		})
	}
	return candidates
}

// TestInvestigateServesTheTerminalForTheMeasuredQuestion is the executed
// end-to-end claim. A caller poses a question whose frame declares
// `project`; retrieval offers seven ci_pipeline_run candidates and handle
// and candidate options of two other kinds; the SERVED document is a
// no_match on the first turn, carrying the declared-kind sentence, and the
// EMITTED line names the reason and both kind sets.
func TestInvestigateServesTheTerminalForTheMeasuredQuestion(t *testing.T) {
	t.Parallel()
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	candidates := chaos5660CIRunCandidates(7)
	telemetry := &recordingTelemetry{}
	engine := chaos5660Engine(t, chaos5660NamedFrame(&project), chaos5660Graph{
		resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}},
		material:   chaos5660MeasuredOffers(),
	}, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_5660"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("served status = %q, want no_match on the FIRST turn -- this is the exact document that asked five unanswerable questions", result.Status)
	}
	found := false
	for _, limitation := range result.Limitations {
		if limitation == declaredKindTerminalLimitation {
			found = true
		}
	}
	if !found {
		t.Fatalf("served limitations = %#v, want the declared-kind terminal sentence", result.Limitations)
	}
	if want := []string{declaredKindTerminalReason}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
	}
	if want := []string{"project"}; !stringSlicesEqual(telemetry.subjectlessTerminalDeclaredKinds, want) {
		t.Fatalf("emitted declared_kinds = %#v, want %#v", telemetry.subjectlessTerminalDeclaredKinds, want)
	}
	if want := []string{"ci_pipeline_run,pull_request"}; !stringSlicesEqual(telemetry.subjectlessTerminalOfferedKinds, want) {
		t.Fatalf("emitted offered_kinds = %#v, want %#v -- the kinds actually offered are half the finding", telemetry.subjectlessTerminalOfferedKinds, want)
	}
}

// TestInvestigateStillClarifiesWhenAnOfferCarriesTheDeclaredKind is the
// end-to-end control. Identical drive, one candidate of the declared kind
// added: the served status goes back to clarification_required and the
// emitted reason back to `ambiguous`. Without this arm the change above is
// indistinguishable from "terminate every named-subject turn".
func TestInvestigateStillClarifiesWhenAnOfferCarriesTheDeclaredKind(t *testing.T) {
	t.Parallel()
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	candidates := append(chaos5660CIRunCandidates(1), SubjectCandidate{
		ReceiptID:    "subr_chaos5660_project",
		Subject:      SubjectRef{Kind: contractsv1.ContextFabricSubjectProject, CanonicalID: "project.v2:x:60997592", Label: "Dev Health Agent Context Runtime (Context Fabric)"},
		State:        contractsv1.ContextFabricResolutionAmbiguous,
		MatchReasons: []string{"exact_name"},
		Confidence:   0.6,
	})
	telemetry := &recordingTelemetry{}
	engine := chaos5660Engine(t, chaos5660NamedFrame(&project), chaos5660Graph{
		resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}},
		material:   chaos5660MeasuredOffers(),
	}, telemetry)

	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_5660"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("served status = %q, want clarification_required -- a conversation that CAN converge must not be terminated", result.Status)
	}
	if want := []string{"ambiguous"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
		t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
	}
	if want := []string{"project"}; !stringSlicesEqual(telemetry.subjectlessTerminalDeclaredKinds, want) {
		t.Fatalf("emitted declared_kinds = %#v, want %#v", telemetry.subjectlessTerminalDeclaredKinds, want)
	}
}

// TestInvestigateIsUnchangedWhenTheFrameDeclaresNoKind is the executed cell
// for the "absent is weaker than declared" rule at the engine seam, not just
// in the guard: the SAME offers that terminate above must still clarify when
// the question constrained no kind. It is also the cell that keeps every
// pre-existing caller of this path -- the shared interpreterFunc double
// carries no frame at all -- provably unaffected.
func TestInvestigateIsUnchangedWhenTheFrameDeclaresNoKind(t *testing.T) {
	t.Parallel()
	candidates := chaos5660CIRunCandidates(1)
	for _, testCase := range []struct {
		cell  string
		frame *QuestionFrame
	}{
		{"no frame validated at all", nil},
		{"a named frame that stated no ExpectedKind", chaos5660NamedFrame(nil)},
	} {
		t.Run(testCase.cell, func(t *testing.T) {
			telemetry := &recordingTelemetry{}
			engine := chaos5660Engine(t, testCase.frame, chaos5660Graph{
				resolution: SubjectResolution{Candidates: candidates, Committed: []SubjectRef{}},
				material:   chaos5660MeasuredOffers(),
			}, telemetry)
			result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_5660"}, validInvestigationRequest())
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != InvestigationClarificationRequired {
				t.Fatalf("served status = %q, want clarification_required unchanged", result.Status)
			}
			if want := []string{"ambiguous"}; !stringSlicesEqual(telemetry.subjectlessTerminalReasons, want) {
				t.Fatalf("emitted reasons = %#v, want %#v", telemetry.subjectlessTerminalReasons, want)
			}
			if want := []string{"none"}; !stringSlicesEqual(telemetry.subjectlessTerminalDeclaredKinds, want) {
				t.Fatalf("emitted declared_kinds = %#v, want the explicit none token", telemetry.subjectlessTerminalDeclaredKinds)
			}
		})
	}
}

// TestTheServedTerminalDisclosesItsBasis executes the disclosure at the
// served document, which is where a consumer reads it: the terminal names
// declared_kind_unmatched on the wire and carries that basis's own sentence,
// and it does NOT claim the frame refusal it did not take.
//
// This test replaced one that pinned the ABSENCE of a basis. That earlier
// pin was correct while no vocabulary member was true of this state, and it
// is kept in the history rather than quietly deleted because the two
// together are the record of the decision: the class was uncountable on the
// wire, it was named as such, and then it was closed.
func TestTheServedTerminalDisclosesItsBasis(t *testing.T) {
	t.Parallel()
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	telemetry := &recordingTelemetry{}
	engine := chaos5660Engine(t, chaos5660NamedFrame(&project), chaos5660Graph{
		resolution: SubjectResolution{Candidates: chaos5660CIRunCandidates(1), Committed: []SubjectRef{}},
		material:   chaos5660MeasuredOffers(),
	}, telemetry)
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_5660"}, validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationNoMatch {
		t.Fatalf("served status = %q, want no_match", result.Status)
	}
	if result.RefusalBasis != contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched {
		t.Fatalf("served refusal_basis = %q, want %q -- without it the class is countable in the log and invisible to every consumer",
			result.RefusalBasis, contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched)
	}
	carried := false
	for _, limitation := range result.Limitations {
		if limitation == contractsv1.ContextFabricDeclaredKindUnmatchedLimitation {
			carried = true
		}
		if strings.Contains(limitation, "which this service has no way to enumerate") {
			t.Fatalf("the terminal claims the service cannot enumerate this kind, which is false: %q", limitation)
		}
	}
	if !carried {
		t.Fatalf("served limitations = %#v, want the basis's own sentence", result.Limitations)
	}
	if want := []string{string(contractsv1.ContextFabricRefusalBasisDeclaredKindUnmatched)}; !stringSlicesEqual(telemetry.subjectlessTerminalRefusalBases, want) {
		t.Fatalf("emitted refusal_basis = %#v, want %#v -- the log line and the wire must name one decision", telemetry.subjectlessTerminalRefusalBases, want)
	}
}

// TestTheWindowConfirmationGateIsUnaffected is the CLASS SWEEP's executed
// cell, not its argument. The other production composer of
// clarification_required is the window-confirmation gate (window.go), and
// dismissing it by reasoning would leave it unswept.
//
// The cell is built to be as hostile as the sweep allows: the frame declares
// `project`, and the offers-only resolve under the gate returns handle and
// candidate options of ci_pipeline_run and pull_request -- the same material
// that terminates on the resolution path above. The gate must STILL clarify,
// because the need it raises is the WINDOW and the options it offers are
// window options: the offer satisfies the need it was raised for, which is
// the only property this change checks.
func TestTheWindowConfirmationGateIsUnaffected(t *testing.T) {
	t.Parallel()
	project := SubjectKind(contractsv1.ContextFabricSubjectProject)
	interpreter := &countingInterpreter{
		interpretation: bootstrapInterpretation(),
		family: QuestionFamilyOutcome{
			Family: QuestionFamilyUnclassified,
			Source: QuestionFamilySourceNone,
			Frame:  chaos5660NamedFrame(&project),
			Gate:   FrameGate{Outcome: FrameGatePassed},
		},
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
		context:    emptyGraphContext(),
		material:   chaos5660MeasuredOffers(),
	}
	engine := buildWindowGateEngine(t, interpreter, graph, newMapResultStore())

	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), validInvestigationRequest())
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("served status = %q, want clarification_required -- the window gate's need is the window and it offered window options; terminating it would end a conversation that can converge on its very next turn", result.Status)
	}
	if result.WindowClarification == nil || len(result.WindowClarification.Options) == 0 {
		t.Fatal("the window gate stopped offering window options")
	}
	for _, limitation := range result.Limitations {
		if limitation == declaredKindTerminalLimitation {
			t.Fatalf("the window gate's terminal acquired the declared-kind sentence: %#v", result.Limitations)
		}
	}
}
