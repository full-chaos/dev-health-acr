package devhealthfacts

// The scope expander's statements read the requested origins' work items,
// links and transitions, never the organization's. Each set below is a
// SUPERSET of the rows a statement keeps: it reads every stored version
// (no FINAL) and matches on key or identity columns only, so the statement's
// own FINAL row, project resolution and predicates still decide every row,
// and its result is the same as over the whole organization. The cost
// follows the requested scope, not the size of the organization.

// requestedProjectIDsPredicate selects the requested projects by raw id.
const requestedProjectIDsPredicate = `id IN {project_ids:Array(String)}`

// requestedProjectKeysPredicate selects the requested projects by
// provider-qualified key (decodeProjectOriginKeys).
const requestedProjectKeysPredicate = `concat(provider, '` + projectOriginKeySeparator + `', id) IN {project_ids:Array(String)}`

// requestedProjectJoinKeysSQL is every value a work item's project_id can
// carry to reach a project the predicate selects: the project's id and its
// project key, in any stored version.
func requestedProjectJoinKeysSQL(projectPredicate string) string {
	return `(SELECT arrayJoin(arrayFilter(k -> k != '', [id, ifNull(project_key, '')])) FROM projects WHERE org_id = {org_id:String} AND ` + projectPredicate + `)`
}

// requestedProjectWorkItemIDsSQL is the ids of the work items whose current
// project_id column (in any stored version) can reach a selected project.
func requestedProjectWorkItemIDsSQL(projectPredicate string) string {
	return `(SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND project_id IN ` + requestedProjectJoinKeysSQL(projectPredicate) + `)`
}

// requestedProjectHistoryWorkItemIDsSQL adds to requestedProjectWorkItemIDsSQL
// the work items a membership transition moved into or out of a selected
// project: the as-of chain's two arms.
func requestedProjectHistoryWorkItemIDsSQL(projectPredicate string) string {
	keys := requestedProjectJoinKeysSQL(projectPredicate)
	return `(SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND project_id IN ` + keys + `
  UNION ALL
  SELECT subject_id FROM project_membership_transitions WHERE org_id = {org_id:String} AND subject_kind = 'work_item' AND (to_project_id IN ` + keys + ` OR from_project_id IN ` + keys + `))`
}

// teamOwnedProjectWorkItemIDsSQL is the ids of the work items of the projects
// the requested teams ({team_ids}) currently own, matched the way
// teamRepositories matches them (provider and resolved join key).
var teamOwnedProjectWorkItemIDsSQL = `(SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND (provider, project_id) IN (
    SELECT p.provider, p.join_key FROM ` + teamOwnedProjectsSQL + ` AS t
    INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
    WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)}))`

// teamAttributedWorkItemKeysSQL is the (repo_id, work_item_id) keys of the
// work items an attribution row names for a requested team.
const teamAttributedWorkItemKeysSQL = `(SELECT repo_id, work_item_id FROM work_item_team_attributions WHERE org_id = {org_id:String} AND team_id IN {team_ids:Array(String)})`

// teamOwnedProjectWorkItemsSQL is the work item rows (FINAL) of
// teamOwnedProjectWorkItemIDsSQL, for a join on provider and project_id.
var teamOwnedProjectWorkItemsSQL = `(SELECT work_item_id, provider, project_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + teamOwnedProjectWorkItemIDsSQL + `)`
