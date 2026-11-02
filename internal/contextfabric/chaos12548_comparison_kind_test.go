package contextfabric

import (
	"context"
	"reflect"
	"strings"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12548: "Compare A and B" with bare names asked which one, and answering
// the kind asked again. These tests hold both halves: the question asks the kind,
// and a kind the caller states reaches both sides of the pair.

const kindAsked = contractsv1.ContextFabricSubjectRepository

func barePair() []SubjectOperand {
	return []SubjectOperand{comparisonNamedOperandWithoutKind("acme-api"), comparisonNamedOperandWithoutKind("acme-web")}
}

// interpretWithRequest runs the real call site with the caller's request.
func interpretWithRequest(t *testing.T, frame QuestionFrame, request InvestigationRequest, edit func(*ModelExecutionReceipt)) (resolved, proposed *QuestionFrame) {
	t.Helper()
	receipt := validModelReceiptFixture(ModelOperationInterpret)
	receipt.QuestionFrame = &frame
	edit(&receipt)
	interpreted := InterpretedQuestion{
		Shape: contractsv1.ContextFabricShapeExplicitCohort, RequestedJudgment: "compare the two",
		SubjectTerms: []string{"acme-api", "acme-web"}, TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	interpreter := RuntimeQuestionInterpreter{Runtime: fakeModelRuntime{interpreted: interpreted, receipt: receipt}, Sink: &fakeReceiptSink{}}
	_, outcome, err := interpreter.Interpret(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("Interpret() error = %v", err)
	}
	if outcome.Frame == nil {
		t.Fatal("the frame did not validate, so this test proves nothing")
	}
	return outcome.Frame, &frame
}

func requestStating(kinds ...SubjectKind) InvestigationRequest {
	request := validInvestigationRequest()
	request.ExpectedKinds = kinds
	return request
}

func TestAKindTheCallerStatesReachesBothSides(t *testing.T) {
	t.Parallel()
	resolved, proposed := interpretWithRequest(t, comparisonFrame(barePair()...), requestStating(kindAsked), func(*ModelExecutionReceipt) {})
	operands := ClassifyComparisonOperands(resolved)
	if operands.Admission != ComparisonAdmittedNamedPair {
		t.Fatalf("admission = %q, want %q", operands.Admission, ComparisonAdmittedNamedPair)
	}
	for _, slot := range operands.Slots {
		if slot.Kind != kindAsked {
			t.Fatalf("slot %+v, want kind %q", slot, kindAsked)
		}
	}
	for _, operand := range proposed.SubjectExpression.Explicit.Operands {
		if operand.Named.ExpectedKind != nil {
			t.Fatal("the proposal gained a kind the model never emitted")
		}
	}
}

// The caller is speaking now, so its kind comes before the model's.
func TestTheCallersKindComesBeforeTheModels(t *testing.T) {
	t.Parallel()
	resolved, _ := interpretWithRequest(t, comparisonFrame(barePair()...), requestStating(kindAsked), func(r *ModelExecutionReceipt) {
		r.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
	})
	if got := ClassifyComparisonOperands(resolved).Slots[0].Kind; got != kindAsked {
		t.Fatalf("kind = %q, want the caller's %q", got, kindAsked)
	}
}

func TestAStatedKindReachesOnlyANamedPair(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		frame   QuestionFrame
		request InvestigationRequest
		want    ComparisonAdmission
	}{
		"no kind":        {comparisonFrame(barePair()...), requestStating(), ComparisonOutOfCutUnstatedKind},
		"two kinds":      {comparisonFrame(barePair()...), requestStating(kindAsked, contractsv1.ContextFabricSubjectProject), ComparisonOutOfCutUnstatedKind},
		"a scoped side":  {comparisonFrame(comparisonNamedOperandWithoutKind("acme-api"), comparisonScopedOperand("platform", kindAsked)), requestStating(kindAsked), ComparisonOutOfCutUnstatedKind},
		"three sides":    {comparisonFrame(append(barePair(), comparisonNamedOperandWithoutKind("acme-ops"))...), requestStating(kindAsked), ComparisonOutOfCutOperandCount},
		"one side named": {namedFrameWithGoal([]string{"acme-api"}), requestStating(kindAsked), ComparisonNotAComparison},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolved, _ := interpretWithRequest(t, tc.frame, tc.request, func(*ModelExecutionReceipt) {})
			if got := ClassifyComparisonOperands(resolved).Admission; got != tc.want {
				t.Fatalf("admission = %q, want %q", got, tc.want)
			}
			if resolved.SubjectExpression.Named != nil && resolved.SubjectExpression.Named.ExpectedKind != nil {
				t.Fatal("a named_subject frame took the stated kind")
			}
		})
	}
}

