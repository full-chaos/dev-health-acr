package contextfabric

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12230: graphrank writes one page_hint_lookup line per resolveSubjects
// call that reaches its hint loop with an ask_dev_page_context hint
// (graphrank/page_hint_lookup_test.go). The line is one per REQUEST because one
// Investigate hands the caller's page hints to the graph exactly once: the
// class-default window gate runs the offers-only resolution and RETURNS
// (engine.go, the gatedOfferMaterial branch), and every other request runs the
// decisive resolution only. The engine's two other ResolveSubjects calls
// replace the hints with engine-minted ones: the cohort group read
// (hintsource.CohortGroupAuthorization) and the answer-reuse recheck
// (hintsource.AnswerReuseAuthorizationRecheck, reached only when every request
// hint is committed, so its subject list is never empty). This test pins the
// exactly-once property on both paths, so a change that resolves the same
// page hints twice in one request fails here.

func pageContextHints() []SubjectHint {
	return []SubjectHint{
		{Kind: SubjectRepository, ID: "repository:5a1d7e44-0000-4000-8000-000000000001", Label: "dev-health-acr", Source: hintsource.AskDevPageContext},
		{Kind: SubjectTeam, ID: "team:platform", Source: hintsource.AskDevPageContext},
	}
}

func TestOneRequestHandsItsPageHintsToTheGraphExactlyOnce(t *testing.T) {
	t.Parallel()

	t.Run("gated: the offers-only resolution only", func(t *testing.T) {
		t.Parallel()
		graph := &acceptanceGraphReader{resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, context: emptyGraphContext()}
		engine := buildWindowGateEngine(t, &countingInterpreter{interpretation: bootstrapInterpretation()}, graph, newMapResultStore())
		request := validInvestigationRequest() // no window: the class-default gate fires
		request.RequestedScope.SubjectHints = pageContextHints()

		result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
		if err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if result.Status != InvestigationClarificationRequired {
			t.Fatalf("Status = %q, want clarification_required: the fixture must take the gated path", result.Status)
		}
		if graph.resolveCalls != 1 || !OffersOnlyResolution(graph.resolveCtxs[0]) {
			t.Fatalf("ResolveSubjects calls = %d (offers-only: %v), want exactly one, offers-only", graph.resolveCalls, offersOnlyMarks(graph.resolveCtxs))
		}
		if !reflect.DeepEqual(graph.lastRequest.RequestedScope.SubjectHints, pageContextHints()) {
			t.Fatalf("the offers-only resolution got hints %#v, want the request's page hints", graph.lastRequest.RequestedScope.SubjectHints)
		}
	})

	t.Run("decisive: the decisive resolution only", func(t *testing.T) {
		t.Parallel()
		project := acceptanceProject()
		graph := &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
		}
		facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
			return bootstrapFactBundle(project), nil
		})
		engine := buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore())
		request := validInvestigationRequestWithConfirmedWindow() // a confirmed window: no gate
		request.RequestedScope.SubjectHints = pageContextHints()

		if _, err := engine.Investigate(context.Background(), acceptancePrincipal(), request); err != nil {
			t.Fatalf("Investigate() error = %v", err)
		}
		if graph.resolveCalls != 1 || OffersOnlyResolution(graph.resolveCtxs[0]) {
			t.Fatalf("ResolveSubjects calls = %d (offers-only: %v), want exactly one, decisive", graph.resolveCalls, offersOnlyMarks(graph.resolveCtxs))
		}
		if !reflect.DeepEqual(graph.lastRequest.RequestedScope.SubjectHints, pageContextHints()) {
			t.Fatalf("the decisive resolution got hints %#v, want the request's page hints", graph.lastRequest.RequestedScope.SubjectHints)
		}
	})
}

func offersOnlyMarks(ctxs []context.Context) []bool {
	marks := make([]bool, len(ctxs))
	for i, ctx := range ctxs {
		marks[i] = OffersOnlyResolution(ctx)
	}
	return marks
}

// hintRecordingAcceptanceGraph records the subject hints of every
// ResolveSubjects call, so a test can count the calls that carry page hints.
type hintRecordingAcceptanceGraph struct {
	*acceptanceGraphReader
	hintsPerCall [][]SubjectHint
}

func (g *hintRecordingAcceptanceGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.hintsPerCall = append(g.hintsPerCall, append([]SubjectHint(nil), request.RequestedScope.SubjectHints...))
	return g.acceptanceGraphReader.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, anchorKind)
}

