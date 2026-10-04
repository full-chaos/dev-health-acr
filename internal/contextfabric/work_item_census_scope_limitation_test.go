package contextfabric

import (
	"context"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// censusScopeGraph is the acceptance graph double with one behaviour added: when
// the caller named a repository it reports, as the resolver does after the work
// item census ran inside that scope, that the scope was applied.
type censusScopeGraph struct{ *acceptanceGraphReader }

func (g censusScopeGraph) ResolveSubjects(ctx context.Context, principal storage.Principal, request InvestigationRequest, interpreted InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, anchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	if len(request.RequestedScope.RepositorySlugs) > 0 {
		RecordWorkItemCensusRepositoryScope(ctx)
	}
	return g.acceptanceGraphReader.ResolveSubjects(ctx, principal, request, interpreted, binding, confirmedKind, confirmedAnchor, frame, anchorKind)
}

func hasCensusScopeLimitation(result InvestigationResult) bool {
	return slices.Contains(result.Limitations, contractsv1.ContextFabricWorkItemCensusRepositoryScopeLimitation)
}

func TestInvestigateStatesTheWorkItemCensusRepositoryScopeOnEveryTerminal(t *testing.T) {
	t.Parallel()
	project := acceptanceProject()
	foundGraph := func() *acceptanceGraphReader {
		return &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
			context:    bootstrapGraphContext(project),
		}
	}
	notFoundGraph := func() *acceptanceGraphReader {
		return &acceptanceGraphReader{
			resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}},
			context:    emptyGraphContext(),
		}
	}
	for _, tc := range []struct {
		name      string
		graph     func() *acceptanceGraphReader
		scoped    bool
		wantLine  bool
		wantState InvestigationStatus
	}{
		{"found, scope given", foundGraph, true, true, InvestigationComplete},
		{"found, no scope", foundGraph, false, false, InvestigationComplete},
		{"not found, scope given", notFoundGraph, true, true, InvestigationNoMatch},
		{"not found, no scope", notFoundGraph, false, false, InvestigationNoMatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			graph := censusScopeGraph{tc.graph()}
			var engine *Engine
			if tc.wantState == InvestigationComplete {
				facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
					return bootstrapFactBundle(project), nil
				})
				engine = buildAcceptanceEngine(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), newMapResultStore())
			} else {
				engine = buildTerminalEngine(t, graph, nil)
			}
			request := validInvestigationRequestWithConfirmedWindow()
			if tc.scoped {
				request.RequestedScope.RepositorySlugs = []string{"acme/repo-25"}
			}
			result, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if result.Status != tc.wantState {
				t.Fatalf("Status = %q, want %q", result.Status, tc.wantState)
			}
			if got := hasCensusScopeLimitation(result); got != tc.wantLine {
				t.Fatalf("scope limitation present = %v, want %v; limitations = %q", got, tc.wantLine, result.Limitations)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}
