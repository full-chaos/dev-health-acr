package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	cf "github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memoryinvestigation"
	"github.com/full-chaos/dev-health-acr/internal/limits"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

type kindRefusal string

const (
	refusalUnavailable kindRefusal = "unavailable"
	refusalRejected    kindRefusal = "rejected"
)

type refusableProvider struct {
	inner   cf.FactProvider
	refused map[cf.FactKind]kindRefusal
}

func (p refusableProvider) Capability() cf.FactCapability { return p.inner.Capability() }

func (p refusableProvider) ReadFacts(_ context.Context, _ storage.Principal, query cf.FactQuery) (cf.FactProviderResult, error) {
	capability := p.inner.Capability()
	refusal := p.refused[capability.Kind]
	if refusal == refusalUnavailable {
		return cf.FactProviderResult{State: cf.SourceUnavailable}, nil
	}
	fields := map[string]cf.FactValue{}
	facts := make([]cf.CanonicalFact, 0, len(query.Subjects))
	for _, subject := range query.Subjects {
		facts = append(facts, cf.CanonicalFact{Kind: capability.Kind, Subject: subject, Fields: fields, SourceState: cf.SourceAvailable, Source: capability.Name, SourceVersion: capability.Version, EvidenceRefIDs: evidenceOf(capability, refusal)})
	}
	return cf.FactProviderResult{State: cf.SourceAvailable, Facts: facts}, nil
}

type completeStatusModel struct{}

func (completeStatusModel) InterpretQuestion(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.ModelExecutionReceipt, error) {
	return cf.InterpretedQuestion{}, cf.ModelExecutionReceipt{}, errors.New("interpreted by teamStatusInterpreter")
}

func (completeStatusModel) SynthesizeAnswer(_ context.Context, _ storage.Principal, input cf.SynthesisInput) (cf.SynthesisDraft, cf.ModelExecutionReceipt, error) {
	draft := validRouteTestSynthesisDraft()
	draft.Status = cf.InvestigationComplete
	draft.Drivers = []cf.DriverJudgment{}
	draft.DirectJudgment = "The Platform team is in good shape."
	draft.CurrentState = "Stable."
	draft.DeterministicAnswer = "The Platform team is in good shape."
	receipt := validRouteTestModelReceipt(cf.ModelOperationSynthesize)
	receipt.Outcome = "success"
	return draft, receipt, nil
}

type provenTeamGraph struct{ surfaceGraph }

func (g provenTeamGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request cf.InvestigationRequest, interpreted cf.InterpretedQuestion, binding cf.ResolvedGraphBinding, kind *cf.ConfirmedExpectedKind, anchor *cf.ConfirmedAnchorSelection, frame *cf.QuestionFrame, scopeKind cf.SubjectKind) (cf.SubjectResolution, cf.StructureOfferMaterial, cf.CommitBasisSet, cf.CommitDecisionDigestSet, error) {
	resolution, material, _, digests, err := g.surfaceGraph.ResolveSubjects(ctx, principal, request, interpreted, binding, kind, anchor, frame, scopeKind)
	bases := cf.CommitBasisSet{}
	for _, subject := range resolution.Committed {
		bases.Record(subject, cf.CommitBasisCallerCanonicalID)
	}
	return resolution, material, bases, digests, err
}

type teamStatusInterpreter struct{}

func (teamStatusInterpreter) Interpret(context.Context, storage.Principal, cf.InvestigationRequest) (cf.InterpretedQuestion, cf.QuestionFamilyOutcome, error) {
	frame := cf.DeriveFrameObligations(cf.QuestionFrame{
		Goals:             []cf.InvestigationGoal{cf.GoalAssessState},
		SubjectExpression: cf.SubjectExpression{Kind: cf.SubjectExpressionNamed, Named: &cf.NamedSubjectExpression{Terms: []string{"Platform"}, ExpectedKind: teamKind()}},
		Temporal:          cf.TemporalIntentCurrent, Version: cf.QuestionFrameVersion,
	}, nil)
	validated := cf.ValidateFrame(frame, nil, cf.ShapeOpen)
	if validated.Outcome != cf.FrameValidationOutcomeValid {
		return cf.InterpretedQuestion{}, cf.QuestionFamilyOutcome{}, fmt.Errorf("invalid frame: %+v", validated.Failure)
	}
	frame = validated.Frame
	return cf.InterpretedQuestion{Shape: cf.ShapeOpen, RequestedJudgment: "status", TimeContext: cf.TimeContext{Axis: cf.TemporalCurrent}, FactRequirements: []cf.FactRequirement{{Kind: cf.FactHealth}, {Kind: cf.FactWorkload}, {Kind: cf.FactFlow}}},
		cf.QuestionFamilyOutcome{Family: cf.QuestionFamilyUnclassified, Source: cf.QuestionFamilySourceModel, Frame: &frame, FrameObligations: frame.Obligations, Gate: cf.DecideFrameGate(validated, true)}, nil
}

func evidenceOf(capability cf.FactCapability, refusal kindRefusal) []string {
	if refusal == refusalRejected {
		return nil
	}
	return []string{"evidence_" + capability.Name}
}

