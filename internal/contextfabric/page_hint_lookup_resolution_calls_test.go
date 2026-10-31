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
