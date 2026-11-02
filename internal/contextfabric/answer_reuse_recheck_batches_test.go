package contextfabric

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// recheckResolveCall is what one ResolveSubjects call was asked.
type recheckResolveCall struct {
	hints           []SubjectHint
	options         InvestigationOptions
	binding         ResolvedGraphBinding
	confirmedKind   *ConfirmedExpectedKind
	confirmedAnchor *ConfirmedAnchorSelection
	frame           *QuestionFrame
	scopeAnchorKind SubjectKind
}

// recheckDiscoverCall is what one DiscoverContext call was asked.
type recheckDiscoverCall struct {
	committed []SubjectRef
	hints     []SubjectHint
	options   InvestigationOptions
	binding   ResolvedGraphBinding
	frame     *QuestionFrame
}

// recheckGraph is the graph double for the batched recheck. It honours the two
// limits of one call that the recheck has to work around:
//
//   - ResolveSubjects commits at most Options.MaxSubjectCandidates subjects and
//     drops the rest as it drops an unauthorized one, as the real caller-hint
//     exit does (graphrank.TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap).
//     A double that committed every hint would prove nothing here.
//   - DiscoverContext shares one Options.MaxRelationshipPaths budget across its
//     committed subjects, as graphrank.AdmitEdges does, so a subject past the
//     budget has no path and its evidence reads as not visible.
//
// It records every call, so a test asserts the reads and not only the outcome.
type recheckGraph struct {
	graphReaderStub
	// denied is keyed by canonical id. A denied subject resolves to nothing.
	denied map[string]struct{}
	// resolveErrOn is the 1-based ResolveSubjects call that fails; 0 is none.
	resolveErrOn int
	// discoverErrOn is the 1-based DiscoverContext call that fails; 0 is none.
	discoverErrOn int
	// unasked maps a 1-based ResolveSubjects call to the subjects it commits
	// without being asked. The real exit can do this: a census-attested subject
	// beside a scope anchor (graphrank's commitCensusAttestedBesideScopeAnchor).
	unasked map[int][]SubjectRef
	// candidateRefs are the evidence refs a subject's candidate row carries,
	// keyed by canonical id.
	candidateRefs map[string][]string
	// pathRefs is the evidence ref a subject's admitted path makes visible,
	// keyed by canonical id.
	pathRefs map[string]string
	// removed are the subjects discovery or the removed-team read reports
	// removed, keyed by canonical id.
	removed map[string]struct{}
	// narrowingUnverifiedFor makes discovery disclose an unverified project
	// narrowing when it walks this canonical id.
	narrowingUnverifiedFor string
	// gateDenied makes the stored-result gate withhold the answer, so the
	// recheck never runs.
	gateDenied bool
	// onRead runs at the start of every ResolveSubjects and DiscoverContext
	// call. A test uses it to advance a clock by what one read costs.
	onRead func()
	// hangResolveOn and hangDiscoverOn are the 1-based calls that block until
	// their context ends; hangRemoved does the same to the removed-team read.
	hangResolveOn, hangDiscoverOn int
	hangRemoved                   bool
	// afterHang is what a hung read returns once its context ended, in place of
	// the context's own error: a socket read reports its own timeout.
	afterHang error
	// hangPartial makes a hung read come back with no error and a partial
	// result, as a read that swallowed its failed lookups does.
	hangPartial bool
	// holdFor makes every read take this long and ignore its context.
	holdFor time.Duration
	// readDelay makes every read take this long, unless its context ends first.
	readDelay time.Duration
	// finishBefore makes the last discovery return this long before the
	// deadline of its context.
	finishBefore time.Duration
	// removedErr is what the removed-team read returns, at once.
	removedErr error
	// deadlines holds the deadline each read's context carried, zero for none.
	deadlines []time.Time

	resolves   []recheckResolveCall
	discovers  []recheckDiscoverCall
	removedFor [][]SubjectRef
}

// recheckHangGuard ends a hung read whose context never ends, so a recheck with
// no deadline fails its test rather than hanging the run.
const recheckHangGuard = 2 * time.Second

// errRecheckHangGuard reports a read that nothing bounded.
var errRecheckHangGuard = errors.New("read was never bounded by a deadline")

// read is the start of one graph read of the recheck.
func (g *recheckGraph) read(ctx context.Context) {
	if g.onRead != nil {
		g.onRead()
	}
	deadline, _ := ctx.Deadline()
	g.deadlines = append(g.deadlines, deadline)
	if g.holdFor > 0 {
		time.Sleep(g.holdFor)
	}
	if g.readDelay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(g.readDelay):
		}
	}
}

// hang blocks until ctx ends, and returns what such a read reports.
func (g *recheckGraph) hang(ctx context.Context) error {
	select {
	case <-ctx.Done():
		if g.afterHang != nil {
			return g.afterHang
		}
		return ctx.Err()
	case <-time.After(recheckHangGuard):
		return errRecheckHangGuard
	}
}

