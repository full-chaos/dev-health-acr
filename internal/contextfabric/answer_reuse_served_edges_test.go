package contextfabric

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// servedEdgeCall is what one ServedEdgeEvidence call was asked.
type servedEdgeCall struct {
	edges   []RelationshipEdge
	request InvestigationRequest
	binding ResolvedGraphBinding
	when    TimeContext
}

// servedEdgeGraph is the recheck double with the direct read of CHAOS-12537.
// Its read proves an edge's refs unless the edge is refused, as the walk's own
// gates would refuse it; it never proves a ref no served edge carries.
type servedEdgeGraph struct {
	*recheckGraph
	// refused names the edges the gates refuse, by "from>to" canonical ids.
	refused map[string]struct{}
	cut     bool
	err     error
	hang    bool
	calls   []servedEdgeCall
}

func servedEdgeKey(edge RelationshipEdge) string {
	return edge.From.CanonicalID + ">" + edge.To.CanonicalID
}

func (g *servedEdgeGraph) ServedEdgeEvidence(ctx context.Context, _ storage.Principal, request GraphDiscoveryRequest, edges []RelationshipEdge) (ServedEdgeProof, error) {
	g.read(ctx)
	deadline, _ := ctx.Deadline()
	g.deadlines = append(g.deadlines, deadline)
	g.calls = append(g.calls, servedEdgeCall{
		edges: append([]RelationshipEdge(nil), edges...), request: request.Request, binding: request.Binding, when: request.Interpretation.TimeContext,
	})
	if g.hang {
		return ServedEdgeProof{}, g.recheckGraph.hang(ctx)
	}
	proof := ServedEdgeProof{Context: GraphContext{EvidenceRefIDs: []string{}}}
	for _, edge := range edges {
		if _, refused := g.refused[servedEdgeKey(edge)]; refused {
			continue
		}
		proof.Context.EvidenceRefIDs = append(proof.Context.EvidenceRefIDs, edge.EvidenceRefIDs...)
	}
	// A failed or cut read still hands back everything it saw, so a caller
	// that ignored the error or the cut would serve from it, and a test sees.
	if g.err != nil {
		return proof, g.err
	}
	proof.Cut = g.cut
	return proof, nil
}

// servedEdgeNoRemovedTeams is a graph with the direct read and no removed-team
// read: GraphReader's own methods and ServedEdgeEvidence, nothing else.
type servedEdgeNoRemovedTeams struct {
	GraphReader
	served *servedEdgeGraph
}

func (g servedEdgeNoRemovedTeams) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	return g.served.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
}

func (g servedEdgeNoRemovedTeams) ServedEdgeEvidence(ctx context.Context, principal storage.Principal, request GraphDiscoveryRequest, edges []RelationshipEdge) (ServedEdgeProof, error) {
	return g.served.ServedEdgeEvidence(ctx, principal, request, edges)
}

// servedEdgeCandidate is a stored answer over these subjects that serves one
// path between each neighbouring pair, cites every path's ref, and so needs a
// recheck of its evidence. The last path runs into the last batch.
func servedEdgeCandidate(subjects []SubjectRef) InvestigationResult {
	candidate := recheckCandidate(subjects)
	candidate.Paths = nil
	candidate.EvidenceRefIDs = nil
	for index := 0; index+1 < len(subjects); index++ {
		ref := evidenceRef(index)
		edge := RelationshipEdge{From: subjects[index], To: subjects[index+1], Type: "depends_on", EvidenceRefIDs: []string{ref}}
		candidate.Paths = append(candidate.Paths, RelationshipPath{
			PathID: "path-" + subjects[index].CanonicalID, Nodes: []SubjectRef{edge.From, edge.To}, Edges: []RelationshipEdge{edge}, EvidenceRefIDs: []string{ref},
		})
		candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, ref)
	}
	return candidate
}

// walkSees makes the walk double prove every path ref of the candidate, as a
// full discovery over every subject would.
func walkSees(graph *recheckGraph, subjects []SubjectRef) {
	graph.pathRefs = map[string]string{}
	for index := 0; index+1 < len(subjects); index++ {
		graph.pathRefs[subjects[index].CanonicalID] = evidenceRef(index)
	}
}

