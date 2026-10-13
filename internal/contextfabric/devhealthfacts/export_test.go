package devhealthfacts

import (
	"time"

	"github.com/full-chaos/dev-health-go/readers"
)

func SetClockForTest(now func() time.Time) (restore func()) {
	previous := clock
	clock = now
	return func() { clock = previous }
}

func StatusSinceStatementForTest(settings readers.Settings) string {
	return statusSinceStatement(settings)
}

func TeamProjectLinkRowsSQLForTest() string {
	return teamProjectLinkRowsSQL("ids", factTimeBound{})
}

// TeamProjectLinkRowsPreStreamingSQLForTest is the team project link rows
// statement as it read before the organization's link rows and pull requests
// were streamed past the owned issues: a differential oracle only.
func TeamProjectLinkRowsPreStreamingSQLForTest() string {
	bound := factTimeBound{}
	owned := `(SELECT p.provider AS provider, p.join_key AS join_key FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
    INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1 AND t.team_id IN {ids:Array(String)})`
	issues := `(SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN ` + linkedWorkItemIDsSQL + `
      AND (cw.provider, cw.project_id) IN ` + owned + ` AND ` + workItemIsIssueSQL("cw") + `)`
	return `(
  SELECT DISTINCT t.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
  INNER JOIN ` + issues + ` AS w ON w.provider = p.provider AND w.project_id = p.join_key
  INNER JOIN ` + admittedLinkedPullRequestRowsSQL + ` AS l ON l.work_item_id = w.work_item_id
  WHERE p.key_resolution_count = 1 AND t.team_id IN {ids:Array(String)}
)`
}