// AuthorizeStoredSubjects is the stored-result gate, ahead of the recheck.
func (g *recheckGraph) AuthorizeStoredSubjects(ctx context.Context, principal storage.Principal, binding ResolvedGraphBinding, subjects []SubjectRef) ([]StoredSubjectOutcome, error) {
	if !g.gateDenied {
		return g.graphReaderStub.AuthorizeStoredSubjects(ctx, principal, binding, subjects)
	}
	out := make([]StoredSubjectOutcome, len(subjects))
	for index := range out {
		out[index] = StoredSubjectDenied
	}
	return out, nil
}

func (g *recheckGraph) ResolveSubjects(ctx context.Context, _ storage.Principal, request InvestigationRequest, _ InterpretedQuestion, binding ResolvedGraphBinding, confirmedKind *ConfirmedExpectedKind, confirmedAnchor *ConfirmedAnchorSelection, frame *QuestionFrame, scopeAnchorKind SubjectKind) (SubjectResolution, StructureOfferMaterial, CommitBasisSet, CommitDecisionDigestSet, error) {
	g.read(ctx)
	g.resolves = append(g.resolves, recheckResolveCall{
		hints: append([]SubjectHint(nil), request.RequestedScope.SubjectHints...), options: request.Options, binding: binding,
		confirmedKind: confirmedKind, confirmedAnchor: confirmedAnchor, frame: frame, scopeAnchorKind: scopeAnchorKind,
	})
	call := len(g.resolves)
	if call == g.resolveErrOn {
		return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, errors.New("graph read failed")
	}
	if call == g.hangResolveOn {
		err := g.hang(ctx)
		if g.hangPartial {
			return SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}, StructureOfferMaterial{}, nil, nil, nil
		}
		return SubjectResolution{}, StructureOfferMaterial{}, nil, nil, err
	}
	committed := make([]SubjectRef, 0, len(request.RequestedScope.SubjectHints))
	for _, hint := range request.RequestedScope.SubjectHints {
		if _, refused := g.denied[hint.ID]; refused {
			continue
		}
		committed = append(committed, SubjectRef{Kind: hint.Kind, CanonicalID: hint.ID, Label: hint.Label})
	}
	if limit := request.Options.MaxSubjectCandidates; limit > 0 && len(committed) > limit {
		committed = committed[:limit]
	}
	committed = append(committed, g.unasked[call]...)
	candidates := make([]SubjectCandidate, 0, len(committed))
	for _, subject := range committed {
		candidates = append(candidates, SubjectCandidate{Subject: subject, EvidenceRefIDs: g.candidateRefs[subject.CanonicalID]})
	}
	return SubjectResolution{Candidates: candidates, Committed: committed}, StructureOfferMaterial{}, provenCommitBases(committed...), nil, nil
}

func (g *recheckGraph) DiscoverContext(ctx context.Context, _ storage.Principal, request GraphDiscoveryRequest) (GraphContext, error) {
	g.read(ctx)
	g.discovers = append(g.discovers, recheckDiscoverCall{
		committed: append([]SubjectRef(nil), request.Resolution.Committed...), hints: append([]SubjectHint(nil), request.Request.RequestedScope.SubjectHints...),
		options: request.Request.Options, binding: request.Binding, frame: request.Frame,
	})
	if len(g.discovers) == g.discoverErrOn {
		return GraphContext{}, errors.New("graph read failed")
	}
	if len(g.discovers) == g.hangDiscoverOn {
		err := g.hang(ctx)
		if !g.hangPartial {
			return GraphContext{}, err
		}
	}
	if deadline, ok := ctx.Deadline(); ok && g.finishBefore > 0 && len(g.discovers) == len(g.resolves) {
		time.Sleep(time.Until(deadline) - g.finishBefore)
	}
	discovered := GraphContext{Resolution: request.Resolution, EvidenceRefIDs: []string{}}
	for index, subject := range request.Resolution.Committed {
		if ref, ok := g.pathRefs[subject.CanonicalID]; ok && index < request.Request.Options.MaxRelationshipPaths {
			discovered.EvidenceRefIDs = append(discovered.EvidenceRefIDs, ref)
		}
		if _, gone := g.removed[subject.CanonicalID]; gone {
			discovered.RemovedTeams = append(discovered.RemovedTeams, subject)
		}
		if subject.CanonicalID == g.narrowingUnverifiedFor {
			discovered.Coverage.Details = append(discovered.Coverage.Details, detailFixture(contractsv1.ContextFabricCoverageDetailReadPopulationUnverified, "context-fabric:graph", true, ProjectNarrowingUnverifiedRaw, nil))
		}
	}
	return discovered, nil
}

// RemovedTeams is the bounded read of a stored answer that cites no evidence.
func (g *recheckGraph) RemovedTeams(ctx context.Context, _ storage.Principal, _ ResolvedGraphBinding, subjects []SubjectRef) ([]SubjectRef, error) {
	deadline, _ := ctx.Deadline()
	g.deadlines = append(g.deadlines, deadline)
	g.removedFor = append(g.removedFor, append([]SubjectRef(nil), subjects...))
	if g.removedErr != nil {
		return nil, g.removedErr
	}
	if g.hangRemoved {
		return nil, g.hang(ctx)
	}
	var removed []SubjectRef
	for _, subject := range subjects {
		if _, gone := g.removed[subject.CanonicalID]; gone {
			removed = append(removed, subject)
		}
	}
	return removed, nil
}

