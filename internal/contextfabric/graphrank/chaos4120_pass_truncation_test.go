package graphrank

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// chaos4120Tracer records every event -- mirrors chaos4086Tracer's own
// pattern (chaos4086_floor_trace_test.go), a separate type per file's
// established convention in this package rather than a shared test helper.
type chaos4120Tracer struct{ events []ResolutionTraceEvent }

func (c *chaos4120Tracer) Trace(event ResolutionTraceEvent) { c.events = append(c.events, event) }

func (c *chaos4120Tracer) eventsByStage(stage string) []ResolutionTraceEvent {
	var out []ResolutionTraceEvent
	for _, e := range c.events {
		if e.Stage == stage {
			out = append(out, e)
		}
	}
	return out
}

// TestChaos4120_SearchEmitsItsOwnTruncationPerTerm is the EMISSION pin for
// resolve.go's newly per-event Truncated field on the "search" stage: before
// this, a per-term Search() truncation only ever fed the pooled
// resolution-wide searchTruncated flag, so no per-event trace field could
// tell a reader which term (if any) truncated.
func TestChaos4120_SearchEmitsItsOwnTruncationPerTerm(t *testing.T) {
	t.Parallel()
	tracer := &chaos4120Tracer{}
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{"alpha": {candidateNode(contextfabric.SubjectRepository, "repo_1", "Alpha", 0.9, "*")}},
		searchTruncated: true,
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted("alpha"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	events := tracer.eventsByStage("search")
	if len(events) == 0 {
		t.Fatal("no \"search\" stage event traced")
	}
	for _, e := range events {
		if !e.Truncated {
			t.Errorf("search event for a truncated backend has Truncated=false, want true (event: %+v)", e)
		}
	}
}

// TestChaos4120_SearchQuestionEmitsItsOwnStageEvent is the EMISSION pin for
// the question-level pass's own trace event -- before this ticket, the
// SearchQuestion call traced NOTHING of its own; its truncated/degraded
// outcome only ever reached the pooled resolution-wide flags, making it
// indistinguishable on any trace from a per-term "search" truncation.
func TestChaos4120_SearchQuestionEmitsItsOwnStageEvent(t *testing.T) {
	t.Parallel()
	tracer := &chaos4120Tracer{}
	request := testRequest()
	node := candidateNode(contextfabric.SubjectProject, "project_only_question", "Only Question", 0.9, "*")
	backend := &fakeGraphBackend{
		enableSearchQuestion:    true,
		searchResults:           map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults:   map[string][]CandidateNode{request.Question: {node}},
		searchQuestionTruncated: true,
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}

	questionEvents := tracer.eventsByStage("search_question")
	if len(questionEvents) != 1 {
		t.Fatalf("got %d \"search_question\" events, want exactly 1 (the pass runs at most once per resolution)", len(questionEvents))
	}
	if !questionEvents[0].Truncated {
		t.Error("search_question event Truncated = false, want true (the fixture reported truncation)")
	}
	if questionEvents[0].SearchResultCount != 1 {
		t.Errorf("search_question event SearchResultCount = %d, want 1", questionEvents[0].SearchResultCount)
	}

	// The per-term "search" pass's own event must NOT report the question
	// pass's truncation -- the whole point of splitting the stage is that
	// the two are independently readable.
	searchEvents := tracer.eventsByStage("search")
	for _, e := range searchEvents {
		if e.Truncated {
			t.Errorf("per-term search event reports Truncated=true, but only the fixture's SearchQuestion call was configured to truncate (event: %+v)", e)
		}
	}
}

// TestChaos4120_SearchQuestionNotTruncatedReportsFalse is the negative
// case: a question pass that did NOT truncate must trace Truncated=false,
// not merely omit the event.
func TestChaos4120_SearchQuestionNotTruncatedReportsFalse(t *testing.T) {
	t.Parallel()
	tracer := &chaos4120Tracer{}
	request := testRequest()
	backend := &fakeGraphBackend{
		enableSearchQuestion:  true,
		searchResults:         map[string][]CandidateNode{"alpha": {}},
		searchQuestionResults: map[string][]CandidateNode{request.Question: {}},
	}
	deps := backend.deps()
	deps.ResolutionTracer = tracer
	if _, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, request, testInterpreted("alpha"), deps, nil, nil); err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	questionEvents := tracer.eventsByStage("search_question")
	if len(questionEvents) != 1 {
		t.Fatalf("got %d \"search_question\" events, want exactly 1", len(questionEvents))
	}
	if questionEvents[0].Truncated {
		t.Error("search_question event Truncated = true, want false (the fixture did not report truncation)")
	}
}