// TestADirectProofOfTheServedEdgesReplacesTheWalk (CHAOS-12537) is the reason
// for the change. A hit of 54 subjects used to walk two discoveries; it now
// rechecks every subject in its two batches, proves the served edges in ONE
// direct read, and walks nothing. The read gets every served edge, the
// recheck's own options with no hint, the turn's binding and the answer's own
// time, under the recheck's deadline.
func TestADirectProofOfTheServedEdgesReplacesTheWalk(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	candidate := servedEdgeCandidate(subjects)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{}}
	run := runRecheck(t, graph, candidate)
	run.requireHit(t)
	t.Logf("subjects=54 ResolveSubjects=%d ServedEdgeEvidence=%d DiscoverContext=%d", len(graph.resolves), len(graph.calls), len(graph.discovers))
	if got := graph.hintedSizes(); !slices.Equal(got, []int{50, 4}) {
		t.Fatalf("resolve batches = %v, want [50 4]: every subject is still rechecked", got)
	}
	if len(graph.discovers) != 0 {
		t.Fatalf("DiscoverContext calls = %d, want 0: the served edges were proven directly", len(graph.discovers))
	}
	if len(graph.calls) != 1 {
		t.Fatalf("ServedEdgeEvidence calls = %d, want 1", len(graph.calls))
	}
	call := graph.calls[0]
	if len(call.edges) != len(candidate.Paths) {
		t.Fatalf("served edges read = %d, want every one of %d", len(call.edges), len(candidate.Paths))
	}
	if call.request.Options != reuseRecheckOptions || len(call.request.RequestedScope.SubjectHints) != 0 {
		t.Fatalf("the read left the recheck's options or carried hints: %+v", call.request)
	}
	if call.binding != (ResolvedGraphBinding{GraphKey: "stub-key", Epoch: 0}) {
		t.Fatalf("read binding = %+v, want the one binding of the turn", call.binding)
	}
	if call.when != candidate.Interpretation.TimeContext {
		t.Fatalf("read time = %+v, want the stored answer's own", call.when)
	}
	if graph.deadlines[len(graph.deadlines)-1].IsZero() {
		t.Fatal("the direct read carried no deadline: it must run under the recheck's budget")
	}
}

// TestTheWalkDecidesWhatTheDirectReadCannotProve (CHAOS-12537): the direct read
// only ever ends a recheck with a hit. In each case below it proves nothing, so
// the walk runs exactly as before, and its verdict is the answer's.
func TestTheWalkDecidesWhatTheDirectReadCannotProve(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	for _, tc := range []struct {
		name     string
		setup    func(*servedEdgeGraph, *InvestigationResult)
		walkSees bool
		want     AnswerReuseOutcome
		read     int
	}{
		{
			name: "a ref no served edge carries, which the walk proves",
			setup: func(_ *servedEdgeGraph, candidate *InvestigationResult) {
				candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, evidenceRef(999))
			},
			walkSees: true, want: AnswerReuseHit, read: 1,
		},
		{
			name: "a ref no served edge carries, which the walk cannot prove either",
			setup: func(_ *servedEdgeGraph, candidate *InvestigationResult) {
				candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, evidenceRef(999))
			},
			want: AnswerReuseMissEvidenceContainment, read: 1,
		},
		{
			name: "an edge the gates now refuse, in the last batch",
			setup: func(g *servedEdgeGraph, _ *InvestigationResult) {
				g.refused = map[string]struct{}{servedEdgeKey(RelationshipEdge{From: subjects[52], To: subjects[53]}): {}}
			},
			want: AnswerReuseMissEvidenceContainment, read: 1,
		},
		{
			name:     "the read reached its row limit",
			setup:    func(g *servedEdgeGraph, _ *InvestigationResult) { g.cut = true },
			walkSees: true, want: AnswerReuseHit, read: 1,
		},
		{
			name:     "the read failed",
			setup:    func(g *servedEdgeGraph, _ *InvestigationResult) { g.err = errors.New("graph read failed") },
			walkSees: true, want: AnswerReuseHit, read: 1,
		},
		{
			name: "the removed-team read failed",
			setup: func(g *servedEdgeGraph, _ *InvestigationResult) {
				g.removedErr = errors.New("removed team read failed")
			},
			walkSees: true, want: AnswerReuseHit, read: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate := servedEdgeCandidate(subjects)
			graph := &servedEdgeGraph{recheckGraph: &recheckGraph{}}
			tc.setup(graph, &candidate)
			if tc.name == "the removed-team read failed" {
				team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team-1", Label: "Team 1"}
				candidate.SubjectResolution.Committed = append(candidate.SubjectResolution.Committed, team)
				candidate.SubjectResolution.CommitDecisionDigests = identityProvenDigests(candidate.SubjectResolution.Committed...)
			}
			if tc.walkSees {
				// The walk double proves one ref per walked subject: each
				// path's ref through its source, and the extra ref through the
				// last subject, which starts no path.
				walkSees(graph.recheckGraph, subjects)
				graph.pathRefs[subjects[53].CanonicalID] = evidenceRef(999)
			}
			run := runRecheck(t, graph, candidate)
			if len(graph.calls) != tc.read {
				t.Fatalf("ServedEdgeEvidence calls = %d, want %d", len(graph.calls), tc.read)
			}
			if len(graph.discovers) == 0 {
				t.Fatal("the walk did not run: what the direct read cannot prove must be left to it")
			}
			if tc.want == AnswerReuseHit {
				if !run.hit() {
					t.Fatalf("outcome = %v, want the walk's hit", run.telemetry.answerReuseOutcomes)
				}
				return
			}
			if run.hit() {
				t.Fatal("a stored answer was served, want the walk's refusal")
			}
			if got := run.telemetry.answerReuseOutcomes; len(got) != 1 || got[0] != tc.want {
				t.Fatalf("outcome = %v, want [%s] from the walk", got, tc.want)
			}
		})
	}
}

