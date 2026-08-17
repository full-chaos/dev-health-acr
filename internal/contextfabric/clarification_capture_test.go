package contextfabric

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recordingClarificationSink is the CHAOS-3859 test double: it just
// remembers every event it was given, so a test can assert exactly what
// Engine handed to the ClarificationSelectionSink port.
type recordingClarificationSink struct {
	events []ClarificationSelectionEvent
}

func (s *recordingClarificationSink) RecordSelection(_ context.Context, event ClarificationSelectionEvent) {
	s.events = append(s.events, event)
}

func mustEngineForClarificationCaptureTest(t *testing.T, store InvestigationResultStore, sink ClarificationSelectionSink) *Engine {
	t.Helper()
	project := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	graph := &capturingGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context: GraphContext{
			Paths: []RelationshipPath{}, DriverCandidates: []DriverJudgment{}, FactRequirements: []FactRequirement{},
			EvidenceRefIDs: []string{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
		},
	}
	interpretation := InterpretedQuestion{
		Shape: ShapeOpen, RequestedJudgment: "status", TimeContext: TimeContext{Axis: TemporalCurrent},
		FactRequirements: []FactRequirement{{Kind: FactStatus}},
	}
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return interpretation, nil
		}),
		Graph: graph,
		Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return CanonicalFactBundle{
				Facts: []CanonicalFact{}, Coverage: Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				Version: "ops-v1", Versions: map[FactKind]string{}, Watermarks: map[FactKind]string{},
			}, nil
		}),
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			return InvestigationResult{
				Status: InvestigationComplete, DirectJudgment: "Ask Dev is on track.", CurrentState: "Nominal.",
				StrongestPressures: []string{}, Drivers: []DriverJudgment{}, RemainingWork: []Finding{}, ReadinessGaps: []Finding{},
				Paths: []RelationshipPath{}, Conflicts: []Finding{}, Limitations: []string{}, EvidenceRefIDs: []string{},
				ClaimedFacts:        []ClaimedFact{},
				Coverage:            Coverage{Sources: []SourceObservation{}, DegradedReasons: []string{}},
				DeterministicAnswer: "Ask Dev is on track based on available context.", Warnings: []string{},
				Versions: VersionSet{
					Backend: "test", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
					InterpretationVersion: "interpret-v1", SynthesisVersion: "synthesis-v1",
				},
			}, nil
		}),
		Results: store, ClarificationSelectionSink: sink,
	}, EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(500, 0).UTC() }, NewResultID: func() string { return "result_capture_00001" },
		ReuseProjectionVersion: "projection-current-v1", ReuseModelIdentities: []string{"openai-compatible/gpt-5-nano"},
		ReuseRetrievalIdentity: ReuseRetrievalIdentity{EmbedRetrievalIdentity: "none", RetrievalPolicyVersion: "rp1"},
		ReusePromptVersions: ReusePromptVersions{
			InterpretationPromptVersion: "context-fabric-interpretation.v7", SynthesisPromptVersion: "context-fabric-synthesis.v9",
		},
		ReuseVersionAuthorities: ReuseVersionAuthorities{
			QueryVersion: "devhealthfacts.clickhouse.v1", CanonicalServiceVersion: "context-fabric-facts.v1", ModelOutputSchemaVersion: "context-fabric-model-output.v1",
		},
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

// twoCandidatePriorResult is a clarification_required-shaped prior result
// offering TWO candidates, one of which the test's PriorSubjectReceipts
// will go on to select -- so capture's "complete offered set, not just the
// selection" claim is provable against a real negative example.
func twoCandidatePriorResult() InvestigationResult {
	selected := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev"}
	other := SubjectRef{Kind: SubjectProject, CanonicalID: "project_ask_web", Label: "Ask Web"}
	prior := validInvestigationResult()
	prior.ResultID = "result_clarify_0001"
	prior.Question = "Was Ask ready to ship?"
	prior.Status = InvestigationClarificationRequired
	prior.SubjectResolution = SubjectResolution{
		Candidates: []SubjectCandidate{
			{ReceiptID: "receipt_selected_01", Subject: selected, State: ResolutionProposed, MatchReasons: []string{"Verbatim substring matched."}, Confidence: 0.91},
			{ReceiptID: "receipt_other_02", Subject: other, State: ResolutionProposed, MatchReasons: []string{"Verbatim substring matched."}, Confidence: 0.58},
		},
	}
	return prior
}

