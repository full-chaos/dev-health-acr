package contextfabric

import (
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// A work-item question over a repository that names its period as a bare
// calendar phrase ("last month") reaches the period-role path: the window is
// committed on the previous calendar month, so a created or updated period is
// refused with the period-role sentence (not as an unservable member kind) and
// a completed period reads through the completion filter on the month's bounds.
func TestAWorkItemQuestionOverARepositoryWithACalendarPeriodReachesThePeriodRolePath(t *testing.T) {
	defer reportWorkItemMutationPanic(t)
	mcpRequest := func(question string) InvestigationRequest {
		request := statedPeriodRequest(question)
		request.Consumer.Surface = mcpSurface
		return request
	}
	walk := TreeWorkItemWalk{Members: []TreeWorkItemMember{treeMember(t, "ENG-1", TreeLinkTierNative)}, PullRequests: 1, LinkedIssues: 1}
	for _, question := range []string{
		"Which work items of acme/api were created last month?",
		"Which work items of acme/api were updated last month?",
	} {
		run := runRepositoryTree(t, repositoryTreeCase{frame: periodTupleFrame(), request: mcpRequest(question), walk: walk})
		if run.err != nil {
			t.Fatal(run.err)
		}
		if !limitationsContain(run.result.Limitations, contractsv1.ContextFabricWorkItemRepositoryPeriodRoleRefusalLimitation) {
			t.Fatalf("%q: the period-role refusal is not named: status=%s limitations=%v", question, run.result.Status, run.result.Limitations)
		}
		if run.graph.calls != 0 || len(run.filter.requests) != 0 {
			t.Fatalf("%q: a refused period walked=%d filtered=%d", question, run.graph.calls, len(run.filter.requests))
		}
	}
	filter := &treeFilterFake{keep: map[string]bool{walk.Members[0].Subject.CanonicalID: true}}
	served := runRepositoryTree(t, repositoryTreeCase{frame: periodTupleFrame(), request: mcpRequest("Which work items of acme/api were closed last month?"), walk: walk, filter: filter})
	if served.err != nil || served.graph.calls != 1 || len(filter.requests) != 1 {
		t.Fatalf("completed calendar period: err=%v walks=%d filters=%d limitations=%v", served.err, served.graph.calls, len(filter.requests), served.result.Limitations)
	}
	w := served.result.EffectiveEvidenceWindow
	if w == nil || w.Provenance != WindowQuestionStated || w.Start == nil || w.End == nil {
		t.Fatalf("effective window = %+v, want the committed calendar month", w)
	}
	wantStart, wantEnd := calendarPeriodOracle("last month", w.End.Add(time.Second))
	if !w.Start.Equal(wantStart) || !w.End.Equal(wantEnd) || !filter.requests[0].CompletedStart.Equal(wantStart) || !filter.requests[0].CompletedEnd.Equal(wantEnd) {
		t.Fatalf("window %v..%v filter %v..%v, want the previous calendar month %v..%v", w.Start, w.End, filter.requests[0].CompletedStart, filter.requests[0].CompletedEnd, wantStart, wantEnd)
	}
}
