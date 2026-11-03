package falkorgraph

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12606, the lead's ruling (option a): an allocation question over a
// team's children is answered by the team's own investment fact, so discovery
// builds no member set and runs no member census. The walk from the committed
// anchor still runs: it is the relationship read of any committed subject.
func TestDiscoverContextReadsNoMemberSetWhenTheAnchorsFactAnswers(t *testing.T) {
	discover := func(anchorFactOnly, committed bool) (*contextfabric.Cohort, []string) {
		t.Helper()
		fake, _ := ownedRepositoryFake()
		inner := fake.queryFunc
		var mu sync.Mutex
		var censusReads []string
		fake.queryFunc = func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.Contains(cypher, "IN $kinds") {
				mu.Lock()
				censusReads = append(censusReads, cypher)
				mu.Unlock()
			}
			return inner(ctx, graphKey, cypher, params, readOnly)
		}
		anchor := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:platform", Label: "Platform"}
		request := ownershipRoutingRequest(repositoriesOfAnchorFrame("Platform"), anchor)
		request.ScopeAnchorKind = contextfabric.SubjectTeam
		request.Request.Question = "how is team Platform investing across its repositories?"
		request.Request.Options.MaxCohortMembers = 12
		request.AnchorFactOnly = anchorFactOnly
		if !committed {
			// The anchor is named but not committed: the census that a cohort
			// frame with no committed subject is eligible for must not run.
			request.Resolution = contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{}}
			request.ScopeAnchorResolved = false
		}
		result, err := newTeamAdapter(t, fake).DiscoverContext(context.Background(), storage.Principal{OrgID: "org-1"}, request)
		if err != nil {
			t.Fatalf("DiscoverContext() error = %v", err)
		}
		return result.Cohort, censusReads
	}
	if cohort, _ := discover(false, true); cohort == nil || len(cohort.Members) == 0 {
		t.Fatalf("control: the frame without the flag served cohort %+v, want the owned repositories", cohort)
	}
	if _, censusReads := discover(false, false); len(censusReads) == 0 {
		t.Fatal("control: with no committed anchor and no flag, the member census did not run")
	}
	for _, committed := range []bool{true, false} {
		cohort, censusReads := discover(true, committed)
		if cohort != nil {
			t.Fatalf("committed=%v: cohort = %+v, want none: the anchor's own fact answers", committed, cohort)
		}
		if len(censusReads) != 0 {
			t.Fatalf("committed=%v: a member census ran: %v", committed, censusReads)
		}
	}
}
