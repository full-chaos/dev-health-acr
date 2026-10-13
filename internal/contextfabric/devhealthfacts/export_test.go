package devhealthfacts

import (
	"context"
	"strings"
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

// RelationCurrentProbeForTest is the relation end rule as one statement over
// every blocker row of orgID: source, target, whether the provider still
// reports the row, and whether its writer could not be derived.
func RelationCurrentProbeForTest(orgID string) string {
	probe := `SELECT s, t, toUInt8(NOT ended), toUInt8(code = 4) FROM ` + relationRowsSQL()
	return strings.ReplaceAll(probe, "{org_id:String}", "'"+orgID+"'")
}

// BlockerFragmentNames are the parts RunBlockerFragmentsForTest runs on their
// own; each statement carries its name as the literal 'frag-<name>'.
var BlockerFragmentNames = []string{"endpoints", "authorized", "admitted_links", "edge_rows_repo", "edge_rows_project", "owned_projects", "project_link_rows", "work_items_s", "relation_ended_keys"}

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
		"endpoints":           blockerEndpointIDsSQL,
		"authorized":          authorized,
		"admitted_links":      admittedIssueLinksAmongSQL(blockerEndpointIDsSQL),
		"edge_rows_repo":      "(" + blockerEdgeRowsSQL(blockerOpenPredicate, authorized, relationCurrentSQL("d"), true) + ")",
		"edge_rows_project":   "(" + blockerProjectEdgeRowsSQL(blockerOpenPredicate, authorized, relationCurrentSQL("d"), true) + ")",
		"owned_projects":      "(SELECT t.team_id FROM " + teamOwnedProjectsAsOfSQL(factTimeBound{}) + " AS t INNER JOIN " + resolvedProjectsSQL + " AS p ON p.provider = t.provider AND p.id = t.id WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)})",
		"project_link_rows":   teamProjectLinkRowsAmongSQL("team_ids", factTimeBound{}, blockerEndpointIDsSQL),
		"work_items_s":        "(SELECT * FROM " + blockerWorkItemsSQL("s") + ")",
		"relation_ended_keys": relationEndedKeysSQL(),
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

// AuthorizedIDsForTest renders the authorized-ids set of principal's scope over
// every work item of the organization. mode "production" is what the readers
// use; "library" is the library relation for the same scope; "fast" forces the
// organization-wide shape whatever the scope (a planted defect for the test).
func AuthorizedIDsForTest(principal storage.Principal, requested []string, mode string) (string, []readers.Binding) {
	scope := workItemRepositoryAuthorization(principal, requested)
	switch mode {
	case "library":
		return workItemAuthorizedIDsLibrarySQL(scope, "")
	case "fast":
		return `(SELECT w.work_item_id FROM work_items AS w WHERE w.org_id = {org_id:String} GROUP BY w.work_item_id)`, nil
	}
	return workItemAuthorizedIDsAmongSQL(scope, "")
}

// OrganizationWideAuthorizationForTest reports whether the blocker statements
// take the organization-wide shape for this live principal and request.
func OrganizationWideAuthorizationForTest(principal storage.Principal, requested []string) bool {
	return organizationWideAuthorization(workItemRepositoryAuthorization(principal, requested))
}

// SetBlockerAuthorizedIDsBoundForTest moves the bound above which the blocker
// statements carry the authorization relation themselves.
func SetBlockerAuthorizedIDsBoundForTest(bound int) (restore func()) {
	previous := blockerAuthorizedIDsBound
	blockerAuthorizedIDsBound = bound
	return func() { blockerAuthorizedIDsBound = previous }
}

// OrganizationWideAuthorizationScopeForTest is the same decision over a raw
// scope, for the repository-id dimensions a principal cannot express.
func OrganizationWideAuthorizationScopeForTest(scope readers.AuthorizationScope) bool {
	return organizationWideAuthorization(scope)
}

// SetBlockerEndedLinksBoundForTest moves the bound above which the blocker
// statements carry the end rule themselves.
func SetBlockerEndedLinksBoundForTest(bound int) (restore func()) {
	previous := blockerEndedLinksBound
	blockerEndedLinksBound = bound
	return func() { blockerEndedLinksBound = previous }
}

// BlockerEndedLinksPlanProbeForTest is an EXPLAIN of the bound end rule over the
// dependency table, for a test that checks the bound arrays are not folded into
// the plan as literals repeated at every step.
func BlockerEndedLinksPlanProbeForTest(orgID string) string {
	statement := `EXPLAIN actions = 1 SELECT count() FROM work_item_dependencies AS d FINAL WHERE d.org_id = {org_id:String} AND lower(ifNull(d.relationship_type, '')) = '` + blockerRelationshipType + `'` + blockerEndedLinksCurrentSQL
	return strings.ReplaceAll(statement, "{org_id:String}", "'"+orgID+"'")
}

// BlockerEndedLinkBindingNamesForTest are the three binding names of the end rule.
func BlockerEndedLinkBindingNamesForTest() (source, target, relationType string) {
	return blockerEndedSourceBinding, blockerEndedTargetBinding, blockerEndedTypeBinding
}