// TestANarrowedRequestTakesTheWalk (CHAOS-12537): a request that narrows its
// scope never reaches the direct read, because only the walk discloses an
// unverified narrowing. A "*" or a blank narrows nothing.
func TestANarrowedRequestTakesTheWalk(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		scope   RequestedScope
		narrows bool
	}{
		{name: "repository", scope: RequestedScope{RepositorySlugs: []string{"acme/api"}}, narrows: true},
		{name: "project", scope: RequestedScope{ProjectIDs: []string{"proj-1"}}, narrows: true},
		{name: "team", scope: RequestedScope{TeamIDs: []string{"team-1"}}, narrows: true},
		{name: "a resolved project narrowing", scope: RequestedScope{ProjectNarrowing: &contractsv1.ContextFabricProjectNarrowing{}}, narrows: true},
		{name: "star", scope: RequestedScope{ProjectIDs: []string{"*"}, TeamIDs: []string{" * "}}},
		{name: "blank", scope: RequestedScope{RepositorySlugs: []string{" "}}},
		{name: "none"},
	} {
		if got := requestedScopeNarrows(tc.scope); got != tc.narrows {
			t.Fatalf("%s: requestedScopeNarrows = %t, want %t", tc.name, got, tc.narrows)
		}
	}
}

// TestALostSubjectInTheLastBatchStillRefuses (CHAOS-12537 acceptance): a
// subject the caller lost access to, in the last batch, refuses the answer
// before any evidence read, direct or walked.
func TestALostSubjectInTheLastBatchStillRefuses(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{denied: map[string]struct{}{subjects[53].CanonicalID: {}}}}
	run := runRecheck(t, graph, servedEdgeCandidate(subjects))
	run.requireMiss(t, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectNotCommitted})
	if len(graph.calls) != 0 || len(graph.discovers) != 0 {
		t.Fatalf("evidence was read after a refused subject: direct=%d walked=%d", len(graph.calls), len(graph.discovers))
	}
}

// TestADirectReadPastTheBudgetIsTheDeadlineMiss (CHAOS-12537): the direct read
// runs under the recheck's one budget. A read that outlives it is the named
// deadline miss, never a hit and never an authorization word, and the walk does
// not start.
func TestADirectReadPastTheBudgetIsTheDeadlineMiss(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{}, hang: true}
	run := runBudgeted(t, context.Background(), graph, servedEdgeCandidate(subjects), 50*time.Millisecond)
	if run.hit() {
		t.Fatal("a stored answer was served after the budget ended")
	}
	if got := run.telemetry.answerReuseOutcomes; len(got) != 1 || got[0] != AnswerReuseMissRecheckDeadline {
		t.Fatalf("outcome = %v, want [%s]", got, AnswerReuseMissRecheckDeadline)
	}
	if len(graph.discovers) != 0 {
		t.Fatalf("the walk ran after the budget ended: %d discoveries", len(graph.discovers))
	}
}

