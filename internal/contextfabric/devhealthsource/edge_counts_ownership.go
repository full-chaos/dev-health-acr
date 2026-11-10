package devhealthsource

import (
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The OWNED_BY_TEAM edge counts only MEASURE the edges the three ownership
// producers already emit; they add no attribution rule. Each reads the row
// source and GROUP BY of its producer (the *FromGroupSQL functions; the
// project arm reads the stored groups its producer pages), so a group the
// producer collapses is one edge here and a group it retracts or omits is
// none. An edge is one group: (work item, team), (project, team, source) or
// (provider, repository, team, source).

// ownedByTeamArms are the kinds of the edge's source node.
var ownedByTeamArms = []string{
	string(contractsv1.ContextFabricSubjectWorkItem),
	string(contractsv1.ContextFabricSubjectProject),
	string(contractsv1.ContextFabricSubjectRepository),
}

// repositoryOwnershipEdgeCountSQL counts the repository -> team groups
// queryRepositoryTeams projects: a group whose repository key did not resolve
// (a glob, a ghost name, a provider mismatch) is omitted there.
func repositoryOwnershipEdgeCountSQL() string {
	return `SELECT '` + string(contractsv1.ContextFabricSubjectRepository) + `' AS arm, count() AS edge_count FROM (
SELECT o.provider AS g_provider, o.repo_key AS g_repo_key, o.team_id AS g_team_id, o.source_name AS g_source
` + repositoryTeamsFromGroupSQL() + `
HAVING o.repo_key != ''
)`
}

// projectOwnershipEdgeCountSQL counts the project -> team groups
// queryProjectTeams projects, from the stored groups it pages: a live group
// (not gone) that is not suppressed (an ambiguous key or a conflicting
// identity, no row of it assertable: a retraction there), whose project's
// canonical id fits the natural-key bound (else omitted there). The store
// lags the source tables by up to one refresh, as the projector does.
func projectOwnershipEdgeCountSQL() string {
	return `SELECT '` + string(contractsv1.ContextFabricSubjectProject) + `' AS arm, count() AS edge_count
FROM project_team_ownership_groups_current FINAL
WHERE org_id = {org_id:String} AND is_deleted = 0 AND edge_suppressed = 0 AND ` + derivedIDFitsSQL(identity.KindProject, "provider", "project_id")
}

// workItemOwnershipEdgeCountSQL counts the work item -> team pairs
// queryWorkItemTeams projects: a pair with no primary or co-owner row is a
// retraction there (a candidate row alone gives no edge), and a work item whose
// canonical id is over the natural-key bound is omitted.
func workItemOwnershipEdgeCountSQL() string {
	return `SELECT '` + string(contractsv1.ContextFabricSubjectWorkItem) + `' AS arm, uniqExact(g_repo_id, g_work_item_id, g_team_id) AS edge_count FROM (
SELECT toString(w.repo_id) AS g_repo_id, a.work_item_id AS g_work_item_id, ifNull(a.team_id, '') AS g_team_id
` + workItemTeamsFromGroupSQL() + `
HAVING countIf(` + workItemTeamsQualifies + `) > 0
)
WHERE ` + derivedIDFitsSQL(identity.KindWorkItem, "g_repo_id", "g_work_item_id")
}

// ownedByTeamPopulations is one population per arm: the project statement is
// the heaviest, so each runs as its own statement under the projector's
// per-statement memory cap.
func ownedByTeamPopulations() []edgePopulation {
	return []edgePopulation{
		{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, arms: []string{string(contractsv1.ContextFabricSubjectWorkItem)}, sql: workItemOwnershipEdgeCountSQL()},
		{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, arms: []string{string(contractsv1.ContextFabricSubjectProject)}, sql: projectOwnershipEdgeCountSQL()},
		{relation: contractsv1.ContextFabricRelationshipOwnedByTeam, arms: []string{string(contractsv1.ContextFabricSubjectRepository)}, sql: repositoryOwnershipEdgeCountSQL()},
	}
}
