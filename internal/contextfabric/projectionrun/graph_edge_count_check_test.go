package projectionrun_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

var (
	edgeTransition = contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmTransition}
	edgeColumn     = contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToProject, Arm: contextfabric.EdgeArmWorkItemColumn}
	edgeReview     = contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipBelongsToPullRequest}
)

func newEdgeHarness(t *testing.T, source, graph map[contextfabric.EdgeCountKey]int64) *countHarness {
	t.Helper()
	h := newCountHarness(t, map[contextfabric.SubjectKind]int64{}, map[contextfabric.SubjectKind]int64{}, nil, nil, time.Minute)
	h.source.edges, h.backend.edgeGraph = source, graph
	return h
}

// The prod roll that motivated the check: BELONGS_TO_PROJECT edges fell from
// 6756 to 1997 and the node count stayed clean.
func TestEdgeCountWarnsWhenBelongsToProjectEdgesFallBelowSource(t *testing.T) {
	t.Parallel()
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 6756, edgeColumn: 40},
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 1997, edgeColumn: 40})
	h.run(testStart)
	if len(h.observer.edgeEvents) != 0 || strings.Contains(h.log.String(), "graph_edges_below_source") {
		t.Fatalf("a gap seen once may be rows in flight and must not warn yet:\n%s", h.log.String())
	}
	h.run(testStart.Add(time.Minute))
	logged := h.log.String()
	for _, want := range []string{`"check":"graph_edges_below_source"`, `"relation_type":"BELONGS_TO_PROJECT"`, `"arm":"transition"`, `"source_count":6756`, `"graph_count":1997`, `"tolerance":135`, `"source":"source-a"`} {
		if !strings.Contains(logged, want) {
			t.Fatalf("missing %s in:\n%s", want, logged)
		}
	}
	if len(h.observer.edgeEvents) != 1 || h.observer.edgeEvents[0].Relation != contractsv1.ContextFabricRelationshipBelongsToProject || h.observer.edgeEvents[0].Arm != contextfabric.EdgeArmTransition || h.observer.edgeEvents[0].SourceCount != 6756 || h.observer.edgeEvents[0].GraphCount != 1997 {
		t.Fatalf("expected one edge counter event, got %+v", h.observer.edgeEvents)
	}
	last := h.observer.checks[len(h.observer.checks)-1]
	if last.Outcome != projectionrun.GraphCountCheckCompleted || last.EdgeGaps != 1 || last.Gaps != 0 || last.RelationsCompared != 2 || last.Errors != 0 {
		t.Fatalf("the node count is clean, the edge count found one gap: %+v", last)
	}
}

func TestEdgeCountToleranceAndArmsAreIndependent(t *testing.T) {
	t.Parallel()
	// Exactly at tolerance is silent, one past it warns, and the other arm stays silent.
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 1000, edgeColumn: 100, edgeReview: 10},
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 980, edgeColumn: 96, edgeReview: 10})
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.edgeEvents) != 1 || h.observer.edgeEvents[0].Arm != contextfabric.EdgeArmWorkItemColumn || h.observer.edgeEvents[0].Tolerance != 3 {
		t.Fatalf("only the column arm (100 vs 96, tolerance 3) is beyond tolerance: %+v", h.observer.edgeEvents)
	}
}

func TestEdgeCountWarnsWhenARelationHasNoEdgesInTheGraph(t *testing.T) {
	t.Parallel()
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeReview: 1},
		map[contextfabric.EdgeCountKey]int64{edgeReview: 0})
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.edgeEvents) != 1 || h.observer.edgeEvents[0].Tolerance != 0 || h.observer.edgeEvents[0].Arm != "" {
		t.Fatalf("one source edge with none in the graph is a gap at tolerance 0: %+v", h.observer.edgeEvents)
	}
	if !strings.Contains(h.log.String(), `"arm":""`) || !strings.Contains(h.log.String(), `"relation_type":"BELONGS_TO_PULL_REQUEST"`) {
		t.Fatalf("a relation without arms logs an empty arm:\n%s", h.log.String())
	}
}

