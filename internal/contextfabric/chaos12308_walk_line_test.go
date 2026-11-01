package contextfabric

import (
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12308: the repository walk line carries the pull requests the walk
// crossed, per tier, by names that do not read as the answer's count.
func TestTheRepositoryWalkLineNamesTheCrossedPullRequests(t *testing.T) {
	records := captureSlogJSON(t, func(logger *slog.Logger) {
		NewSlogEngineTelemetry(logger).RecordRepositoryWorkItemWalk(context.Background(), storage.Principal{OrgID: "org_sink_test"},
			RepositoryWorkItemWalkEvent{Outcome: RepositoryWorkItemWalkMembers, Measured: true, Members: 2, Crossed: CrossedPullRequests{ExplicitText: 4, Heuristic: 5}})
	})
	if len(records) != 1 || records[0]["walk_crossed_pull_requests_explicit_text"] != float64(4) || records[0]["walk_crossed_pull_requests_heuristic"] != float64(5) {
		t.Fatalf("records = %v, want the crossed counts", records)
	}
}