// AuthorizeStoredSubjects admits every stored subject, as graphReaderStub's
// does, so the stored answer reaches the reuse rules under test.
func (g *hintRecordingAcceptanceGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	return graphReaderStub{}.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

func (g *hintRecordingAcceptanceGraph) callsBySource(source string) int {
	calls := 0
	for _, hints := range g.hintsPerCall {
		for _, hint := range hints {
			if hint.Source == source {
				calls++
				break
			}
		}
	}
	return calls
}

// The answer-reuse recheck never carries the caller's page hints, so a reuse
// miss followed by the fresh resolution still hands them to the graph once.
// The recheck keeps the request's own hints only when the stored answer has
// no subject to recheck (answer_reuse.go reuseAuthorizationStillHolds), and it
// is reached only after requestHintsCommittedBy (answer_reuse.go, tryReuse):
// every request hint must be one of the stored answer's committed subjects. A
// request with a page hint therefore misses before the recheck when the stored
// answer committed nothing, and otherwise rechecks the stored subjects under
// hintsource.AnswerReuseAuthorizationRecheck.
func TestAReuseMissDoesNotHandThePageHintsToTheGraphTwice(t *testing.T) {
	t.Parallel()
	hinted := func() []SubjectRef {
		var subjects []SubjectRef
		for _, hint := range pageContextHints() {
			subjects = append(subjects, SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.Label})
		}
		return subjects
	}
	for _, tc := range []struct {
		name         string
		committed    []SubjectRef
		wantOutcome  AnswerReuseOutcome
		wantRechecks int
	}{
		// The confirmed-empty stored answer: refused before the recheck.
		{name: "stored answer committed nothing", committed: []SubjectRef{}, wantOutcome: AnswerReuseMissHintNotCommitted, wantRechecks: 0},
		// The stored answer committed the page subjects; the recheck runs on
		// engine-minted hints, finds nothing, and the request goes fresh.
		{name: "recheck fails", committed: hinted(), wantOutcome: AnswerReuseMissAuthorization, wantRechecks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, candidate := reusableCandidate()
			candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: tc.committed}
			// The request's confirmed trailing-90-day window, so the window
			// rules let the stored answer through to the recheck.
			candidate.EffectiveEvidenceWindow = &EffectiveEvidenceWindow{RelativeID: RelativeWindowTrailing90D, Provenance: WindowQuestionStated}
			graph := &hintRecordingAcceptanceGraph{acceptanceGraphReader: &acceptanceGraphReader{
				resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
				context:    emptyGraphContext(),
			}}
			telemetry := &recordingTelemetry{}
			project := acceptanceProject()
			runtime := fakeModelRuntime{interpreted: bootstrapInterpretation(), draft: bootstrapDraft(project), receipt: acceptanceReceipt()}
			engine, err := NewEngine(EngineDependencies{
				Interpreter: RuntimeQuestionInterpreter{Runtime: runtime},
				Graph:       graph,
				Facts: factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return bootstrapFactBundle(project), nil
				}),
				Synthesizer: RuntimeAnswerSynthesizer{Runtime: runtime, Options: RuntimeAnswerSynthesizerOptions{ServiceVersion: "acceptance-test", Backend: "graph"}},
				Results:     newMapResultStore(),
				Telemetry:   telemetry,
				ReuseGate: reuseGateFunc(func(context.Context, storage.Principal, ReuseKey) (InvestigationResult, bool, error) {
					return candidate, true, nil
				}),
			}, EngineOptions{ServiceVersion: "acceptance-test", NewResultID: func() string { return freshTestResultID }})
			if err != nil {
				t.Fatalf("NewEngine() error = %v", err)
			}
			request := validInvestigationRequestWithConfirmedWindow()
			request.RequestedScope.SubjectHints = pageContextHints()

			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Reused || !reuseOutcomeSlicesEqual(telemetry.answerReuseOutcomes, []AnswerReuseOutcome{tc.wantOutcome}) {
				t.Fatalf("reused=%v outcomes=%v, want a %s miss that goes fresh", result.Reused, telemetry.answerReuseOutcomes, tc.wantOutcome)
			}
			if got := graph.callsBySource(string(hintsource.AnswerReuseAuthorizationRecheck)); got != tc.wantRechecks {
				t.Fatalf("recheck calls = %d, want %d (hints per call: %#v)", got, tc.wantRechecks, graph.hintsPerCall)
			}
			if got := graph.callsBySource(hintsource.AskDevPageContext); got != 1 {
				t.Fatalf("ResolveSubjects calls carrying page hints = %d, want exactly 1 (hints per call: %#v)", got, graph.hintsPerCall)
			}
		})
	}
}
