package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos4086Tracer records every event, so a test can assert on a stage the
// resolver emits at most once per resolution.
type chaos4086Tracer struct{ events []ResolutionTraceEvent }

func (c *chaos4086Tracer) Trace(event ResolutionTraceEvent) { c.events = append(c.events, event) }

func (c *chaos4086Tracer) floorEvents() []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range c.events {
		if e.Stage == "kind_coverage_floor" {
			out = append(out, e)
		}
	}
	return out
}

// TestChaos4086_CoverageFloorEmitsItsOwnTraceEvent is the EMISSION pin.
//
// The harness reads this event to fill a trial-report row, but a harness-side
// test constructs its trace capture directly and would therefore still pass
// if the resolver stopped emitting entirely. This asserts the resolver's own
// behavior: the floor runs, and it says so.
func TestChaos4086_CoverageFloorEmitsItsOwnTraceEvent(t *testing.T) {
	t.Parallel()
	tracer := &chaos4086Tracer{}
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults: map[string][]CandidateNode{
			"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*")},
		},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.6, "*")}},
		},
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	events := tracer.floorEvents()
	if len(events) == 0 {
		t.Fatal("the coverage floor emitted no kind_coverage_floor event -- the trial report's floor fields can only ever read zero")
	}
	event := events[len(events)-1]
	if !event.KindCoverageFloorFired {
		t.Error("KindCoverageFloorFired = false, but the floor added a pull_request the pool did not have")
	}
	if event.KindCoverageMissingKinds == 0 {
		t.Error("KindCoverageMissingKinds = 0, but the pool started with only work_item")
	}
}

// TestChaos4086_CoverageFloorReportsNotFiredWhenItAddsNothing pins the
// distinction the boolean exists to draw. A floor that RAN over missing kinds
// and found nothing is a different finding from one that never ran, and
// MissingKinds>0 with Fired==false is how a reader tells them apart.
func TestChaos4086_CoverageFloorReportsNotFiredWhenItAddsNothing(t *testing.T) {
	t.Parallel()
	tracer := &chaos4086Tracer{}
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*")}},
		// No searchKindResults: every missing kind is queried and comes
		// back empty.
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	events := tracer.floorEvents()
	if len(events) == 0 {
		t.Fatal("a floor that found nothing must still emit -- silence is indistinguishable from never running")
	}
	event := events[len(events)-1]
	if event.KindCoverageFloorFired {
		t.Error("KindCoverageFloorFired = true, but the floor added nothing")
	}
	if event.KindCoverageMissingKinds == 0 {
		t.Error("KindCoverageMissingKinds = 0, but kinds were missing and queried")
	}
}

// TestChaos4086_FloorStateNeverRidesOnTheDecisionEvent pins the separation
// the resolver is careful to maintain.
//
// The floor's truncation and degradation are deliberately excluded from the
// commit gate's inputs (see the call site in resolve.go). Carrying its state
// on the DECISION event would put it exactly where a reader infers gate
// inputs from, re-creating by presentation a coupling the code does not have.
func TestChaos4086_FloorStateNeverRidesOnTheDecisionEvent(t *testing.T) {
	t.Parallel()
	tracer := &chaos4086Tracer{}
	backend := &fakeGraphBackend{
		enableSearchKind: true,
		searchResults:    map[string][]CandidateNode{"outage": {candidateNode(contextfabric.SubjectWorkItem, "wi_1", "Outage work item", 0.9, "*")}},
		searchKindResults: map[string]map[contextfabric.SubjectKind][]CandidateNode{
			"outage": {contextfabric.SubjectPullRequest: {candidateNode(contextfabric.SubjectPullRequest, "pr_1", "Outage PR", 0.6, "*")}},
		},
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("outage"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	for _, e := range tracer.events {
		if e.Stage == "kind_coverage_floor" {
			continue
		}
		if e.KindCoverageFloorFired || e.KindCoverageMissingKinds != 0 || e.KindCoverageFloorTruncated {
			t.Fatalf("stage %q carries coverage-floor state (fired=%v missing=%d truncated=%v) -- it belongs to its own stage only", e.Stage, e.KindCoverageFloorFired, e.KindCoverageMissingKinds, e.KindCoverageFloorTruncated)
		}
	}
}
