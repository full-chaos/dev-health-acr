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
// carry to reach a project the predicate selects: the project's id (even an
// empty one, which the resolution also matches) and its non-empty project
// key, in any stored version.
func requestedProjectJoinKeysSQL(projectPredicate string) string {
	return `(SELECT arrayJoin(if(ifNull(project_key, '') = '', [id], [id, ifNull(project_key, '')])) FROM projects WHERE org_id = {org_id:String} AND ` + projectPredicate + `)`
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

// teamOwnedProjectJoinKeysSQL is every (provider, project_id) a work item can
// carry to reach a project the requested teams ({team_ids}) own, in any
// stored version or validity: the projects whose id or project key equals an
// ownership row's project_id or project_key, with their ids and project keys.
// It reads the ownership and project tables directly, so the statement's own
// ownership resolution (teamOwnedProjectsSQL) still decides every row.
const teamOwnedProjectJoinKeysSQL = `(SELECT provider, arrayJoin(if(ifNull(project_key, '') = '', [id], [id, ifNull(project_key, '')])) FROM projects WHERE org_id = {org_id:String} AND ((provider, id) IN ` + teamOwnershipValuesSQL + ` OR (provider, ifNull(project_key, '')) IN ` + teamOwnershipValuesSQL + `))`

// teamOwnershipValuesSQL is every (provider, value) an ownership row of a
// requested team names a project by: its project_id (even an empty one, which
// the library's ownership arm also matches) and its non-empty project_key.
const teamOwnershipValuesSQL = `(SELECT provider, arrayJoin(if(ifNull(project_key, '') = '', [project_id], [project_id, ifNull(project_key, '')])) FROM team_project_ownership WHERE org_id = {org_id:String} AND team_id IN {team_ids:Array(String)})`

// teamOwnedProjectWorkItemIDsSQL is the ids of the work items of the projects
// the requested teams own (teamOwnedProjectJoinKeysSQL).
const teamOwnedProjectWorkItemIDsSQL = `(SELECT work_item_id FROM work_items WHERE org_id = {org_id:String} AND (provider, project_id) IN ` + teamOwnedProjectJoinKeysSQL + `)`

// teamAttributedWorkItemKeysSQL is the (repo_id, work_item_id) keys of the
// work items an attribution row names for a requested team.
const teamAttributedWorkItemKeysSQL = `(SELECT repo_id, work_item_id FROM work_item_team_attributions WHERE org_id = {org_id:String} AND team_id IN {team_ids:Array(String)})`

// teamOwnedProjectWorkItemsSQL is the work item rows (FINAL) of
// teamOwnedProjectWorkItemIDsSQL, for a join on provider and project_id.
const teamOwnedProjectWorkItemsSQL = `(SELECT work_item_id, provider, project_id, type FROM work_items FINAL WHERE org_id = {org_id:String} AND work_item_id IN ` + teamOwnedProjectWorkItemIDsSQL + `)`

// transitionSubjectsAmongSQL restricts a project_membership_transitions read
// to the sort-key prefixes (subject_kind, repo_id, subject_id) of the subject
// ids candidates names, in any stored version: every row of a kept subject is
// read, so its dedupe and window partitions are whole, and FINAL reads only
// those subjects' granules.
func transitionSubjectsAmongSQL(candidates string) string {
	return ` AND (subject_kind, repo_id, subject_id) IN (SELECT subject_kind, repo_id, subject_id FROM project_membership_transitions WHERE org_id = {org_id:String} AND subject_id IN ` + candidates + `)`
}
