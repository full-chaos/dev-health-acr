package devhealthfacts_test

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// KNOWN GAP, pinned on purpose. The link-state statement reads work_graph_issue_pr
// by its key prefix (org_id, repo_id) so its cost stays flat in the
// organization (TestTeamPullRequestLinkStateCostIsFlatWithLinkedIssuesOnRealClickHouse).
// The fallback-only rule therefore sees the native rows of an issue only in the
// repositories the read asks about. A fallback link of an issue whose only native
// link is in a repository nobody asked about still makes the pull request look
// linked, so the "pull requests without linked work" reason is not given for it.
// Exposure is none while the write side keeps the rule at the source (the ops
// issue-PR link writer drops a lower-tier link where a native one exists, in
// production since the rule was enforced there). A design that finds the native
// row without reading the organization turns this test red on purpose: rewrite
// it to expect the reason then.
func TestTeamPullRequestLinkStateKnownGapNativeLinkInAnotherRepository(t *testing.T) {
	ctx := context.Background()
	query, direct := newCHAOS3780IntegrationClient(t, ctx)
	const orgID = "org-pr-link-known-gap"
	s := newPRLinkSeed(t, ctx, direct, orgID)
	inWindow := ts(2026, 9, 10, 0, 0, 0)
	s.team("team-gap", "repo-gap")
	s.pullRequest("repo-gap", 1, inWindow, nil)
	s.link("repo-gap", "issue-gap", 1, "heuristic")
	s.link("repo-holder", "issue-gap", 7, "native")
	provider := findProvider(t, devhealthfacts.NewProviders(query), contextfabric.FactInvestment)
	start, end := ts(2026, 9, 1, 0, 0, 0), ts(2026, 9, 25, 0, 0, 0)
	result, err := provider.ReadFacts(ctx, storage.Principal{OrgID: orgID}, contextfabric.FactQuery{
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalRange, Start: &start, End: &end},
		Kind: contextfabric.FactInvestment, Subjects: []contextfabric.SubjectRef{teamSubject("team-gap")},
	})
	if err != nil {
		t.Fatalf("ReadFacts: %v", err)
	}
	reason := contextfabric.CombineRequestNote(result.Reason, result.RequestNote) + " | " + result.SubjectReasons[contextfabric.FactSubjectKey(teamSubject("team-gap"))]
	const token = "investment_team_prs_without_linked_work"
	if strings.Contains(reason, token) {
		t.Fatalf("known gap changed: reason = %q now names %s, so the native link in another repository covers the fallback link; if that is intended the gap is closed: rewrite this test", reason, token)
	}
}