// hintedSizes is the size of each recorded resolution, in order.
func (g *recheckGraph) hintedSizes() []int {
	sizes := make([]int, 0, len(g.resolves))
	for _, call := range g.resolves {
		sizes = append(sizes, len(call.hints))
	}
	return sizes
}

// discoveredSizes is the committed size of each recorded discovery, in order.
func (g *recheckGraph) discoveredSizes() []int {
	sizes := make([]int, 0, len(g.discovers))
	for _, call := range g.discovers {
		sizes = append(sizes, len(call.committed))
	}
	return sizes
}

// recheckCandidate is a stored answer that names exactly these subjects.
func recheckCandidate(subjects []SubjectRef) InvestigationResult {
	_, candidate := reusableCandidate()
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: subjects, CommitDecisionDigests: identityProvenDigests(subjects...)}
	return candidate
}

// recheckRun is what one Investigate call did.
type recheckRun struct {
	result     InvestigationResult
	err        error
	telemetry  *recordingTelemetry
	modelCalls int
}

func (r recheckRun) hit() bool { return r.err == nil && r.result.Reused }

// requireMiss fails unless the run missed once with this outcome and refusal.
// A miss reaches the interpreter, which this harness refuses, so the turn
// makes exactly one model call and serves nothing stored.
func (r recheckRun) requireMiss(t *testing.T, outcome AnswerReuseOutcome, refusal AnswerReuseRefusal) {
	t.Helper()
	if r.hit() {
		t.Fatalf("a stored answer was served, want the miss %s", outcome)
	}
	if r.modelCalls != 1 {
		t.Fatalf("model calls = %d, want 1: a miss must reach the fresh path", r.modelCalls)
	}
	if len(r.telemetry.answerReuseOutcomes) != 1 || r.telemetry.answerReuseOutcomes[0] != outcome {
		t.Fatalf("outcomes = %v, want exactly [%s]", r.telemetry.answerReuseOutcomes, outcome)
	}
	if len(r.telemetry.answerReuseRefusals) != 1 || r.telemetry.answerReuseRefusals[0] != refusal {
		t.Fatalf("refusals = %+v, want exactly [%+v]", r.telemetry.answerReuseRefusals, refusal)
	}
}

// requireHit fails unless the run served the stored answer with no model call.
func (r recheckRun) requireHit(t *testing.T) {
	t.Helper()
	if !r.hit() {
		t.Fatalf("reused %t err %v outcomes %v refusals %+v, want a hit", r.result.Reused, r.err, r.telemetry.answerReuseOutcomes, r.telemetry.answerReuseRefusals)
	}
	if r.modelCalls != 0 {
		t.Fatalf("model calls = %d, want 0 on a hit", r.modelCalls)
	}
	if len(r.telemetry.answerReuseOutcomes) != 1 || r.telemetry.answerReuseOutcomes[0] != AnswerReuseHit {
		t.Fatalf("outcomes = %v, want exactly [%s]", r.telemetry.answerReuseOutcomes, AnswerReuseHit)
	}
}

// runRecheck runs the real Engine.Investigate -> tryReuse path over candidate.
func runRecheck(t *testing.T, graph GraphReader, candidate InvestigationResult) recheckRun {
	t.Helper()
	run := recheckRun{telemetry: &recordingTelemetry{}}
	engine := mustReuseTestEngine(t, EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			run.modelCalls++
			return InterpretedQuestion{}, errors.New("fresh investigation reached")
		}),
		Graph:     graph,
		Results:   &resultStoreStub{},
		Telemetry: run.telemetry,
		ReuseGate: optionalOwnerReuseGate{candidate: candidate},
	})
	run.result, run.err = engine.Investigate(context.Background(), ownerPrincipal("user_a", "acme/tools"), validInvestigationRequest())
	return run
}

// evidenceRef is the ref a test subject's discovery makes visible.
func evidenceRef(index int) string { return fmt.Sprintf("acr:v1:evidence:%010d", index) }