// A one-subject frame keeps the model's kind, stated field or not.
func TestAStatedKindLeavesANamedSubjectToTheModel(t *testing.T) {
	t.Parallel()
	resolved, _ := interpretWithRequest(t, namedFrameWithGoal([]string{"acme-api"}), requestStating(kindAsked), func(r *ModelExecutionReceipt) {
		r.RequestedSubjectKind = contractsv1.ContextFabricSubjectProject
	})
	named := resolved.SubjectExpression.Named
	if named == nil || named.ExpectedKind == nil || *named.ExpectedKind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("named %+v, want the model's kind", named)
	}
}

// A redeemed kind option reads as the field, so the frame has one reading of it.
func TestInterpretRequestCarriesAConfirmedKind(t *testing.T) {
	t.Parallel()
	validated := []contractsv1.ContextFabricBoundSubjectReceipt{{ResultID: "result_1", ReceiptID: "receipt_1"}}
	confirmed := requestStructureCanonicalization{Confirmed: []confirmedStructureMember{{Member: contractsv1.ContextFabricStructureNeedExpectedKind, AppliedValue: string(kindAsked)}}}
	got := interpretRequestFor(validInvestigationRequest(), validated, confirmed)
	if !reflect.DeepEqual(got.ExpectedKinds, []SubjectKind{kindAsked}) || !reflect.DeepEqual(got.PriorSubjectReceipts, validated) {
		t.Fatalf("request %+v", got)
	}
	plain := interpretRequestFor(requestStating(contractsv1.ContextFabricSubjectProject), nil, requestStructureCanonicalization{})
	if !reflect.DeepEqual(plain.ExpectedKinds, []SubjectKind{contractsv1.ContextFabricSubjectProject}) {
		t.Fatalf("a request with no receipt lost its own kind: %+v", plain.ExpectedKinds)
	}
}

func kindMaterial() StructureOfferMaterial {
	return StructureOfferMaterial{
		Missing: []StructureNeedKind{
			contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectCandidate,
			contractsv1.ContextFabricStructureNeedSubjectAnchor, contractsv1.ContextFabricStructureNeedSubjectHandle,
		},
		KindOptions: []contractsv1.ContextFabricKindOption{
			{Kind: contractsv1.ContextFabricSubjectRepository, Label: "a repository"},
			{Kind: contractsv1.ContextFabricSubjectProject, Label: "a project"},
			{Kind: contractsv1.ContextFabricSubjectTeam, Label: "a team"},
		},
		AnchorOptions:          []contractsv1.ContextFabricAnchorOption{{Label: "acme-api"}},
		AnchorOptionsRequireV2: true,
		HandleOptions:          []contractsv1.ContextFabricHandleOption{{Label: "acme-api"}},
		CandidateOptions:       []contractsv1.ContextFabricCandidateOption{{Label: "acme-api"}},
	}
}

func askingResolution() SubjectResolution {
	return SubjectResolution{ClarificationPrompt: "Which subject did you mean: acme-api, acme-web?"}
}

func TestABareNameComparisonAsksTheKind(t *testing.T) {
	t.Parallel()
	frame := comparisonFrame(barePair()...)
	resolution, material := askComparisonKind(&frame, askingResolution(), kindMaterial())
	if want := `Are "acme-api" and "acme-web" each a repository, a project or a team?`; resolution.ClarificationPrompt != want {
		t.Fatalf("prompt = %q, want %q", resolution.ClarificationPrompt, want)
	}
	if !reflect.DeepEqual(material.Missing, []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}) {
		t.Fatalf("missing %v", material.Missing)
	}
	if len(material.KindOptions) != 3 {
		t.Fatalf("kind options %+v", material.KindOptions)
	}
	if material.CandidateOptions != nil || material.AnchorOptions != nil || material.HandleOptions != nil || material.AnchorOptionsRequireV2 {
		t.Fatalf("an offer to pick one side stayed: %+v", material)
	}
}

func TestOneKindOptionReadsAsOneKind(t *testing.T) {
	t.Parallel()
	frame := comparisonFrame(barePair()...)
	material := kindMaterial()
	material.KindOptions = material.KindOptions[:1]
	resolution, _ := askComparisonKind(&frame, askingResolution(), material)
	if want := `Are "acme-api" and "acme-web" each a repository?`; resolution.ClarificationPrompt != want {
		t.Fatalf("prompt = %q", resolution.ClarificationPrompt)
	}
}

