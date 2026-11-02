package contextfabric

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// ReuseLineScenario names one reuse attempt on the real Engine.Investigate ->
// tryReuse path, for the external certify test: eventspec imports this package,
// so a test that certifies a line cannot live inside it.
type ReuseLineScenario string

const (
	ReuseLineHit               ReuseLineScenario = "hit at 54 subjects"
	ReuseLineNoSubject         ReuseLineScenario = "hit naming no subject"
	ReuseLineDegraded          ReuseLineScenario = "degraded hit"
	ReuseLineNotCommitted      ReuseLineScenario = "subject not committed"
	ReuseLineRecheckFailed     ReuseLineScenario = "subject recheck failed"
	ReuseLineContainment       ReuseLineScenario = "evidence containment miss"
	ReuseLineUnavailable       ReuseLineScenario = "recheck unavailable"
	ReuseLineNarrowing         ReuseLineScenario = "project narrowing unverified"
	ReuseLineDegradeInvalid    ReuseLineScenario = "degrade invalid"
	ReuseLineRecheckDeadline   ReuseLineScenario = "recheck deadline"
	ReuseLineGateWithheld      ReuseLineScenario = "gate withheld"
	ReuseLineCapExceeded       ReuseLineScenario = "subject cap exceeded"
	ReuseLineGraphNotProjected ReuseLineScenario = "graph not projected"
)

// ReuseLineRun is what one scenario did to the graph.
type ReuseLineRun struct {
	Reads    int
	Subjects int
}

// RunReuseLineForTest runs one scenario with telemetry as the engine's sink. The
// engine's clock stands still except that each graph read of the recheck advances
// it by step, so the recheck's true duration is exactly Reads * step. A measured
// value can then be told from a constant, and from a clock started late.
func RunReuseLineForTest(ctx context.Context, t *testing.T, telemetry EngineTelemetry, scenario ReuseLineScenario, step time.Duration) ReuseLineRun {
	t.Helper()
	subjects := reuseSubjects(54)
	last := subjects[53]
	budget := time.Hour
	graph := &recheckGraph{pathRefs: map[string]string{}}
	candidate := recheckCandidate(subjects)
	cite := func() {
		candidate.EvidenceRefIDs = []string{evidenceRef(0)}
		graph.pathRefs[last.CanonicalID] = evidenceRef(0)
	}
	switch scenario {
	case ReuseLineHit:
		cite()
	case ReuseLineNoSubject:
		_, candidate = reusableCandidate()
		candidate.SubjectResolution = SubjectResolution{Candidates: []SubjectCandidate{}, Committed: []SubjectRef{}}
	case ReuseLineDegraded, ReuseLineDegradeInvalid:
		// The unproven ref rides a candidate row, so it is no citation and the
		// answer degrades. A complete answer with no judgment fails stored
		// validation, so the degraded copy cannot be served: a store never hands
		// out such a row, and this is the route to that refusal.
		candidate.SubjectResolution.Candidates = []SubjectCandidate{{ReceiptID: "receipt_degrade_01", Subject: subjects[0], State: ResolutionAmbiguous, Confidence: 0.5, MatchReasons: []string{"Matched a term."}, EvidenceRefIDs: []string{evidenceRef(1)}}}
		if scenario == ReuseLineDegradeInvalid {
			candidate.DirectJudgment = ""
		}
	case ReuseLineNotCommitted:
		graph.denied = map[string]struct{}{last.CanonicalID: {}}
	case ReuseLineRecheckFailed:
		graph.resolveErrOn = 2
	case ReuseLineContainment:
		candidate.EvidenceRefIDs = []string{evidenceRef(0)}
	case ReuseLineUnavailable:
		cite()
		graph.discoverErrOn = 2
	case ReuseLineRecheckDeadline:
		cite()
		graph.hangDiscoverOn = 2
		budget = 20 * time.Millisecond
	case ReuseLineNarrowing:
		cite()
		graph.narrowingUnverifiedFor = last.CanonicalID
	case ReuseLineGateWithheld:
		graph.gateDenied = true
	case ReuseLineCapExceeded:
		candidate = maximalRecheckCandidate(1)
	case ReuseLineGraphNotProjected:
		candidate.SubjectResolution.GraphNotProjected = true
	default:
		t.Fatalf("unknown scenario %q", scenario)
	}
	var elapsed atomic.Int64
	base := time.Unix(200, 0).UTC()
	graph.onRead = func() { elapsed.Add(int64(step)) }
	engine, err := NewEngine(EngineDependencies{
		Interpreter: interpreterFunc(func(context.Context, storage.Principal, InvestigationRequest) (InterpretedQuestion, error) {
			return InterpretedQuestion{}, errors.New("fresh investigation reached")
		}),
		Graph: graph, Facts: failingFactReader{t: t}, Results: &resultStoreStub{}, Telemetry: telemetry,
		ReuseGate: optionalOwnerReuseGate{candidate: candidate},
		Synthesizer: synthesizerFunc(func(context.Context, storage.Principal, SynthesisInput) (InvestigationResult, error) {
			t.Fatal("synthesizer should not be reached")
			return InvestigationResult{}, nil
		}),
	}, EngineOptions{
		ServiceVersion: "acr-test", NewResultID: func() string { return freshTestResultID }, ReuseRecheckBudget: budget,
		ReuseVersionAuthorities: ReuseVersionAuthorities{OwnershipRoutingVersion: OwnershipRoutingVersion},
		Now:                     func() time.Time { return base.Add(time.Duration(elapsed.Load())) },
	})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	_, _ = engine.Investigate(ctx, ownerPrincipal("user_a", "acme/tools"), validInvestigationRequest())
	return ReuseLineRun{Reads: len(graph.resolves) + len(graph.discovers), Subjects: len(reuseSubjectsToRecheck(candidate))}
}