// TestARecheckResolvesEverySubjectInBatches (CHAOS-12511) runs the real path
// over a double that commits at most 50 subjects a call. It asserts the reads,
// not only the hit: one exact batch shape per size, every subject asked for
// once, in the stored answer's order, and every call under the recheck's own
// options, with no frame, on the one binding. The second half repeats each
// size with a citation, so discovery runs too, and the same shape holds there.
func TestARecheckResolvesEverySubjectInBatches(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		subjects int
		batches  []int
	}{
		{subjects: 1, batches: []int{1}},
		{subjects: 49, batches: []int{49}},
		{subjects: 50, batches: []int{50}},
		{subjects: 51, batches: []int{50, 1}},
		{subjects: 54, batches: []int{50, 4}},
		{subjects: 99, batches: []int{50, 49}},
		{subjects: 100, batches: []int{50, 50}},
		{subjects: 101, batches: []int{50, 50, 1}},
		{subjects: 150, batches: []int{50, 50, 50}},
	} {
		for _, cited := range []bool{false, true} {
			t.Run(fmt.Sprintf("subjects=%d/cited=%t", tc.subjects, cited), func(t *testing.T) {
				t.Parallel()
				subjects := reuseSubjects(tc.subjects)
				graph := &recheckGraph{pathRefs: map[string]string{}}
				candidate := recheckCandidate(subjects)
				wantDiscover := []int{}
				if cited {
					// The one citation is visible through the last subject only,
					// so it needs the last batch's discovery.
					candidate.EvidenceRefIDs = []string{evidenceRef(0)}
					graph.pathRefs[subjects[len(subjects)-1].CanonicalID] = evidenceRef(0)
					wantDiscover = tc.batches
				}
				run := runRecheck(t, graph, candidate)
				run.requireHit(t)
				t.Logf("subjects=%d cited=%t ResolveSubjects=%d DiscoverContext=%d", tc.subjects, cited, len(graph.resolves), len(graph.discovers))
				if got := graph.hintedSizes(); !slices.Equal(got, tc.batches) {
					t.Fatalf("resolve batches = %v, want %v", got, tc.batches)
				}
				if got := graph.discoveredSizes(); !slices.Equal(got, wantDiscover) {
					t.Fatalf("discovery batches = %v, want %v", got, wantDiscover)
				}
				asked := make([]string, 0, tc.subjects)
				for _, call := range graph.resolves {
					if call.options != reuseRecheckOptions {
						t.Fatalf("resolve options = %+v, want reuseRecheckOptions", call.options)
					}
					if call.confirmedKind != nil || call.confirmedAnchor != nil || call.frame != nil || call.scopeAnchorKind != "" {
						t.Fatalf("a recheck carried a frame or a confirmation: %+v", call)
					}
					if call.binding != (ResolvedGraphBinding{GraphKey: "stub-key", Epoch: 0}) {
						t.Fatalf("resolve binding = %+v, want the one binding of the turn", call.binding)
					}
					for _, hint := range call.hints {
						if hint.Source != string(hintsource.AnswerReuseAuthorizationRecheck) {
							t.Fatalf("hint source = %q", hint.Source)
						}
						asked = append(asked, hint.ID)
					}
				}
				want := make([]string, 0, tc.subjects)
				for _, subject := range subjects {
					want = append(want, subject.CanonicalID)
				}
				if !slices.Equal(asked, want) {
					t.Fatalf("subjects asked, in order = %v, want %v: none missing, none twice", asked, want)
				}
				if cited {
					walked := make([]string, 0, tc.subjects)
					for index, call := range graph.discovers {
						if call.options != reuseRecheckOptions || call.frame != nil || call.binding != (ResolvedGraphBinding{GraphKey: "stub-key", Epoch: 0}) {
							t.Fatalf("a discovery left the recheck's frame, options or binding: %+v", call)
						}
						if !slices.Equal(call.hints, graph.resolves[index].hints) {
							t.Fatalf("discovery %d ran on other hints than its resolution", index)
						}
						for _, subject := range call.committed {
							walked = append(walked, subject.CanonicalID)
						}
					}
					if !slices.Equal(walked, want) {
						t.Fatalf("subjects walked, in order = %v, want %v", walked, want)
					}
				}
			})
		}
	}
}

// TestAnUncommittedSubjectInAnyBatchRefusesTheAnswer (CHAOS-12511): one subject
// the current grant does not commit refuses the whole answer, wherever its
// batch falls, and the reads stop at that batch.
func TestAnUncommittedSubjectInAnyBatchRefusesTheAnswer(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		subjects  int
		denied    int
		wantCalls int
	}{
		{name: "first subject of the first batch", subjects: 120, denied: 0, wantCalls: 1},
		{name: "last subject of the first batch", subjects: 120, denied: 49, wantCalls: 1},
		{name: "first subject of the middle batch", subjects: 120, denied: 50, wantCalls: 2},
		{name: "last subject of the last batch", subjects: 120, denied: 119, wantCalls: 3},
		{name: "the one subject of a last batch of one", subjects: 51, denied: 50, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			subjects := reuseSubjects(tc.subjects)
			graph := &recheckGraph{denied: map[string]struct{}{subjects[tc.denied].CanonicalID: {}}, pathRefs: map[string]string{subjects[0].CanonicalID: evidenceRef(0)}}
			candidate := recheckCandidate(subjects)
			candidate.EvidenceRefIDs = []string{evidenceRef(0)}
			run := runRecheck(t, graph, candidate)
			run.requireMiss(t, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectNotCommitted})
			if len(graph.resolves) != tc.wantCalls {
				t.Fatalf("ResolveSubjects calls = %d, want %d: a refusal stops the reads", len(graph.resolves), tc.wantCalls)
			}
			if len(graph.discovers) != 0 {
				t.Fatalf("DiscoverContext calls = %d, want 0 after a refused subject", len(graph.discovers))
			}
		})
	}
}

