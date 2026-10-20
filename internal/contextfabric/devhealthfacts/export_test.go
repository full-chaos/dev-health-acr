package devhealthfacts

import (
	"context"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/relationcurrent"
	"strings"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/ext"

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
	probe := `SELECT s, t, toUInt8(NOT ended), toUInt8(code = 4) FROM ` + relationcurrent.BlockerRowsSQL()
	return strings.ReplaceAll(probe, "{org_id:String}", "'"+orgID+"'")
}

func TeamProjectLinkRowsSQLForTest() string {
	return teamProjectLinkRowsSQL("ids", factTimeBound{})
}

// teamLinkCandidatesSQL is the team link candidate set as a subquery: the
// form a statement inlines it in when it runs without the external table.
func teamLinkCandidatesSQL(bound factTimeBound) string {
	return `(SELECT cp.work_item_id FROM work_items AS cp WHERE ` + teamLinkCandidatesPredicate(bound) + `)`
}

// inlineTeamLinkCandidates puts the team link candidates the statement takes
// as an external table back into its text.
func inlineTeamLinkCandidates(statement string, bound factTimeBound) string {
	return strings.ReplaceAll(statement, teamLinkCandidatesSent, teamLinkCandidatesSQL(bound))
}

// TeamLinkCandidatesTableName is the external table the team project link rows
// read their candidates from.
const TeamLinkCandidatesTableName = teamLinkCandidatesTable

// TeamLinkCandidatesStatementForTest is the statement that reads the team
// link candidates (no bound): one row, one Array(String).
func TeamLinkCandidatesStatementForTest() string {
	return teamLinkCandidatesStatement(factTimeBound{})
}

// TeamLinkCandidatesTableForTest is the external table the team project link
// rows read their candidates from.
func TeamLinkCandidatesTableForTest(ids []string) (*ext.Table, error) {
	return teamLinkCandidatesExternalTable(ids)
}

// TeamLinkCandidatesContextForTest reads the teams' link candidates through
// client (no bound) and returns ctx carrying them, empty or not.
func TeamLinkCandidatesContextForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string) (context.Context, error) {
	var ids []string
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamLinkCandidates", teamLinkCandidatesStatement(factTimeBound{}), orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&ids)
	})
	if err != nil {
		return nil, err
	}
	table, err := teamLinkCandidatesExternalTable(ids)
	if err != nil {
		return nil, err
	}
	return clickhousedriver.Context(ctx, clickhousedriver.WithExternalTable(table)), nil
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

// TeamProjectLinkRowsTwoPassSQLForTest is the team project link rows statement
// as main carried it before the owned projects were resolved once: the before
// side of the memory comparison.
func TeamProjectLinkRowsTwoPassSQLForTest() string {
	bound := factTimeBound{}
	owned := `(SELECT p.provider AS provider, p.join_key AS join_key FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
    INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1 AND t.team_id IN {ids:Array(String)})`
	candidates := `(SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cp.work_item_id IN ` + linkedWorkItemIDsSQL + `
      AND (cp.provider, cp.project_id) IN ` + owned + `)`
	issues := `(SELECT cw.work_item_id AS work_item_id, cw.provider AS provider, cw.project_id AS project_id FROM work_items AS cw FINAL
    WHERE cw.org_id = {org_id:String} AND cw.work_item_id IN ` + candidates + `
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
		"edge_rows_repo":      "(" + blockerEdgeRowsSQL(blockerOpenPredicate, authorized, relationcurrent.BlockerCurrentSQL("d"), true) + ")",
		"edge_rows_project":   "(" + blockerProjectEdgeRowsSQL(blockerOpenPredicate, authorized, relationcurrent.BlockerCurrentSQL("d"), true) + ")",
		"owned_projects":      "(SELECT t.team_id FROM " + teamOwnedProjectsAsOfSQL(factTimeBound{}) + " AS t INNER JOIN " + resolvedProjectsSQL + " AS p ON p.provider = t.provider AND p.id = t.id WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)})",
		"project_link_rows":   teamProjectLinkRowsAmongSQL("team_ids", factTimeBound{}, blockerEndpointIDsSQL),
		"work_items_s":        "(SELECT * FROM " + blockerWorkItemsSQL("s") + ")",
		"relation_ended_keys": relationcurrent.BlockerEndedKeysSQL(),
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

func CommitmentAuthorizedItemIDsSQLForTest(principal storage.Principal, requestedScope []string) string {
	authorized, _ := commitmentAuthorizedItemIDsSQL(principal, requestedScope)
	return authorized
}

// SetFileHotspotSettingsForTest replaces the settings clause of the file
// hotspot statements; restore puts the production clause back.
func SetFileHotspotSettingsForTest(clause string) (restore func()) {
	previous := fileHotspotSettings
	fileHotspotSettings = clause
	return func() { fileHotspotSettings = previous }
}

// ForceLibraryAuthorizationForTest makes the organization-wide scope take the
// library relation again (the shape before the fast path), a negative control
// for the memory ceilings.
func ForceLibraryAuthorizationForTest() (restore func()) {
	previous := libraryAuthorizationForced
	libraryAuthorizationForced = true
	return func() { libraryAuthorizationForced = previous }
}

// SetBlockerMaxBlockSizeForTest sets the rows per block of the blocker
// statements (0 leaves the server default), for a measurement.
func SetBlockerMaxBlockSizeForTest(rows int) (restore func()) {
	previous := blockerMaxBlockSize
	blockerMaxBlockSize = rows
	return func() { blockerMaxBlockSize = previous }
}

// BlockerStatementForTest renders a statement with the blocker settings.
func BlockerStatementForTest(statement string, settings readers.Settings) string {
	return blockerStatement(statement, settings)
}

// SetBlockerStartVariantForTest selects an experimental first-seen narrowing
// of the age statements, for a measurement.
func SetBlockerStartVariantForTest(variant string) (restore func()) {
	previous := blockerStartVariant
	blockerStartVariant = variant
	return func() { blockerStartVariant = previous }
}
