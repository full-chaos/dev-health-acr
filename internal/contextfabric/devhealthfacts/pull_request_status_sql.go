package devhealthfacts

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// pullRequestStatusSQL is the WHERE clause, on the pull request alias p, that
// keeps the pull requests holding status now, the rule of
// contextfabric.PullRequestStatusMatches: the state (lower case, trimmed) for
// open, merged and closed; for a review status the state is open and the
// review rows of the same repositories decide. Empty status, no clause. The
// statement must bind org_id and repo_ids.
func pullRequestStatusSQL(status string) (string, []contextpacket.ClickHouseBinding) {
	if !contextfabric.InPullRequestStatusVocabulary(status) {
		return "", nil
	}
	state := status
	if contextfabric.PullRequestStatusReviewDerived(status) {
		state = contextfabric.PullRequestStatusOpen
	}
	clause := `
  AND lower(trim(ifNull(p.state, ''))) = {pull_request_state:String}`
	bindings := []contextpacket.ClickHouseBinding{{Name: "pull_request_state", Value: state}}
	switch status {
	case contextfabric.PullRequestStatusAwaitingReview:
		clause += `
  AND (p.repo_id, p.number) NOT IN (
    SELECT rv.repo_id, rv.number
    FROM git_pull_request_reviews AS rv FINAL
    WHERE rv.org_id = {org_id:String} AND toString(rv.repo_id) IN {repo_ids:Array(String)}
      AND upper(trim(rv.state)) != 'PENDING')`
	case contextfabric.PullRequestStatusApproved, contextfabric.PullRequestStatusChangesRequested:
		decision := "APPROVED"
		if status == contextfabric.PullRequestStatusChangesRequested {
			decision = "CHANGES_REQUESTED"
		}
		clause += `
  AND (p.repo_id, p.number) IN (
    SELECT repo_id, number
    FROM (
      SELECT rv.repo_id AS repo_id, rv.number AS number, upper(trim(rv.state)) AS decision, rv.submitted_at AS submitted_at,
        max(rv.submitted_at) OVER (PARTITION BY rv.repo_id, rv.number) AS latest
      FROM git_pull_request_reviews AS rv FINAL
      WHERE rv.org_id = {org_id:String} AND toString(rv.repo_id) IN {repo_ids:Array(String)}
        AND upper(trim(rv.state)) IN ('APPROVED', 'CHANGES_REQUESTED'))
    WHERE submitted_at = latest
    GROUP BY repo_id, number
    HAVING uniqExact(decision) = 1 AND any(decision) = {pull_request_review_decision:String})`
		bindings = append(bindings, contextpacket.ClickHouseBinding{Name: "pull_request_review_decision", Value: decision})
	}
	return clause, bindings
}