// TestOneFailedBatchRefusesTheAnswer (CHAOS-12511): a batch that could not
// answer is not a batch that said yes, whichever batch it was.
func TestOneFailedBatchRefusesTheAnswer(t *testing.T) {
	t.Parallel()
	for _, failed := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("batch %d of 3", failed), func(t *testing.T) {
			t.Parallel()
			graph := &recheckGraph{resolveErrOn: failed}
			run := runRecheck(t, graph, recheckCandidate(reuseSubjects(120)))
			run.requireMiss(t, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectRecheckFailed})
			if len(graph.resolves) != failed {
				t.Fatalf("ResolveSubjects calls = %d, want %d: a failed batch stops the reads", len(graph.resolves), failed)
			}
		})
	}
}

// TestAnUnaskedSubjectAuthorizesNothing (CHAOS-12511): a batch can commit a
// subject it was not asked about. That never answers for another batch: a
// subject is authorized only by a batch that asked for it. Here the first
// batch commits a subject of the second batch, and the second batch refuses it.
func TestAnUnaskedSubjectAuthorizesNothing(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	last := subjects[53]
	graph := &recheckGraph{denied: map[string]struct{}{last.CanonicalID: {}}, unasked: map[int][]SubjectRef{1: {last}}}
	run := runRecheck(t, graph, recheckCandidate(subjects))
	run.requireMiss(t, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectNotCommitted})
	if got := graph.hintedSizes(); !slices.Equal(got, []int{50, 4}) {
		t.Fatalf("resolve batches = %v, want [50 4]", got)
	}
}

// TestAnUnaskedSubjectWidensNoDiscovery (CHAOS-12511): an unasked commit does
// not reach discovery as an origin, and its candidate row proves no evidence
// visible. The stored citation here is visible only through the unasked
// subject, so the answer is refused.
func TestAnUnaskedSubjectWidensNoDiscovery(t *testing.T) {
	t.Parallel()
	subjects := reuseSubjects(54)
	unasked := SubjectRef{Kind: SubjectProject, CanonicalID: "project_unasked", Label: "Unasked"}
	graph := &recheckGraph{
		unasked:       map[int][]SubjectRef{1: {unasked}, 2: {unasked}},
		candidateRefs: map[string][]string{unasked.CanonicalID: {evidenceRef(1)}},
		pathRefs:      map[string]string{unasked.CanonicalID: evidenceRef(1)},
	}
	candidate := recheckCandidate(subjects)
	candidate.EvidenceRefIDs = []string{evidenceRef(1)}
	run := runRecheck(t, graph, candidate)
	run.requireMiss(t, AnswerReuseMissEvidenceContainment, AnswerReuseRefusal{})
	for _, call := range graph.discovers {
		for _, subject := range call.committed {
			if subject == unasked {
				t.Fatal("a subject no batch asked about reached discovery")
			}
		}
	}
}

