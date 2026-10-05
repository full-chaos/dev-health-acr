//go:build fixturegraph

package fixturegraph

import (
	"fmt"
	"testing"
)

// Each case below names a use-case the frozen worlds cannot show today. It is skipped while
// the world lacks the shape and FAILS the moment the world gains it, so the gap cannot go
// quiet: whoever refreezes a richer world must replace the case with real assertions.
func gap(t *testing.T, name, sql string) {
	t.Helper()
	got := chScalar(t, sql)
	if got != "0" {
		t.Fatalf("the seeded worlds now hold %s (count=%s): replace this skipped case with assertions for it", name, got)
	}
	t.Skipf("the frozen worlds hold no %s", name)
}

func TestGapLinkTiersBeyondNative(t *testing.T) {
	gap(t, "issue link of tier explicit_text or heuristic", fmt.Sprintf("SELECT count() FROM work_graph_issue_pr FINAL WHERE org_id = %s AND provenance != 'native'", sqlStr(orgID(t))))
}

func TestGapIssueLinkedAcrossRepositories(t *testing.T) {
	gap(t, "issue linked to pull requests of more than one repository", fmt.Sprintf("SELECT count() FROM (SELECT work_item_id FROM work_graph_issue_pr FINAL WHERE org_id = %s GROUP BY work_item_id HAVING uniqExact(repo_id) > 1)", sqlStr(orgID(t))))
}

func TestGapTeamRepositoryOwnership(t *testing.T) {
	gap(t, "team_repo_ownership row", fmt.Sprintf("SELECT count() FROM team_repo_ownership FINAL WHERE org_id = %s", sqlStr(orgID(t))))
}

func TestGapMoreThanOneIssueProvider(t *testing.T) {
	gap(t, "second issue provider", fmt.Sprintf("SELECT toString(greatest(uniqExact(provider) - 1, 0)) FROM work_items FINAL WHERE org_id = %s", sqlStr(orgID(t))))
}

func TestGapMissingMetricDay(t *testing.T) {
	gap(t, "daily metric series with a missing day", fmt.Sprintf("SELECT count() FROM (SELECT repo_id, dateDiff('day', min(day), max(day)) + 1 - count() AS missing FROM repo_metrics_daily FINAL WHERE org_id = %s GROUP BY repo_id HAVING missing > 0)", sqlStr(orgID(t))))
}
