// Package ownershipresolve holds the ONE definition of how a
// team_repo_ownership row resolves to a repository (CHAOS-7073 ruling K11,
// shared with the graph edge by CHAOS-7119). It is a leaf: it imports nothing
// from this module, so both the fact reads (devhealthfacts) and the graph
// projection (devhealthsource) can depend on it without depending on each
// other.
//
// Every read that asks "which repositories does this team own" resolves
// team_repo_ownership rows the way ops does (dev-health-ops
// internal/queryapi/teamscope/teamscope.go RepoCondition, origin/main). A NULL
// repo_id does not mean unresolved: the GitHub team-autoimport writer leaves
// repo_id NULL on every row and carries only repo_full_name, so a read that
// drops those rows (`repo_id IS NOT NULL`) silently loses every repository
// that team owns. The row is resolved against repos by (org_id, provider,
// lower-cased name).
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
//   - r.provider = o.provider: a full name is not unique across providers.
//   - the resolved id must exist in repos for this org, on both branches:
//     ClickHouse enforces no foreign key, and an ownership row that outlived
//     its repository must not stay readable for a team alone. (The graph edge
//     opts out of this one clause only, see Options.KeepUnresolved.)
//
// acr conventions kept: repos is read FINAL (ReplacingMergeTree(last_synced)
// keyed (org_id, id), so FINAL yields each repository's latest row, the same
// row argMax(..., last_synced) GROUP BY id picks), and the caller's filter is
// applied inside the team_repo_ownership read exactly where every caller
// applied it before.
package ownershipresolve

import "strings"

// RepoKeyExpr is the resolved repository id, as a string: the row's own
// repo_id wins, the name match is the fallback. Only meaningful where
// ResolvedPredicate holds.
const RepoKeyExpr = `coalesce(toString(o.repo_id), toString(r.id))`

// ResolvedPredicate is true when the row names a repository: its own repo_id,
// or a name match proven by the join's `matched` sentinel.
const ResolvedPredicate = `(o.repo_id IS NOT NULL OR r.matched = 1)`

// Options widens the derived table for a caller that needs more than the
// default (team_id, repo_key, repo_full_name). The zero value renders the
// fact reads' table byte for byte.
type Options struct {
	// OwnershipColumns are extra team_repo_ownership columns read into the
	// `o` subquery, after org_id, provider, team_id, repo_id, repo_full_name.
	OwnershipColumns []string
	// Columns are extra output expressions (over `o` and `r`), appended after
	// the default three.
	Columns []string
	// KeepUnresolved is the graph edge's mode (CHAOS-7119). Every ownership
	// row survives: a row that resolves to nothing carries repo_key '' (so
	// the caller can count it and move its cursor past it instead of losing
	// it silently), and a non-NULL repo_id is kept whether or not repos holds
	// it -- the edge projects that case to the orphaned-repository sentinel
	// (default pending a ruling; the fact reads drop it, as ops does). The
	// resolution itself (own id wins, provider, lower-cased name, matched
	// sentinel) is unchanged.
	KeepUnresolved bool
}

// OwnedRepositoriesSource returns a derived table of team_repo_ownership rows
// resolved to repositories: one row per ownership row that survives filter
// (appended after `WHERE org_id = {org_id:String}`, so it starts with " AND")
// and, unless opts.KeepUnresolved, resolves to a repository present in repos.
// repo_key is the repository UUID as a string. Callers dedupe (DISTINCT /
// GROUP BY) as they did over the raw table.
func OwnedRepositoriesSource(filter string, opts Options) string {
	repoKey := RepoKeyExpr
	if opts.KeepUnresolved {
		repoKey = `if(` + ResolvedPredicate + `, ` + RepoKeyExpr + `, '')`
	}
	var extraOutput, extraOwnership string
	for _, column := range opts.Columns {
		extraOutput += ",\n\t\t" + column
	}
	if len(opts.OwnershipColumns) > 0 {
		extraOwnership = ", " + strings.Join(opts.OwnershipColumns, ", ")
	}
	statement := `(
	SELECT o.team_id AS team_id,
		` + repoKey + ` AS repo_key,
		o.repo_full_name AS repo_full_name` + extraOutput + `
	FROM (
		SELECT org_id, provider, team_id, repo_id, repo_full_name` + extraOwnership + `
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
		   AND lower(r.repo) = lower(o.repo_full_name)`
	if !opts.KeepUnresolved {
		statement += `
	WHERE ` + ResolvedPredicate + `
	  AND ` + RepoKeyExpr + ` IN (
		  SELECT toString(id) AS id
		  FROM repos FINAL
		  WHERE org_id = {org_id:String}
	  )`
	}
	return statement + `
)`
}