// TestEvidenceIsProvenAcrossEveryBatch (CHAOS-12511): one discovery shares one
// path budget across its origins, so past that budget an origin has no path and
// its evidence reads as not visible. One discovery per batch keeps each
// origin's share. A ref counts as visible when any batch proves it, through a
// path or through a candidate row, and a ref no batch proves is still refused.
func TestEvidenceIsProvenAcrossEveryBatch(t *testing.T) {
	t.Parallel()
	const subjectCount = 300
	subjects := reuseSubjects(subjectCount)
	cited := func(refs map[string]string) InvestigationResult {
		candidate := recheckCandidate(subjects)
		candidate.EvidenceRefIDs = []string{}
		for _, subject := range subjects {
			if ref, ok := refs[subject.CanonicalID]; ok {
				candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, ref)
			}
		}
		return candidate
	}
	everyPath := func() map[string]string {
		refs := make(map[string]string, subjectCount)
		for index, subject := range subjects {
			refs[subject.CanonicalID] = evidenceRef(index)
		}
		return refs
	}

	t.Run("every origin keeps its path", func(t *testing.T) {
		t.Parallel()
		graph := &recheckGraph{pathRefs: everyPath()}
		run := runRecheck(t, graph, cited(graph.pathRefs))
		run.requireHit(t)
		if got := graph.discoveredSizes(); !slices.Equal(got, []int{50, 50, 50, 50, 50, 50}) {
			t.Fatalf("discovery batches = %v, want six of 50", got)
		}
	})
	for _, tc := range []struct {
		name  string
		index int
	}{{"a candidate row of the first batch", 0}, {"a candidate row of the last batch", subjectCount - 1}} {
		t.Run(tc.name+" proves its ref", func(t *testing.T) {
			t.Parallel()
			graph := &recheckGraph{candidateRefs: map[string][]string{subjects[tc.index].CanonicalID: {evidenceRef(tc.index)}}}
			candidate := recheckCandidate(subjects)
			candidate.EvidenceRefIDs = []string{evidenceRef(tc.index)}
			runRecheck(t, graph, candidate).requireHit(t)
		})
	}
	t.Run("a ref no batch proves is refused", func(t *testing.T) {
		t.Parallel()
		refs := everyPath()
		graph := &recheckGraph{pathRefs: refs}
		candidate := cited(refs)
		candidate.EvidenceRefIDs = append(candidate.EvidenceRefIDs, evidenceRef(subjectCount))
		runRecheck(t, graph, candidate).requireMiss(t, AnswerReuseMissEvidenceContainment, AnswerReuseRefusal{})
	})
	for _, failed := range []int{1, 6} {
		t.Run(fmt.Sprintf("discovery %d of 6 fails", failed), func(t *testing.T) {
			t.Parallel()
			graph := &recheckGraph{pathRefs: everyPath(), discoverErrOn: failed}
			runRecheck(t, graph, cited(graph.pathRefs)).requireMiss(t, AnswerReuseMissRecheckUnavailable, AnswerReuseRefusal{})
			if len(graph.discovers) != failed {
				t.Fatalf("DiscoverContext calls = %d, want %d: a failed read stops the reads", len(graph.discovers), failed)
			}
		})
	}
	t.Run("an unverified narrowing in the last batch refuses a clean answer", func(t *testing.T) {
		t.Parallel()
		graph := &recheckGraph{pathRefs: everyPath(), narrowingUnverifiedFor: subjects[subjectCount-1].CanonicalID}
		runRecheck(t, graph, cited(graph.pathRefs)).requireMiss(t, AnswerReuseMissProjectNarrowingUnverified, AnswerReuseRefusal{})
	})
	t.Run("a removed team in the last batch is stated on the reused answer", func(t *testing.T) {
		t.Parallel()
		graph := &recheckGraph{pathRefs: everyPath(), removed: map[string]struct{}{subjects[subjectCount-1].CanonicalID: {}}}
		run := runRecheck(t, graph, cited(graph.pathRefs))
		run.requireHit(t)
		if !slices.Contains(run.result.Limitations, contractsv1.ContextFabricRemovedTeamLimitation) {
			t.Fatalf("limitations = %v, want the removed-team sentence", run.result.Limitations)
		}
	})
}

// TestTheRemovedTeamReadSeesEverySubject (CHAOS-12511): a stored answer that
// cites no evidence runs no discovery, so its one removed-team read must name
// every subject, not the last batch.
func TestTheRemovedTeamReadSeesEverySubject(t *testing.T) {
	t.Parallel()
	subjects := make([]SubjectRef, 0, 54)
	for index := range 54 {
		subjects = append(subjects, SubjectRef{Kind: SubjectTeam, CanonicalID: fmt.Sprintf("team_%03d", index), Label: fmt.Sprintf("Team %03d", index)})
	}
	graph := &recheckGraph{removed: map[string]struct{}{subjects[0].CanonicalID: {}}}
	run := runRecheck(t, graph, recheckCandidate(subjects))
	run.requireHit(t)
	if len(graph.removedFor) != 1 || !slices.Equal(graph.removedFor[0], subjects) {
		t.Fatalf("removed-team reads = %d, want one over every subject", len(graph.removedFor))
	}
	if !slices.Contains(run.result.Limitations, contractsv1.ContextFabricRemovedTeamLimitation) {
		t.Fatalf("limitations = %v, want the removed-team sentence", run.result.Limitations)
	}
}

// TestAnAnswerNamingNoSubjectKeepsItsOneRead (CHAOS-12511): no subject means
// no batch to send, but such an answer always made one resolution, and its
// discovery reads what that resolution committed. Batching must not turn it
// into no read at all, and there is no asked set to narrow that resolution to.
func TestAnAnswerNamingNoSubjectKeepsItsOneRead(t *testing.T) {
	t.Parallel()
	found := SubjectRef{Kind: SubjectProject, CanonicalID: "project_found", Label: "Found"}
	graph := &recheckGraph{unasked: map[int][]SubjectRef{1: {found}}, pathRefs: map[string]string{found.CanonicalID: evidenceRef(0)}}
	_, candidate := reusableCandidate()
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	candidate.EvidenceRefIDs = []string{evidenceRef(0)}
	run := runRecheck(t, graph, candidate)
	run.requireHit(t)
	if len(graph.resolves) != 1 || len(graph.resolves[0].hints) != 0 {
		t.Fatalf("resolves = %+v, want one read with no hint", graph.resolves)
	}
	if len(graph.discovers) != 1 || len(graph.discovers[0].committed) != 1 || graph.discovers[0].committed[0] != found {
		t.Fatalf("discovers = %+v, want one read of that resolution", graph.discovers)
	}
}

