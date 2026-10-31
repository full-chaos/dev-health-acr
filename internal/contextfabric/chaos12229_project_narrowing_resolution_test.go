package contextfabric

import (
	"context"
	"errors"
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12229: a requested project narrowing is resolved to canonical
// identities once per investigation, before the first graph read, and every
// graph read sees the resolved scope.

// resolvingGraph is the acceptance graph with the optional resolver.
type resolvingGraph struct {
	*acceptanceGraphReader
	narrowing *contractsv1.ContextFabricProjectNarrowing
	err       error
	calls     int
	asked     []string
}

func (g *resolvingGraph) ResolveProjectNarrowing(_ context.Context, _ storage.Principal, _ ResolvedGraphBinding, projectIDs []string) (*contractsv1.ContextFabricProjectNarrowing, error) {
	g.calls++
	g.asked = append([]string(nil), projectIDs...)
	return g.narrowing, g.err
}

func projectNarrowedInvestigation(t *testing.T, graph GraphReader, projectIDs ...string) (*recordingTelemetry, error) {
	t.Helper()
	project := acceptanceProject()
	facts := factReaderFunc(func(context.Context, storage.Principal, CanonicalFactRequest) (CanonicalFactBundle, error) {
		return bootstrapFactBundle(project), nil
	})
	telemetry := &recordingTelemetry{}
	engine := buildAcceptanceEngineWithTelemetry(t, graph, facts, bootstrapInterpretation(), bootstrapDraft(project), nil, telemetry)
	request := validInvestigationRequestWithConfirmedWindow()
	request.RequestedScope.ProjectIDs = projectIDs
	_, err := engine.Investigate(context.Background(), acceptancePrincipal(), request)
	return telemetry, err
}

func acceptanceGraphFor() *acceptanceGraphReader {
	project := acceptanceProject()
	return &acceptanceGraphReader{
		resolution: SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{project}},
		context:    bootstrapGraphContext(project),
	}
}

func TestARequestedProjectNarrowingIsResolvedOnceAndReachesTheGraphReads(t *testing.T) {
	t.Parallel()
	narrowing := &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}
	graph := &resolvingGraph{acceptanceGraphReader: acceptanceGraphFor(), narrowing: narrowing}
	telemetry, err := projectNarrowedInvestigation(t, graph, "proj-1")
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if graph.calls != 1 || len(graph.asked) != 1 || graph.asked[0] != "proj-1" {
		t.Fatalf("resolver calls = %d with %v, want one call for [proj-1]", graph.calls, graph.asked)
	}
	if got := graph.lastRequest.RequestedScope.ProjectNarrowing; got != narrowing {
		t.Fatalf("the graph read saw ProjectNarrowing %+v, want the resolved narrowing", got)
	}
	if len(telemetry.projectNarrowingResolutions) != 1 {
		t.Fatalf("resolution lines = %+v, want one", telemetry.projectNarrowingResolutions)
	}
	if got := telemetry.projectNarrowingResolutions[0]; got.Outcome != ProjectNarrowingResolved || got.RequestedIDs != 1 || got.ResolvedIdentities != 1 {
		t.Fatalf("resolution line = %+v, want resolved, 1 requested, 1 identity", got)
	}
}

func TestAnUnresolvedProjectNarrowingStaysOnTheUnverifiedPath(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		graph   GraphReader
		outcome ProjectNarrowingOutcome
	}{
		"no resolver":              {acceptanceGraphFor(), ProjectNarrowingResolverAbsent},
		"the resolver fails":       {&resolvingGraph{acceptanceGraphReader: acceptanceGraphFor(), err: errors.New("graph down")}, ProjectNarrowingFailed},
		"an incomplete resolution": {&resolvingGraph{acceptanceGraphReader: acceptanceGraphFor(), narrowing: &contractsv1.ContextFabricProjectNarrowing{Identities: []string{}, Complete: false}}, ProjectNarrowingIncomplete},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			telemetry, err := projectNarrowedInvestigation(t, tc.graph, "proj-1")
			if err != nil {
				t.Fatalf("Investigate() error = %v", err)
			}
			if len(telemetry.projectNarrowingResolutions) != 1 || telemetry.projectNarrowingResolutions[0].Outcome != tc.outcome {
				t.Fatalf("resolution lines = %+v, want one %q", telemetry.projectNarrowingResolutions, tc.outcome)
			}
		})
	}
}