func TestEdgeCountSilentWhenHealthyAndTransientGapDoesNotWarn(t *testing.T) {
	t.Parallel()
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 1000},
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 900})
	h.run(testStart)
	h.source.edges = map[contextfabric.EdgeCountKey]int64{edgeTransition: 900}
	h.run(testStart.Add(time.Minute))
	h.source.edges = map[contextfabric.EdgeCountKey]int64{edgeTransition: 1000}
	h.run(testStart.Add(2 * time.Minute))
	if len(h.observer.edgeEvents) != 0 || strings.Contains(h.log.String(), "graph_edges_below_source") {
		t.Fatalf("a gap that closed between checks must not warn:\n%s", h.log.String())
	}
	last := h.observer.checks[len(h.observer.checks)-1]
	if last.RelationsCompared != 1 || last.EdgeGaps != 0 || last.Outcome != projectionrun.GraphCountCheckCompleted {
		t.Fatalf("the edge check ran and found nothing: %+v", last)
	}
}

func TestEdgeCountGapKeyIsNotTheNodeGapKey(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequestReview: 1000},
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequestReview: 900},
		nil, nil, time.Minute)
	h.source.edges = map[contextfabric.EdgeCountKey]int64{edgeReview: 1000}
	h.backend.edgeGraph = map[contextfabric.EdgeCountKey]int64{edgeReview: 900}
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.events) != 1 || len(h.observer.edgeEvents) != 1 {
		t.Fatalf("the node gap and the edge gap are two findings: nodes=%+v edges=%+v", h.observer.events, h.observer.edgeEvents)
	}
	last := h.observer.checks[len(h.observer.checks)-1]
	if last.Gaps != 1 || last.EdgeGaps != 1 {
		t.Fatalf("each gap counts in its own field: %+v", last)
	}
}

// A measurement that did not happen is never a clean completion.
func TestEdgeCountReadFailuresAreLoud(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		sourceErr, graphErr error
		stage               string
	}{
		"source": {sourceErr: errors.New("clickhouse down"), stage: `"stage":"edge_source_count"`},
		"graph":  {graphErr: errors.New("falkor down"), stage: `"stage":"edge_graph_count"`},
	} {
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newEdgeHarness(t,
				map[contextfabric.EdgeCountKey]int64{edgeTransition: 1000},
				map[contextfabric.EdgeCountKey]int64{edgeTransition: 0})
			h.source.edgeErr, h.backend.edgeErr = tc.sourceErr, tc.graphErr
			if tc.sourceErr != nil {
				h.source.edges = nil
			}
			h.run(testStart)
			logged := h.log.String()
			if !strings.Contains(logged, "projection count check failed") || !strings.Contains(logged, tc.stage) {
				t.Fatalf("an edge read error must be its own Warn:\n%s", logged)
			}
			if strings.Contains(logged, "graph_edges_below_source") {
				t.Fatalf("a read error must never read as a gap:\n%s", logged)
			}
			if len(h.observer.checks) != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckFailed || h.observer.checks[0].Errors != 1 || h.observer.checks[0].RelationsCompared != 0 {
				t.Fatalf("the check failed and compared nothing: %+v", h.observer.checks)
			}
		})
	}
}