func TestAKindQuestionAsksOnlyWhenTheKindIsWhatIsMissing(t *testing.T) {
	t.Parallel()
	bare := comparisonFrame(barePair()...)
	admitted := comparisonFrame(comparisonNamedOperand("acme-api", kindAsked), comparisonNamedOperand("acme-web", kindAsked))
	three := comparisonFrame(append(barePair(), comparisonNamedOperandWithoutKind("acme-ops"))...)
	scoped := comparisonFrame(comparisonNamedOperandWithoutKind("acme-api"), comparisonScopedOperand("", ""))
	single := namedFrameWithGoal([]string{"acme-api"})
	termless := comparisonFrame(comparisonNamedOperandWithoutKind("acme-api"), SubjectOperand{Kind: SubjectOperandNamed, Named: &NamedSubjectExpression{}})
	for name, tc := range map[string]struct {
		frame      *QuestionFrame
		resolution func(*SubjectResolution)
		material   func(*StructureOfferMaterial)
	}{
		"no frame":            {frame: nil},
		"one subject":         {frame: &single},
		"a stated pair":       {frame: &admitted},
		"three sides":         {frame: &three},
		"a scoped side":       {frame: &scoped},
		"a side with no term": {frame: &termless},
		"no question":         {frame: &bare, resolution: func(r *SubjectResolution) { r.ClarificationPrompt = "" }},
		"no kind offered":     {frame: &bare, material: func(m *StructureOfferMaterial) { m.KindOptions = nil }},
		"the kind is not missing": {frame: &bare, material: func(m *StructureOfferMaterial) {
			m.Missing = []StructureNeedKind{contractsv1.ContextFabricStructureNeedSubjectCandidate}
		}},
		"a question too long to serve": {frame: &bare, material: func(m *StructureOfferMaterial) {
			m.KindOptions[0].Label = strings.Repeat("a", contractsv1.ContextFabricProjectedClarificationPromptMaxLength)
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			resolution, material := askingResolution(), kindMaterial()
			if tc.resolution != nil {
				tc.resolution(&resolution)
			}
			if tc.material != nil {
				tc.material(&material)
			}
			wantResolution, wantMaterial := resolution, material
			gotResolution, gotMaterial := askComparisonKind(tc.frame, resolution, material)
			if !reflect.DeepEqual(gotResolution, wantResolution) || !reflect.DeepEqual(gotMaterial, wantMaterial) {
				t.Fatalf("the turn changed: %+v", gotResolution)
			}
		})
	}
}

// The real door: the engine's terminal composes the question the caller reads.
func TestAnEngineTurnServesTheKindQuestion(t *testing.T) {
	t.Parallel()
	frame := comparisonFrame(barePair()...)
	subject := func(id string) SubjectRef {
		return SubjectRef{Kind: kindAsked, CanonicalID: "repository:" + id, Label: id}
	}
	graph := &acceptanceGraphReader{
		resolution: SubjectResolution{
			Candidates: []SubjectCandidate{
				{Subject: subject("acme-api"), State: ResolutionAmbiguous, Confidence: 1, ReceiptID: "receipt_api_1", MatchReasons: []string{"exact"}},
				{Subject: subject("acme-web"), State: ResolutionAmbiguous, Confidence: 1, ReceiptID: "receipt_web_1", MatchReasons: []string{"exact"}},
			},
			Committed: []SubjectRef{}, ClarificationPrompt: "Which subject did you mean: acme-api, acme-web?",
		},
		context: emptyGraphContext(),
		material: StructureOfferMaterial{
			Missing:          []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind, contractsv1.ContextFabricStructureNeedSubjectCandidate},
			KindOptions:      []KindOption{{Kind: kindAsked, Label: "a repository", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
			CandidateOptions: []contractsv1.ContextFabricCandidateOption{{Kind: kindAsked, CanonicalID: "repository:acme-api", Label: "acme-api", OfferSource: contractsv1.ContextFabricStructureOfferEngine}},
		},
	}
	interpreter := &anchorProbeInterpreter{
		interpreted: InterpretedQuestion{Shape: contractsv1.ContextFabricShapeExplicitCohort, RequestedJudgment: "compare", SubjectTerms: []string{"acme-api", "acme-web"}, TimeContext: TimeContext{Axis: TemporalCurrent}, FactRequirements: []FactRequirement{}},
		outcome:     QuestionFamilyOutcome{Frame: &frame, FrameObligations: frame.Obligations, Family: QuestionFamilyExplicitComparison, Source: QuestionFamilySourceModel},
	}
	engine := buildWindowGateEngineWithTelemetry(t, interpreter, graph, &staticResultStore{results: map[string]InvestigationResult{}}, &recordingTelemetry{})
	request := validInvestigationRequestWithConfirmedWindow()
	request.Options.AllowClarification = true
	result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if result.Status != InvestigationClarificationRequired {
		t.Fatalf("status = %q", result.Status)
	}
	if want := `Are "acme-api" and "acme-web" each a repository?`; result.SubjectResolution.ClarificationPrompt != want {
		t.Fatalf("prompt = %q, want %q", result.SubjectResolution.ClarificationPrompt, want)
	}
	if result.StructureNeeds == nil || len(result.StructureNeeds.KindOptions) != 1 || len(result.StructureNeeds.CandidateOptions) != 0 {
		t.Fatalf("needs %+v", result.StructureNeeds)
	}
	if !reflect.DeepEqual(result.StructureNeeds.Missing, []StructureNeedKind{contractsv1.ContextFabricStructureNeedExpectedKind}) {
		t.Fatalf("missing %v", result.StructureNeeds.Missing)
	}
}
