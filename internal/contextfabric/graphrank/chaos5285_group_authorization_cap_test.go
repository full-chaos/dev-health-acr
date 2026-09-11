package graphrank

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// groupAuthorizationHintRequest is the grouped-cohort authorization call shape
// exactly: `count` team hints of source cohort_group_authorization, every one
// of which the backend can resolve, and the given candidate cap.
func groupAuthorizationHintRequest(count, maxSubjectCandidates int) (contextfabric.InvestigationRequest, *fakeGraphBackend) {
	backend := &fakeGraphBackend{exactHints: map[string]CandidateNode{}}
	req := testRequest()
	req.Options.MaxSubjectCandidates = maxSubjectCandidates
	hints := make([]contextfabric.SubjectHint, 0, count)
	for index := 0; index < count; index++ {
		subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: fmt.Sprintf("team:team_%03d", index), Label: fmt.Sprintf("Team %03d", index)}
		backend.exactHints[SubjectKey(subject)] = candidateNode(subject.Kind, subject.CanonicalID, subject.Label, 1, "*")
		hints = append(hints, contextfabric.SubjectHint{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: string(hintsource.CohortGroupAuthorization)})
	}
	req.RequestedScope.SubjectHints = hints
	return req, backend
}

// TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap is the GROUNDING for
// the engine's group-authorization batching, and it is a measurement of the
// real resolver rather than of a double.
//
// The grouped path authorizes its constructed groups by resolving them as
// canonical-id hints, and a group the resolver does not commit is counted as
// DENIED. So the one property of this resolver the authorization step depends
// on is: does it commit every resolvable hint it is handed? It does not. The
// caller-hint exit finalizes through FinalizeExactResolutionWithBasis at
// request.Options.MaxSubjectCandidates, and every hint past that cap is
// dropped exactly as an unauthorized one would be -- indistinguishable, from
// the caller's side, from "the principal may not see this group".
//
// That is what made groups 51..250 of a legal 250-group cohort read as denied
// and never reach the group fact read. The engine's double models this cap
// (groupAuthorizingGraph), and this test is what licenses that model: if the
// resolver ever stopped capping here, the double would be modelling a
// behaviour that no longer exists, and this test would say so.
//
// The control row is the cap itself: exactly MaxSubjectCandidates hints all
// commit, so the 51-hint row's shortfall is the cap and nothing else.
func TestTheCallerHintExitCommitsNoMoreThanItsCandidateCap(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		hints, maxCandidates, wantCommitted int
	}{
		{hints: 50, maxCandidates: 50, wantCommitted: 50}, // control: at the cap, everything commits
		{hints: 51, maxCandidates: 50, wantCommitted: 50}, // one past it: one resolvable group is lost
		{hints: 250, maxCandidates: 50, wantCommitted: 50},
	} {
		t.Run(fmt.Sprintf("hints=%d/cap=%d", row.hints, row.maxCandidates), func(t *testing.T) {
			t.Parallel()
			req, backend := groupAuthorizationHintRequest(row.hints, row.maxCandidates)
			res, _, _, _, err := ResolveSubjectsWithCommitBasis(context.Background(),
				storage.Principal{OrgID: "org_1", RepositoryScopes: []string{"*"}}, req, testInterpreted("teams"),
				backend.deps(), nil, nil, nil, "")
			if err != nil {
				t.Fatalf("ResolveSubjectsWithCommitBasis() error = %v", err)
			}
			t.Logf("hints=%d max_subject_candidates=%d committed=%d", row.hints, row.maxCandidates, len(res.Committed))
			if len(res.Committed) != row.wantCommitted {
				t.Fatalf("committed = %d, want %d -- the resolver's caller-hint cap changed, so the engine double that models it (groupAuthorizingGraph) no longer describes the resolver it stands in for",
					len(res.Committed), row.wantCommitted)
			}
		})
	}
}