func serveTeamStatus(t *testing.T, refused map[cf.FactKind]kindRefusal, authority bool, logs *bytes.Buffer) cf.InvestigationResult {
	t.Helper()
	providers := []cf.FactProvider{}
	for _, real := range devhealthfacts.NewProviders(nil) {
		providers = append(providers, refusableProvider{inner: real, refused: refused})
	}
	registry, err := cf.NewFactCapabilityRegistry(providers, cf.FactRegistryOptions{Logger: slog.New(slog.NewJSONHandler(logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	team := cf.SubjectRef{Kind: cf.SubjectTeam, CanonicalID: "team:PLATFORM", Label: "Platform"}
	store := rankingSurfaceStore{memoryinvestigation.NewStore()}
	next := 0
	engine, err := cf.NewEngine(cf.EngineDependencies{
		Telemetry:    cf.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(logs, nil))),
		Interpreter:  teamStatusInterpreter{},
		Graph:        provenTeamGraph{surfaceGraph{resolution: cf.SubjectResolution{Candidates: []cf.SubjectCandidate{}, Committed: []cf.SubjectRef{team}}}},
		Facts:        registry,
		Requirements: registry,
		Results:      store,
		Synthesizer: cf.RuntimeAnswerSynthesizer{Runtime: completeStatusModel{}, Options: cf.RuntimeAnswerSynthesizerOptions{
			ServiceVersion: "acr-test", Backend: "graph", ProjectionVersion: "projection-v1", QueryVersion: "query-v1",
		}},
	}, cf.EngineOptions{
		ServiceVersion: "acr-test", Now: func() time.Time { return time.Unix(700, 0).UTC() },
		NewResultID:                        func() string { next++; return fmt.Sprintf("result_status_word_%02d", next) },
		ServerCompletenessAuthorityEnabled: authority,
	})
	if err != nil {
		t.Fatal(err)
	}
	app, token := newParityHostedAppWithLogs(t, engine, store, limits.ResourceBudget{MaxItems: 50, MaxTokens: 16_000, MaxBytes: 1 << 20}, logs)
	response := postTeamQuestion(t, app, token)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s\nlogs:\n%s", response.Code, response.Body.String(), logs.String())
	}
	var result cf.InvestigationResult
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

// The served status word is the synthesizer's. The server's own account of
// completeness is completeness.state, derived from the outcome rows the real
// provider declarations produce. These cells pin what each says when one kind
// of a team status answer is refused, with the model claiming complete.
func TestAnswerCompletenessIsPinnedUnderBothSynthesizerStatusWords(t *testing.T) {
	cases := []struct {
		name             string
		refused          map[cf.FactKind]kindRefusal
		wantPartial      bool
		wantState        cf.InvestigationStatus
		wantRequirement  string
		wantDecidingKind string
	}{
		{name: "every kind served", wantState: cf.InvestigationComplete},
		{name: "health unavailable", refused: map[cf.FactKind]kindRefusal{cf.FactHealth: refusalUnavailable}, wantPartial: true, wantState: cf.InvestigationDegraded, wantRequirement: "health/subject/team", wantDecidingKind: "unavailable"},
		{name: "health rejected", refused: map[cf.FactKind]kindRefusal{cf.FactHealth: refusalRejected}, wantPartial: true, wantState: cf.InvestigationDegraded, wantRequirement: "health/subject/team", wantDecidingKind: "unavailable"},
		{name: "flow unavailable", refused: map[cf.FactKind]kindRefusal{cf.FactFlow: refusalUnavailable}, wantPartial: true, wantState: cf.InvestigationPartial, wantRequirement: "state/subject/team", wantDecidingKind: "narrowed"},
		{name: "workload and flow unavailable", refused: map[cf.FactKind]kindRefusal{cf.FactWorkload: refusalUnavailable, cf.FactFlow: refusalUnavailable}, wantPartial: true, wantState: cf.InvestigationPartial, wantRequirement: "state/subject/team", wantDecidingKind: "narrowed"},
	}
	for _, authority := range []bool{false, true} {
		for _, testCase := range cases {
			t.Run(fmt.Sprintf("authority=%v/%s", authority, testCase.name), func(t *testing.T) {
				logs := &bytes.Buffer{}
				result := serveTeamStatus(t, testCase.refused, authority, logs)

				if err := result.Validate(); err != nil {
					t.Fatalf("the served result is not contract-valid: %v", err)
				}
				if result.Coverage.Partial != testCase.wantPartial {
					t.Fatalf("coverage.partial = %v, want %v", result.Coverage.Partial, testCase.wantPartial)
				}
				if string(result.Completeness.State) != string(testCase.wantState) {
					t.Fatalf("completeness.state = %q, want %q", result.Completeness.State, testCase.wantState)
				}
				wantStatus := cf.InvestigationComplete
				if authority {
					wantStatus = testCase.wantState
				}
				if result.Status != wantStatus {
					t.Fatalf("status = %q, want %q (authority=%v: the model said complete)", result.Status, wantStatus, authority)
				}
				if string(result.Completeness.TerminalStatus) != string(result.Status) {
					t.Fatalf("completeness.terminal_status = %q, status = %q", result.Completeness.TerminalStatus, result.Status)
				}
				line := decodeLogLine(t, logs.String(), "context fabric completeness authority")
				disagreed := testCase.wantState != cf.InvestigationComplete
				want := map[string]any{
					"basis": "outcome_derived", "model_status": "complete", "server_state": string(testCase.wantState),
					"disagreed": disagreed, "would_flip": disagreed,
					"deciding_requirement": testCase.wantRequirement, "deciding_outcome": testCase.wantDecidingKind,
				}
				for key, value := range want {
					if line[key] != value {
						t.Fatalf("completeness authority line %s = %v, want %v\nline=%v", key, line[key], value, line)
					}
				}
			})
		}
	}
}

func teamKind() *cf.SubjectKind { kind := cf.SubjectTeam; return &kind }