// No narrowing, or a requested "*" (it narrows nothing): no resolution and
// no line.
func TestNoProjectNarrowingIsNotResolved(t *testing.T) {
	t.Parallel()
	for name, ids := range map[string][]string{"none": nil, "a requested wildcard": {"*"}, "a requested wildcard beside an id": {"*", "proj-1"}} {
		graph := &resolvingGraph{acceptanceGraphReader: acceptanceGraphFor()}
		telemetry, err := projectNarrowedInvestigation(t, graph, ids...)
		if err != nil {
			t.Fatalf("%s: Investigate() error = %v", name, err)
		}
		if graph.calls != 0 || len(telemetry.projectNarrowingResolutions) != 0 {
			t.Fatalf("%s: resolver calls %d, lines %+v, want none", name, graph.calls, telemetry.projectNarrowingResolutions)
		}
	}
}

// A scope already resolved is not resolved again.
func TestAResolvedScopeIsNotResolvedAgain(t *testing.T) {
	t.Parallel()
	graph := &resolvingGraph{acceptanceGraphReader: acceptanceGraphFor(), narrowing: &contractsv1.ContextFabricProjectNarrowing{Complete: true}}
	telemetry := &recordingTelemetry{}
	engine := &Engine{graph: graph, telemetry: telemetry}
	preset := &contractsv1.ContextFabricProjectNarrowing{Identities: []string{"project.v2:linear:proj-1"}, Complete: true}
	scope := engine.resolveRequestedProjectNarrowing(context.Background(), acceptancePrincipal(), ResolvedGraphBinding{}, RequestedScope{ProjectIDs: []string{"proj-1"}, ProjectNarrowing: preset})
	if scope.ProjectNarrowing != preset || graph.calls != 0 || len(telemetry.projectNarrowingResolutions) != 0 {
		t.Fatalf("narrowing %+v, resolver calls %d, lines %d: want the preset narrowing, no call, no line", scope.ProjectNarrowing, graph.calls, len(telemetry.projectNarrowingResolutions))
	}
}

// Review r1 P2: the repository work-item walk (the tuple path) reports a
// member it admitted only on its "*" project list, and the tuple read
// discloses it in its graph coverage (degrading, partial), as the general
// graph read does; the served status is then floored (CHAOS-12242).
func TestTheTuplePathDisclosesAnUnverifiedProjectNarrowing(t *testing.T) {
	for name, unverified := range map[string]bool{"an unverified member": true, "every member proven": false} {
		t.Run(name, func(t *testing.T) {
			gate, _ := NewWorkItemMembershipGate(1, 0)
			ctx, owner := NewWorkItemResponseOwnerContext(context.Background())
			defer owner.Complete()
			graph := &treeGraphFake{walk: TreeWorkItemWalk{
				Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}, PullRequests: 1, LinkedIssues: 1,
				ProjectNarrowingUnverified: unverified,
			}}
			engine := &Engine{treeWorkItemMembership: NewTreeWorkItemMembership(graph, &treeFilterFake{}, gate), telemetry: &recordingTelemetry{}}
			request := InvestigationRequest{RequestedScope: RequestedScope{ProjectIDs: []string{"proj-1"}}}
			got, _, err := engine.discoverWorkItemTuple(ctx, storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, request, ResolvedGraphBinding{}, repositoryAnchoredResolution(repositoryWorkItemAnchor), &AnswerPlan{}, workItemMemberFilter{})
			if err != nil {
				t.Fatalf("discoverWorkItemTuple() error = %v", err)
			}
			if graph.calls != 1 || got.Cohort == nil {
				t.Fatalf("premise: walk calls %d, cohort %+v: the tuple read did not measure", graph.calls, got.Cohort)
			}
			disclosed := false
			for _, detail := range got.Coverage.Details {
				if detail.Code == contractsv1.ContextFabricCoverageDetailReadPopulationUnverified && detail.Raw == ProjectNarrowingUnverifiedRaw && detail.Degrading && detail.Label != "" {
					disclosed = true
				}
			}
			if disclosed != unverified || got.Coverage.Partial != unverified || slices.Contains(got.Coverage.DegradedReasons, ProjectNarrowingUnverifiedRaw) != unverified {
				t.Fatalf("coverage = %+v, want the unverified narrowing disclosed = %v", got.Coverage, unverified)
			}
		})
	}
}