// TestCHAOS3859_CaptureRecordsSelectionOnMatchingPriorSubjectReceipt is the
// primary proof: a PriorSubjectReceipt that resolves to a real candidate in
// a real prior clarification_required result must produce EXACTLY ONE
// ClarificationSelectionEvent, carrying the COMPLETE offered candidate set
// (both entries, not just the one chosen), the correct Selected entry, the
// question hash (not raw text), and the deployment-current pipeline
// context Engine was configured with.
func TestCHAOS3859_CaptureRecordsSelectionOnMatchingPriorSubjectReceipt(t *testing.T) {
	t.Parallel()
	prior := twoCandidatePriorResult()
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	sink := &recordingClarificationSink{}
	engine := mustEngineForClarificationCaptureTest(t, store, sink)

	request := validInvestigationRequest()
	request.Question = "What about Ask Dev specifically?"
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_selected_01"}}
	request.Consumer = ConsumerInfo{Name: "test", Version: "1.0.0", Surface: "mcp"}
	principal := storage.Principal{OrgID: "org_capture_1", AuthenticationMethod: storage.AuthenticationMethodCredential}

	if _, err := engine.Investigate(context.Background(), principal, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("sink.events = %#v, want exactly 1 captured selection", sink.events)
	}
	event := sink.events[0]
	if event.OrgID != "org_capture_1" {
		t.Errorf("OrgID = %q, want org_capture_1", event.OrgID)
	}
	if event.QuestionHash != QuestionHash(prior.Question) {
		t.Errorf("QuestionHash = %q, want the hash of the PRIOR (clarification-triggering) question, not the follow-up's", event.QuestionHash)
	}
	if event.PriorResultID != prior.ResultID {
		t.Errorf("PriorResultID = %q, want %q", event.PriorResultID, prior.ResultID)
	}
	if len(event.OfferedCandidates) != 2 {
		t.Fatalf("OfferedCandidates = %#v, want BOTH candidates captured, not just the selection", event.OfferedCandidates)
	}
	if event.OfferedCandidates[0].ReceiptID != "receipt_selected_01" || event.OfferedCandidates[0].Rank != 0 {
		t.Errorf("OfferedCandidates[0] = %#v", event.OfferedCandidates[0])
	}
	if event.OfferedCandidates[1].ReceiptID != "receipt_other_02" || event.OfferedCandidates[1].Rank != 1 {
		t.Errorf("OfferedCandidates[1] = %#v, want the NOT-selected candidate present as a negative example", event.OfferedCandidates[1])
	}
	if event.Selected.ReceiptID != "receipt_selected_01" || event.Selected.SubjectCanonicalID != "project_ask_dev" {
		t.Errorf("Selected = %#v", event.Selected)
	}
	if event.SelectionProvenance != "credential_mcp" {
		t.Errorf("SelectionProvenance = %q, want credential_mcp for a credential-authenticated MCP surface call", event.SelectionProvenance)
	}
	if event.CapturedAt.IsZero() || !event.CapturedAt.Equal(time.Unix(500, 0).UTC()) {
		t.Errorf("CapturedAt = %v, want Engine's own injected clock value", event.CapturedAt)
	}
	if event.ProjectionVersion != "projection-current-v1" {
		t.Errorf("ProjectionVersion = %q, want the engine's current reuse projection version", event.ProjectionVersion)
	}
	if event.PromptVersions.InterpretationPromptVersion != "context-fabric-interpretation.v7" {
		t.Errorf("PromptVersions.InterpretationPromptVersion = %q", event.PromptVersions.InterpretationPromptVersion)
	}
	if event.VersionAuthorities.QueryVersion != "devhealthfacts.clickhouse.v1" {
		t.Errorf("VersionAuthorities.QueryVersion = %q", event.VersionAuthorities.QueryVersion)
	}
}

// TestCHAOS3859_NoCaptureOnUnmatchedReceipt proves capture is gated on a
// GENUINE match -- a receipt naming a real prior result but the WRONG
// ReceiptID (never offered, or a typo) must not produce any event, exactly
// mirroring resolvePriorSubjectHints' own "no matching candidate" silent
// skip for the hint itself.
func TestCHAOS3859_NoCaptureOnUnmatchedReceipt(t *testing.T) {
	t.Parallel()
	prior := twoCandidatePriorResult()
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	sink := &recordingClarificationSink{}
	engine := mustEngineForClarificationCaptureTest(t, store, sink)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_never_offered"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_capture_2"}, request); err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("sink.events = %#v, want none for an unmatched receipt", sink.events)
	}
}

// TestCHAOS3859_InvestigateSucceedsWithoutASink is the fail-open/additive
// proof: a nil ClarificationSelectionSink (capture off, or not yet
// composed) must not change Investigate's own behavior at all, even when
// PriorSubjectReceipts genuinely matches -- capture is strictly additive.
func TestCHAOS3859_InvestigateSucceedsWithoutASink(t *testing.T) {
	t.Parallel()
	prior := twoCandidatePriorResult()
	store := &staticResultStore{results: map[string]InvestigationResult{prior.ResultID: prior}}
	engine := mustEngineForClarificationCaptureTest(t, store, nil)

	request := validInvestigationRequest()
	request.PriorSubjectReceipts = []BoundSubjectReceipt{{ResultID: prior.ResultID, ReceiptID: "receipt_selected_01"}}

	if _, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_capture_3"}, request); err != nil {
		t.Fatalf("Investigate() error = %v with a nil ClarificationSelectionSink, want capture-off to be a pure no-op", err)
	}
}

// TestClarificationSelectionProvenance is the direct unit-level proof for
// every branch of the BEST-EFFORT human-vs-agent proxy -- see the
// function's own doc comment for why each of these is a proxy, not a
// classification.
func TestClarificationSelectionProvenance(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		principal storage.Principal
		consumer  ConsumerInfo
		want      string
	}{
		{"web assertion wins regardless of surface", storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion}, ConsumerInfo{Surface: "mcp"}, "web_assertion"},
		{"credential + spoof-resistant mcp surface", storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential}, ConsumerInfo{Surface: "mcp"}, "credential_mcp"},
		{"credential + another named surface", storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential}, ConsumerInfo{Surface: "workbench"}, "credential_workbench"},
		{"credential + empty surface", storage.Principal{AuthenticationMethod: storage.AuthenticationMethodCredential}, ConsumerInfo{Surface: ""}, "credential_unknown_surface"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := clarificationSelectionProvenance(testCase.principal, testCase.consumer); got != testCase.want {
				t.Errorf("clarificationSelectionProvenance() = %q, want %q", got, testCase.want)
			}
		})
	}
}