// maximalRecheckCandidate is a stored answer the contract accepts with every
// list that can add a subject at its bound and no subject named twice. Each list
// holds its own subjects, so the union is exactly the sum the cap states. extra
// names that many more subjects, on a driver of their own.
func maximalRecheckCandidate(extra int) InvestigationResult {
	next := 0
	fresh := func(count int) []SubjectRef {
		subjects := make([]SubjectRef, 0, count)
		for range count {
			next++
			subjects = append(subjects, SubjectRef{Kind: SubjectProject, CanonicalID: fmt.Sprintf("project_max_%05d", next), Label: fmt.Sprintf("Max %05d", next)})
		}
		return subjects
	}
	_, candidate := reusableCandidate()
	committed := fresh(contractsv1.ContextFabricResolutionCommittedMaxCount)
	candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: committed, CommitDecisionDigests: identityProvenDigests(committed...)}
	for index, subject := range fresh(contractsv1.ContextFabricResolutionCandidatesMaxCount) {
		candidate.SubjectResolution.Candidates = append(candidate.SubjectResolution.Candidates, SubjectCandidate{
			ReceiptID: fmt.Sprintf("receipt_%05d", index), Subject: subject, State: ResolutionAmbiguous, Confidence: 0.5, MatchReasons: []string{"Matched a term."},
		})
	}
	cohort := Cohort{Kind: SubjectProject, Members: []CohortMember{}, Rationale: "Every project.", Complete: true}
	for index, subject := range fresh(contractsv1.ContextFabricCohortMembersMaxCount) {
		cohort.Members = append(cohort.Members, CohortMember{Subject: subject, Rank: index + 1, InclusionReasons: []string{"Named by the question."}})
	}
	for _, subject := range fresh(contractsv1.ContextFabricCohortExclusionsMaxCount) {
		cohort.Exclusions = append(cohort.Exclusions, CohortExclusion{Subject: subject, Reason: "Archived."})
	}
	candidate.Cohort = &cohort
	for index, subject := range fresh(contractsv1.ContextFabricClaimedFactsMaxCount) {
		candidate.ClaimedFacts = append(candidate.ClaimedFacts, ClaimedFact{ClaimID: fmt.Sprintf("claim_%05d", index), Kind: FactReadiness, Subject: subject, Field: "release_ready", Value: boolScalar(false)})
	}
	for index := range contractsv1.ContextFabricRelationshipPathsMaxCount {
		nodes := fresh(AdmittedPathNodes)
		candidate.Paths = append(candidate.Paths, RelationshipPath{
			PathID: fmt.Sprintf("path_%05d", index), Nodes: nodes, WhyRelevant: "Linked.", EvidenceRefIDs: []string{evidenceRef(0)},
			Edges: []RelationshipEdge{{Type: contractsv1.ContextFabricRelationshipOwnedByTeam, From: nodes[0], To: nodes[1], Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicObserved, ObservedAt: &candidate.GeneratedAt, EvidenceRefIDs: []string{evidenceRef(0)}}},
		})
	}
	// Groups reach the union only when a driver or a finding cites one.
	driver := func(id string, subjects []SubjectRef) DriverJudgment {
		return DriverJudgment{
			DriverID: id, Standing: DriverPrincipal, Category: "narrative", Title: "A driver", Summary: "A driver summary.",
			AffectedSubjects: subjects, EvidenceRefIDs: []string{}, PathIDs: []string{"path_00000"}, Derivation: DerivationGraphAssociated, EpistemicStatus: EpistemicObserved, Confidence: 0.5, Current: true,
		}
	}
	candidate.Drivers = []DriverJudgment{driver("driver_groups_01", fresh(contractsv1.ContextFabricCohortGroupsMaxCount))}
	if extra > 0 {
		candidate.Drivers = append(candidate.Drivers, driver("driver_extras_01", fresh(extra)))
	}
	candidate.Completeness = ComputeAnswerCompleteness(candidate)
	return candidate
}