func TestEdgeCountUnmeasuredIsLoud(t *testing.T) {
	t.Parallel()
	nodes := map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 10}
	t.Run("source without edge counts", func(t *testing.T) {
		t.Parallel()
		h := buildCountHarness(t, nodes, nodes, nil, nil, time.Minute,
			func(s *countingSource) contextfabric.ProjectionSource {
				return nodeOnlySource{fakeSource: s.fakeSource, inner: s}
			}, nil)
		h.run(testStart)
		logged := h.log.String()
		if !strings.Contains(logged, "graph_edge_count_unmeasured") || !strings.Contains(logged, `"reason":"source_no_edge_counts"`) || !strings.Contains(logged, `"source":"source-a"`) {
			t.Fatalf("a source with node counts and no edge counts is an unmeasured Warn:\n%s", logged)
		}
		if len(h.observer.checks) != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckFailed || h.observer.checks[0].Errors != 1 || h.observer.checks[0].KindsCompared != 1 {
			t.Fatalf("the node check still ran; the check as a whole is failed: %+v", h.observer.checks)
		}
	})
	t.Run("backend without edge counts", func(t *testing.T) {
		t.Parallel()
		h := buildCountHarness(t, nodes, nodes, nil, nil, time.Minute, nil,
			func(b *countingBackend) contextfabric.ProjectionBackend {
				return nodeOnlyBackend{fakeBackend: b.fakeBackend, inner: b}
			})
		h.run(testStart)
		logged := h.log.String()
		if strings.Count(logged, "graph_edge_count_unmeasured") != 1 || !strings.Contains(logged, `"reason":"backend_no_edge_counts"`) {
			t.Fatalf("a backend with node counts and no edge counts is ONE unmeasured Warn per check:\n%s", logged)
		}
		if len(h.observer.checks) != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckFailed || h.observer.checks[0].Errors != 1 {
			t.Fatalf("%+v", h.observer.checks)
		}
	})
}

func TestEdgeCountTimeoutAfterNodePhaseIsCancelledNotCompleted(t *testing.T) {
	t.Parallel()
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 10},
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 10})
	h.source.onEdgeCount = h.cancel
	h.run(testStart)
	if len(h.observer.checks) != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckCancelled {
		t.Fatalf("a check cancelled during the edge phase reports cancelled: %+v", h.observer.checks)
	}
}

func TestEdgeCountRunsEvenWhenANodeReadFailed(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t,
		map[contextfabric.SubjectKind]int64{contractsv1.ContextFabricSubjectPullRequest: 10}, map[contextfabric.SubjectKind]int64{},
		errors.New("clickhouse nodes down"), nil, time.Minute)
	h.source.edges = map[contextfabric.EdgeCountKey]int64{edgeTransition: 1000}
	h.backend.edgeGraph = map[contextfabric.EdgeCountKey]int64{edgeTransition: 10}
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	if len(h.observer.edgeEvents) != 1 {
		t.Fatalf("a failed node read must not hide the edge gap: %+v", h.observer.edgeEvents)
	}
}

func TestEdgeCountDeferredRelationsAreCountedOncePerCheck(t *testing.T) {
	t.Parallel()
	h := newEdgeHarness(t,
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 10},
		map[contextfabric.EdgeCountKey]int64{edgeTransition: 10})
	h.source.deferred = []contextfabric.RelationshipType{contractsv1.ContextFabricRelationshipOwnedByTeam, contractsv1.ContextFabricRelationshipBlocks, contractsv1.ContextFabricRelationshipOwnedByTeam}
	h.run(testStart)
	if len(h.observer.checks) != 1 || h.observer.checks[0].RelationsDeferred != 2 || h.observer.checks[0].RelationsCompared != 1 || h.observer.checks[0].Outcome != projectionrun.GraphCountCheckCompleted {
		t.Fatalf("deferred relations are visible and distinct: %+v", h.observer.checks)
	}
}

func TestEdgeCountRunsOncePerIntervalWithTheNodeCheck(t *testing.T) {
	t.Parallel()
	h := newCountHarness(t, map[contextfabric.SubjectKind]int64{}, map[contextfabric.SubjectKind]int64{}, nil, nil, 10*time.Minute)
	h.source.edges = map[contextfabric.EdgeCountKey]int64{edgeTransition: 10}
	h.backend.edgeGraph = map[contextfabric.EdgeCountKey]int64{edgeTransition: 10}
	h.run(testStart)
	h.run(testStart.Add(time.Minute))
	h.run(testStart.Add(9 * time.Minute))
	if got := h.source.edgeCalls.Load(); got != 1 {
		t.Fatalf("inside one interval the edge count reads once, read %d", got)
	}
	h.run(testStart.Add(10 * time.Minute))
	if got := h.source.edgeCalls.Load(); got != 2 {
		t.Fatalf("after the interval it reads again, total %d", got)
	}
}
