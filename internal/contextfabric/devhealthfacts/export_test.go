package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
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

// BlockerFragmentNames are the parts RunBlockerFragmentsForTest runs on their
// own; each statement carries its name as the literal 'frag-<name>'.
var BlockerFragmentNames = []string{"endpoints", "authorized", "admitted_links", "edge_rows_repo", "edge_rows_project", "owned_projects", "project_link_rows", "work_items_s"}

// RunBlockerFragmentsForTest runs each part of the team blocker statements
// alone, under the work item reader settings, so a test can read each part's
// peak memory from the server's query log.
func RunBlockerFragmentsForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, principal storage.Principal, orgID string, repoKeys, teamIDs []string) error {
	authorized, authorizationBindings := workItemAuthorizedIDsAmongSQL(workItemRepositoryAuthorization(principal, nil), blockerEndpointIDsSQL)
	settings, err := workItemReaderSettings(ctx)
	if err != nil {
		return err
	}
	fragments := map[string]string{
		"endpoints":         blockerEndpointIDsSQL,
		"authorized":        authorized,
		"admitted_links":    admittedIssueLinksAmongSQL(blockerEndpointIDsSQL),
		"edge_rows_repo":    "(" + blockerEdgeRowsSQL(blockerOpenPredicate, authorized, true) + ")",
		"edge_rows_project": "(" + blockerProjectEdgeRowsSQL(blockerOpenPredicate, authorized, true) + ")",
		"owned_projects":    "(SELECT t.team_id FROM " + teamOwnedProjectsAsOfSQL(factTimeBound{}) + " AS t INNER JOIN " + resolvedProjectsSQL + " AS p ON p.provider = t.provider AND p.id = t.id WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)})",
		"project_link_rows": teamProjectLinkRowsAmongSQL("team_ids", factTimeBound{}, blockerEndpointIDsSQL),
		"work_items_s":      "(SELECT * FROM " + blockerWorkItemsSQL("s") + ")",
	}
	for _, name := range BlockerFragmentNames {
		statement := readers.WithSettings("SELECT 'frag-"+name+"', count() FROM "+fragments[name], settings)
		extra := append([]readers.Binding{{Name: "team_ids", Value: teamIDs}}, authorizationBindings...)
		if err := readers.QueryOrgScopedNamed(ctx, client, "Fragment"+name, statement, orgID, repoKeys, func(row readers.RowScanner) error {
			var tag string
			var n uint64
			return row.Scan(&tag, &n)
		}, extra...); err != nil {
			return err
		}
	}
	return nil
}