// TestTheRecheckCapIsTheLargestUnionAnAnswerCanCarry (CHAOS-12511) pins the cap
// to its derivation and the real walker, not to a number. The fixture is a
// document the contract accepts, each input list is at the bound the validator
// enforces (one more is rejected), and reuseSubjectsToRecheck returns exactly
// the cap for it.
func TestTheRecheckCapIsTheLargestUnionAnAnswerCanCarry(t *testing.T) {
	t.Parallel()
	// The sum stated in the const's comment. A bound that moves fails here, and
	// that comment moves with it.
	if maxReuseSubjectRecheckCount != 1800 {
		t.Fatalf("maxReuseSubjectRecheckCount = %d, the comment at the const states 1800", maxReuseSubjectRecheckCount)
	}
	candidate := maximalRecheckCandidate(0)
	if err := candidate.Validate(); err != nil {
		t.Fatalf("the maximal fixture is not a document the contract accepts: %v", err)
	}
	if got := len(reuseSubjectsToRecheck(candidate)); got != maxReuseSubjectRecheckCount {
		t.Fatalf("distinct subjects = %d, want the cap %d", got, maxReuseSubjectRecheckCount)
	}
	subject := SubjectRef{Kind: SubjectProject, CanonicalID: "project_past_bound", Label: "Past the bound"}
	for name, step := range map[string]func(*InvestigationResult){
		"committed": func(r *InvestigationResult) {
			r.SubjectResolution.Committed = append(r.SubjectResolution.Committed, subject)
			r.SubjectResolution.CommitDecisionDigests = identityProvenDigests(r.SubjectResolution.Committed...)
		},
		"candidates": func(r *InvestigationResult) {
			r.SubjectResolution.Candidates = append(r.SubjectResolution.Candidates, SubjectCandidate{ReceiptID: "receipt_past_bound", Subject: subject, State: ResolutionAmbiguous, Confidence: 0.5, MatchReasons: []string{"Matched a term."}})
		},
		"members": func(r *InvestigationResult) {
			r.Cohort.Members = append(r.Cohort.Members, CohortMember{Subject: subject, Rank: len(r.Cohort.Members) + 1, InclusionReasons: []string{"Named by the question."}})
		},
		"exclusions": func(r *InvestigationResult) {
			r.Cohort.Exclusions = append(r.Cohort.Exclusions, CohortExclusion{Subject: subject, Reason: "Archived."})
		},
		"claimed facts": func(r *InvestigationResult) {
			r.ClaimedFacts = append(r.ClaimedFacts, ClaimedFact{ClaimID: "claim_past_bound", Kind: FactReadiness, Subject: subject, Field: "release_ready", Value: boolScalar(false)})
		},
		"paths": func(r *InvestigationResult) {
			path := r.Paths[0]
			path.PathID = "path_past_bound"
			r.Paths = append(r.Paths, path)
		},
	} {
		past := maximalRecheckCandidate(0)
		step(&past)
		if err := past.Validate(); err == nil {
			t.Errorf("%s one past the bound validated, so the cap's input is not the enforced bound", name)
		}
	}
	options := reuseRecheckOptions
	if err := options.Validate(); err != nil {
		t.Fatalf("reuseRecheckOptions = %v, want the options the contract accepts", err)
	}
	options.MaxSubjectCandidates++
	if err := options.Validate(); err == nil {
		t.Fatal("one past ContextFabricMaxSubjectCandidatesLimit validated, so the batch size is not the enforced limit")
	}
}

// TestTheRecheckCapIsServedAndOnePastItIsRefused (CHAOS-12511) runs both sides of
// the cap on the real path. These are the worst-case reads. The contract requires
// a path to cite evidence, so an answer at the cap always runs the evidence leg:
// one ResolveSubjects call and one DiscoverContext call for each batch.
func TestTheRecheckCapIsServedAndOnePastItIsRefused(t *testing.T) {
	t.Parallel()
	t.Run("the cap", func(t *testing.T) {
		t.Parallel()
		wantBatches := make([]int, maxReuseSubjectRecheckCount/reuseRecheckOptions.MaxSubjectCandidates)
		for index := range wantBatches {
			wantBatches[index] = reuseRecheckOptions.MaxSubjectCandidates
		}
		candidate := maximalRecheckCandidate(0)
		subjects := reuseSubjectsToRecheck(candidate)
		graph := &recheckGraph{pathRefs: map[string]string{subjects[len(subjects)-1].CanonicalID: evidenceRef(0)}}
		run := runRecheck(t, graph, candidate)
		run.requireHit(t)
		t.Logf("subjects=%d ResolveSubjects=%d DiscoverContext=%d", len(subjects), len(graph.resolves), len(graph.discovers))
		if got := graph.hintedSizes(); !slices.Equal(got, wantBatches) {
			t.Fatalf("resolve batches = %v, want %v", got, wantBatches)
		}
		if got := graph.discoveredSizes(); !slices.Equal(got, wantBatches) {
			t.Fatalf("discovery batches = %v, want %v", got, wantBatches)
		}
		seen := make(map[string]struct{}, len(subjects))
		for _, call := range graph.resolves {
			for _, hint := range call.hints {
				if _, twice := seen[hint.ID]; twice {
					t.Fatalf("subject %s asked for twice", hint.ID)
				}
				seen[hint.ID] = struct{}{}
			}
		}
		if len(seen) != len(subjects) || len(seen) > maxReuseSubjectRecheckCount {
			t.Fatalf("subjects asked = %d, want every one of %d, at most the cap", len(seen), len(subjects))
		}
	})
	t.Run("one past the cap", func(t *testing.T) {
		t.Parallel()
		graph := &recheckGraph{}
		run := runRecheck(t, graph, maximalRecheckCandidate(1))
		run.requireMiss(t, AnswerReuseMissAuthorization, AnswerReuseRefusal{Reason: AnswerReuseRefusalSubjectCapExceeded, SubjectCount: maxReuseSubjectRecheckCount + 1})
		if len(graph.resolves) != 0 || len(graph.discovers) != 0 {
			t.Fatalf("reads = %d resolves, %d discovers, want none past the cap", len(graph.resolves), len(graph.discovers))
		}
	})
}
