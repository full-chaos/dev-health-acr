package devhealthfacts_test

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestChaos5405_TheRepoLessCensusDescribesTheWholeRelationNotThePage pins
// codex r2 F2.
//
// RepoLessCandidateCount was the one count on this record derived by counting
// rows in Go instead of reading a window aggregate. The selection is bounded
// (LIMIT limit+1) and orders admissible rows first, so a denied repo-less row
// beyond the page is never seen -- while CandidateCount, taken from
// scoped_population, describes the whole relation. Reproduced before the fix:
//
//	candidate=2 repo_less_candidate=0 repo_less_denied=1 auth_dropped=1 rows_returned=1
//
// repo_less_candidate=0 with repo_less_denied=1 is not merely low, it is
// self-contradictory: the denied repo-less rows are a SUBSET of the repo-less
// candidates. D-a's rule -- "a count and page obtained from unrelated
// observations cannot constitute a complete census" -- is what this violates,
// and the fix is the one the rest of the function already uses: read the
// aggregate.
//
// r1 moved this increment to BEFORE the authorization gate and that was a real
// fix to a real defect; it was simply on the wrong axis. Being counted before
// the mask does not help a row that is not on the page at all.
func TestChaos5405_TheRepoLessCensusDescribesTheWholeRelationNotThePage(t *testing.T) {
	t.Parallel()

	// TWO rows in the relation: one authorized non-repo-less row, which is
	// returned, and one DENIED repo-less row, which is not -- so the fake
	// hands back exactly ONE row while the aggregates describe both.
	client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405Row(chaos5405RepoID, "WIDGET-101", chaos5405RepoSlug, chaos5405ProjectID, "native_team",
			chaos5405Census{Scoped: 2, Authorized: 1, RepoLess: 1, RepoLessDenied: 1}),
	}}}}
	result, err := chaos5405Expand(t, client, orgWidePrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 200)
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	counts := result.Counts

	if counts.ScopeRowsReturned != 1 {
		t.Fatalf("scope_rows_returned = %d, want 1 -- the setup is meant to return FEWER rows than the population, and if it does not the assertions below prove nothing",
			counts.ScopeRowsReturned)
	}
	if counts.RepoLessCandidateCount != 1 {
		t.Errorf("repo_less_candidate_count = %d, want 1 from the whole 2-row relation -- it was counted from the returned page instead of read from repo_less_population",
			counts.RepoLessCandidateCount)
	}
	// THE INVARIANT, stated as an invariant rather than as a number: the
	// denied repo-less rows are a subset of the repo-less candidates, and the
	// repo-less candidates are a subset of the scoped population. A census
	// that breaks either is describing two different observations.
	if counts.RepoLessAuthorizationDroppedCount > counts.RepoLessCandidateCount {
		t.Errorf("repo_less_authorization_dropped_count = %d exceeds repo_less_candidate_count = %d -- a subset cannot be larger than the set it is drawn from",
			counts.RepoLessAuthorizationDroppedCount, counts.RepoLessCandidateCount)
	}
	if counts.RepoLessCandidateCount > counts.CandidateCount {
		t.Errorf("repo_less_candidate_count = %d exceeds candidate_count = %d",
			counts.RepoLessCandidateCount, counts.CandidateCount)
	}
}

// TestChaos5405_TheRepoLessCensusCountsAuthorizedRowsToo is the other half.
//
// A fix that reads repo_less_denied and calls it the candidate count would
// satisfy the test above while under-reporting every AUTHORIZED repo-less row.
// The candidate population is pre-authorization, so it counts both.
func TestChaos5405_TheRepoLessCensusCountsAuthorizedRowsToo(t *testing.T) {
	t.Parallel()

	// THREE repo-less rows in the relation, TWO of them authorized and one
	// denied. A count taken from repo_less_denied would report 1.
	client := &fakeClient{tables: []fakeTable{{match: chaos5405SelectionMatch, rows: [][]any{
		chaos5405Row(chaos5405ZeroRepoID, "linear:CHAOS-9001", "", chaos5405ProjectID, "native_team",
			chaos5405Census{Scoped: 3, Authorized: 2, RepoLess: 3, RepoLessDenied: 1}),
		chaos5405Row(chaos5405ZeroRepoID, "linear:CHAOS-9002", "", chaos5405ProjectID, "native_team",
			chaos5405Census{Scoped: 3, Authorized: 2, RepoLess: 3, RepoLessDenied: 1}),
	}}}}
	result, err := chaos5405Expand(t, client, orgWidePrincipal(),
		contextfabric.FactScopePolicyProjectWorkItemStatus, contextfabric.FactStatus,
		chaos5405ProjectOrigin(t), 200)
	if err != nil {
		t.Fatalf("ExpandFactScope error = %v", err)
	}
	if got := result.Counts.RepoLessCandidateCount; got != 3 {
		t.Fatalf("repo_less_candidate_count = %d, want 3 -- the candidate population is PRE-authorization and counts authorized repo-less rows as well as denied ones", got)
	}
	if got := result.Counts.RepoLessAuthorizationDroppedCount; got != 1 {
		t.Fatalf("repo_less_authorization_dropped_count = %d, want 1 -- the control is broken if the two counts have been collapsed into one", got)
	}
}