// TestADirectHitStatesARemovedTeam (CHAOS-12537): a direct hit still learns
// which committed team is removed at its source, by the bounded removed-team
// read, as the walk's discovery would have told it.
func TestADirectHitStatesARemovedTeam(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team-removed", Label: "Removed team"}
	candidate := servedEdgeCandidate(subjects)
	candidate.SubjectResolution.Committed = append(candidate.SubjectResolution.Committed, team)
	candidate.SubjectResolution.CommitDecisionDigests = identityProvenDigests(candidate.SubjectResolution.Committed...)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{removed: map[string]struct{}{team.CanonicalID: {}}}}
	run := runRecheck(t, graph, candidate)
	run.requireHit(t)
	if len(graph.discovers) != 0 || len(graph.removedFor) != 1 {
		t.Fatalf("walked=%d removed-team reads=%d, want 0 and 1", len(graph.discovers), len(graph.removedFor))
	}
	if !slices.ContainsFunc(run.result.SubjectResolution.Committed, func(s SubjectRef) bool { return s == team }) {
		t.Fatal("the removed team left the served answer's commits; it is disclosed, not dropped")
	}
	if len(run.result.Limitations) == len(candidate.Limitations) {
		t.Fatal("a direct hit served a removed team with no disclosure")
	}
}

// TestACitationACandidateRowCarriesIsProvenByTheBatches (CHAOS-12537): a
// stored citation that a subject's candidate row carries, in the last batch, is
// proven by the batch that recommitted that subject. The direct read adds the
// served edges' refs to that, so this answer needs no walk either.
func TestACitationACandidateRowCarriesIsProvenByTheBatches(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	candidate := servedEdgeCandidate(subjects)
	nodeRef := evidenceRef(5300)
	candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, nodeRef)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{candidateRefs: map[string][]string{subjects[53].CanonicalID: {nodeRef}}}}
	run := runRecheck(t, graph, candidate)
	run.requireHit(t)
	if len(graph.discovers) != 0 {
		t.Fatalf("DiscoverContext calls = %d, want 0: the batch's candidate row proves the citation", len(graph.discovers))
	}
}

// TestANarrowedRequestNeverReachesTheDirectRead (CHAOS-12537) is the call site
// of requestedScopeNarrows: a request narrowed by repository goes to the walk,
// which alone discloses a narrowing, and the direct read is not asked.
func TestANarrowedRequestNeverReachesTheDirectRead(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	graph := &servedEdgeGraph{recheckGraph: &recheckGraph{}}
	walkSees(graph.recheckGraph, subjects)
	request := validInvestigationRequest()
	request.RequestedScope = RequestedScope{RepositorySlugs: []string{"acme/tools"}}
	telemetry := &recordingTelemetry{}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{}, errors.New("fresh investigation reached")
		}),
		Graph: graph, Results: &resultStoreStub{}, Telemetry: telemetry,
		ReuseGate: optionalOwnerReuseGate{candidate: servedEdgeCandidate(subjects)},
	})
	_, _ = engine.Investigate(context.Background(), ownerPrincipal("user_a", "acme/tools"), request)
	if len(graph.resolves) == 0 {
		t.Fatalf("no recheck ran (outcomes %v): this case tests nothing", telemetry.answerReuseOutcomes)
	}
	if len(graph.calls) != 0 {
		t.Fatalf("a narrowed request reached the direct read %d times", len(graph.calls))
	}
	if len(graph.discovers) == 0 {
		t.Fatal("a narrowed request did not take the walk")
	}
}

// TestATeamAnswerOnAGraphWithNoRemovedTeamReadTakesTheWalk (CHAOS-12537): the
// walk is what states a removed team when the graph has no bounded read for it.
func TestATeamAnswerOnAGraphWithNoRemovedTeamReadTakesTheWalk(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	team := SubjectRef{Kind: SubjectTeam, CanonicalID: "team-1", Label: "Team 1"}
	candidate := servedEdgeCandidate(subjects)
	candidate.SubjectResolution.Committed = append(candidate.SubjectResolution.Committed, team)
	candidate.SubjectResolution.CommitDecisionDigests = identityProvenDigests(candidate.SubjectResolution.Committed...)
	served := &servedEdgeGraph{recheckGraph: &recheckGraph{}}
	walkSees(served.recheckGraph, subjects)
	run := runRecheck(t, servedEdgeNoRemovedTeams{GraphReader: served, served: served}, candidate)
	run.requireHit(t)
	if len(served.calls) != 0 || len(served.discovers) == 0 {
		t.Fatalf("direct=%d walked=%d: a team answer with no removed-team read must take the walk", len(served.calls), len(served.discovers))
	}
}
