package devhealthfacts

// CHAOS-7073 (ruling K11): every fact read that asks "which repositories does
// this team own" resolves team_repo_ownership rows the way ops does
// (dev-health-ops internal/queryapi/teamscope/teamscope.go RepoCondition,
// origin/main). A NULL repo_id does not mean unresolved: the GitHub
// team-autoimport writer leaves repo_id NULL on every row and carries only
// repo_full_name, so a read that drops those rows (`repo_id IS NOT NULL`)
// silently loses every repository that team owns. The row is resolved here
// against repos by (org_id, provider, lower-cased name).
//
// Ops semantics this keeps, clause for clause:
//
//   - owned id = coalesce(toString(o.repo_id), toString(r.id)): a row's own
//     repo_id wins; the name match is only the fallback.
//   - (o.repo_id IS NOT NULL OR r.matched = 1): the join carries an explicit
//     `1 AS matched` sentinel and the filter tests THAT, never `r.id IS NOT
//     NULL` -- ClickHouse fills an unmatched LEFT JOIN column with the type's
//     zero value rather than NULL, so a name repos does not hold would test as
//     non-NULL.
//   - lower(r.repo) = lower(o.repo_full_name): the name match ignores case.
//   - the resolved id must exist in repos for this org, on both branches:
//     ClickHouse enforces no foreign key, and an ownership row that outlived
//     its repository must not stay readable for a team alone.
//
// acr conventions kept: repos is read FINAL (ReplacingMergeTree(last_synced)
// keyed (org_id, id), so FINAL yields each repository's latest row, the same
// row argMax(..., last_synced) GROUP BY id picks), and the validity window
// stays ownershipValidityPredicate, applied inside the team_repo_ownership
// read exactly where every caller applied it before.

// ownedRepositoriesSource returns a derived table (team_id, repo_key,
// repo_full_name) of the currently resolvable owned repositories: one row per
// team_repo_ownership row that survives filter (appended after
// `WHERE org_id = {org_id:String}`, so it starts with " AND") and resolves to
// a repository present in repos. repo_key is the repository UUID as a string.
// Callers dedupe (DISTINCT / GROUP BY) as they did over the raw table.
func ownedRepositoriesSource(filter string) string {
	return `(
	SELECT o.team_id AS team_id,
		coalesce(toString(o.repo_id), toString(r.id)) AS repo_key,
		o.repo_full_name AS repo_full_name
	FROM (
		SELECT org_id, provider, team_id, repo_id, repo_full_name
		FROM team_repo_ownership FINAL
		WHERE org_id = {org_id:String}` + filter + `
	) AS o
	LEFT JOIN (
		SELECT org_id, provider, id, repo, 1 AS matched
		FROM repos FINAL
		WHERE org_id = {org_id:String}
	) AS r
		ON r.org_id = o.org_id
		   AND r.provider = o.provider
		   AND lower(r.repo) = lower(o.repo_full_name)
	WHERE (o.repo_id IS NOT NULL OR r.matched = 1)
	  AND coalesce(toString(o.repo_id), toString(r.id)) IN (
		  SELECT toString(id) AS id
		  FROM repos FINAL
		  WHERE org_id = {org_id:String}
	  )
)`
}
