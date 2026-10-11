package devhealthfacts

import (
	"context"
	"strconv"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// The entity tree is Repository <> Pull request <> Issue <> Project. A project
// reaches repositories only through its issues' linked pull request rows
// (work_graph_issue_pr, whose repo_id is the PULL REQUEST's repository); a
// team reaches repositories through its ownership rows and, one hop further,
// through the repositories its owned projects' issues link to. An issue's own
// repository column and a computed team attribution are never the relation.

// issueLinkTierRankSQL ranks the three provenance tiers of the link of record:
// native 3 > explicit_text 2 > heuristic 1. Any other value ranks 0 and is
// excluded by issueLinksSQL's own filter.
const issueLinkTierRankSQL = `multiIf(provenance = 'native', 3, provenance = 'explicit_text', 2, provenance = 'heuristic', 1, 0)`

// issueLinksSQL is the organization-scoped link of record, one row per
// (pull request repository, issue, pull request number), read FINAL so the
// strongest tier survives the ReplacingMergeTree merge. Rows are not
// de-duplicated per issue: a caller aggregates per repository and takes the
// strongest tier with argMax(provenance, link_rank).
const issueLinksSQL = `(
  SELECT work_item_id, repo_id AS pr_repo_id, provenance, ` + issueLinkTierRankSQL + ` AS link_rank
  FROM work_graph_issue_pr FINAL
  WHERE org_id = {org_id:String} AND ` + issueLinkTierRankSQL + ` > 0
)`

// currentResolvedProjectsSQL is the org-wide project identity resolution of
// the current-axis project chain: projects.id and projects.project_key both
// answer to a work item's project_id, and an ambiguous join key is excluded
// (key_resolution_count) rather than guessed.
const currentResolvedProjectsSQL = `(
  SELECT id, join_key, count() OVER (PARTITION BY join_key) AS key_resolution_count
  FROM (
    SELECT DISTINCT id, provider, join_key FROM (
      SELECT id, provider, id AS join_key FROM projects FINAL WHERE org_id = {org_id:String}
      UNION ALL
      SELECT id, provider, ifNull(project_key, '') AS join_key FROM projects FINAL WHERE org_id = {org_id:String} AND ifNull(project_key, '') != ''
    )
  )
)`

// projectIssueArmsAsOfSQL is the as-of set of issues a project holds: the
// membership-interval arm (history touches that overlap the window) and the
// no-history arm (an issue with no transition rows, admitted on its current
// project_id column). via_history says which arm produced the row.
const projectIssueArmsAsOfSQL = `
  SELECT iv.subject_id AS work_item_id, 1 AS via_history, p.id AS origin_project_id
  FROM ` + membershipTouchesAsOfSQL + ` AS iv
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = iv.provider AND p.join_key = iv.project_id
  WHERE p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
    AND NOT iv.is_malformed AND NOT iv.is_duplicate_add
    AND iv.observed_at <= {window_end:DateTime64(6,'UTC')}
    AND (iv.valid_to IS NULL OR iv.valid_to > {window_start:DateTime64(6,'UTC')})
  UNION ALL
  SELECT w.work_item_id AS work_item_id, 0 AS via_history, p.id AS origin_project_id
  FROM work_items AS w FINAL
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = w.provider AND p.join_key = w.project_id
  WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
    AND (w.repo_id, w.work_item_id) NOT IN (
      SELECT repo_id, subject_id FROM project_membership_transitions FINAL WHERE org_id = {org_id:String} AND subject_kind = 'work_item'
    )
`

// asOfBounds is the half-open window of a historical traversal.
type asOfBounds struct {
	start, end time.Time
}

// classifyLinkedRepository folds one aggregated repository row of a linked
// chain into the counts and candidates. A repository the link names that the
// repos catalog does not hold (empty slug) or the zero UUID is a missing next
// hop, never a fake target. It reports whether the row became a candidate.
func classifyLinkedRepository(counts *contextfabric.FactScopeExpansionCounts, candidates *[]repositoryCandidate, repoID, repoSlug, tier string, root contextfabric.SubjectRef) bool {
	if repoID == zeroRepositoryID || repoSlug == "" {
		counts.MissingNextHopCount++
		return false
	}
	counts.CandidateCount++
	*candidates = append(*candidates, repositoryCandidate{
		repoID: repoID, repoSlug: repoSlug, attributionSource: tier, originRoot: root,
	})
	return true
}

// projectUnlinkedIssueCount counts the DISTINCT issues of the requested
// projects that have no link row at all. They reach no repository through the
// tree, and the answer must say so rather than read as "nothing is missing".
func (e *ScopeExpander) projectUnlinkedIssueCount(ctx context.Context, orgID string, projectIDs []string, bounds *asOfBounds) (int, error) {
	var statement string
	bindings := []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "project_ids", Value: projectIDs},
	}
	if bounds == nil {
		statement = `SELECT uniqExact(w.work_item_id)
FROM work_items AS w FINAL
INNER JOIN ` + currentResolvedProjectsSQL + ` AS p ON p.join_key = w.project_id
WHERE w.org_id = {org_id:String} AND p.key_resolution_count = 1 AND p.id IN {project_ids:Array(String)}
  AND w.work_item_id NOT IN (SELECT work_item_id FROM ` + issueLinksSQL + `)`
	} else {
		statement = `SELECT uniqExact(a.work_item_id)
FROM (` + projectIssueArmsAsOfSQL + `) AS a
WHERE a.work_item_id NOT IN (SELECT work_item_id FROM ` + issueLinksSQL + `)`
		bindings = append(bindings,
			contextpacket.ClickHouseBinding{Name: "window_start", Value: bounds.start},
			contextpacket.ClickHouseBinding{Name: "window_end", Value: bounds.end},
		)
	}
	return e.scalarCount(ctx, statement, bindings)
}

// teamUnlinkedIssueCount is projectUnlinkedIssueCount for the issues of the
// projects a team currently owns.
func (e *ScopeExpander) teamUnlinkedIssueCount(ctx context.Context, orgID string, teamIDs []string) (int, error) {
	statement := `SELECT uniqExact(w.work_item_id)
FROM ` + teamOwnedProjectsSQL + ` AS t
INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.provider = p.provider AND w.project_id = p.join_key
WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)}
  AND w.work_item_id NOT IN (SELECT work_item_id FROM ` + issueLinksSQL + `)`
	return e.scalarCount(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "team_ids", Value: teamIDs},
	})
}

func (e *ScopeExpander) scalarCount(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (int, error) {
	rows, err := e.client.Query(ctx, statement, bindings)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count uint64
	if rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, err
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return int(count), nil
}

// teamOwnedProjectsSQL is every (provider, project id, team) a team currently
// owns through team_project_ownership, identity-resolved by the library's
// two-armed project match.
var teamOwnedProjectsSQL = "(SELECT provider, id, team_id FROM " + readers.ProjectOwnershipCatalogJoinSQL(readers.OwnershipValidityPredicate(readers.TimeBound{})) + ")"

// teamRepositoryOwnershipSource is the source label of a repository the team
// owns directly, the one team target that is a Direct edge.
const teamRepositoryOwnershipSource = "team_repo_ownership"

// teamRepositories runs the team -> repository hop on the entity tree:
// the repositories a team owns (team_repo_ownership, current rows) UNION the
// repositories its owned projects' issues link to (team_project_ownership ->
// project -> issue -> work_graph_issue_pr). work_item_team_attributions is
// NOT consulted: it attributes an issue to a team, it is not a relation of a
// team to a repository.
//
// One row per distinct repository. best_source is the strongest way the team
// reaches it: direct ownership first, then the strongest link tier. Direct
// ownership is the only edge that asserts the relationship (basis Direct);
// reaching a repository through an owned project's linked pull request is
// activity (basis ActivityProxy), with the link tier carried per target.
func (e *ScopeExpander) teamRepositories(ctx context.Context, orgID string, origins []contextfabric.SubjectRef, limit int) ([]repositoryCandidate, contextfabric.FactScopeExpansionCounts, error) {
	teamIDs := decodeTeamOriginIDs(origins)
	if len(teamIDs) == 0 {
		// Every origin failed to decode as a team id: an upstream invariant
		// violation, reported as a traversal that queried for nothing.
		return nil, contextfabric.FactScopeExpansionCounts{}, nil
	}
	limitPlusOne := limit + 1
	current := readers.OwnershipValidityPredicate(readers.TimeBound{})
	statement := `SELECT repo_id_str, repo_slug, argMin(source, source_rank) AS best_source, argMin(origin_team_id, (source_rank, origin_team_id)) AS origin_team_id
FROM (
  SELECT toString(o.repo_id) AS repo_id_str, ifNull(r.repo, '') AS repo_slug, '` + teamRepositoryOwnershipSource + `' AS source, 0 AS source_rank, o.team_id AS origin_team_id
  FROM ` + readers.TeamRepositoryOwnershipSQL(current) + ` AS o
  LEFT JOIN repos AS r FINAL ON r.id = o.repo_id AND r.org_id = {org_id:String}
  WHERE o.team_id IN {team_ids:Array(String)}
  UNION ALL
  SELECT toString(l.pr_repo_id) AS repo_id_str, ifNull(r.repo, '') AS repo_slug, l.provenance AS source, 4 - l.link_rank AS source_rank, t.team_id AS origin_team_id
  FROM ` + teamOwnedProjectsSQL + ` AS t
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
  INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.provider = p.provider AND w.project_id = p.join_key
  INNER JOIN ` + issueLinksSQL + ` AS l ON l.work_item_id = w.work_item_id
  LEFT JOIN repos AS r FINAL ON r.id = l.pr_repo_id AND r.org_id = {org_id:String}
  WHERE p.key_resolution_count = 1 AND t.team_id IN {team_ids:Array(String)}
)
GROUP BY repo_id_str, repo_slug
ORDER BY repo_id_str
LIMIT ` + strconv.Itoa(limitPlusOne)
	rows, err := e.client.Query(ctx, statement, []contextpacket.ClickHouseBinding{
		{Name: "org_id", Value: orgID},
		{Name: "team_ids", Value: teamIDs},
	})
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	defer rows.Close()

	originsByID := teamOriginsByRawID(origins)
	var candidates []repositoryCandidate
	counts := contextfabric.FactScopeExpansionCounts{}
	repositoryRows := 0
	for rows.Next() {
		var repoID, repoSlug, bestSource, originTeamID string
		if err := rows.Scan(&repoID, &repoSlug, &bestSource, &originTeamID); err != nil {
			return nil, contextfabric.FactScopeExpansionCounts{}, err
		}
		repositoryRows++
		if repoID == zeroRepositoryID || repoSlug == "" {
			counts.MissingNextHopCount++
			continue
		}
		counts.CandidateCount++
		basis := contextfabric.FactScopeBasisActivityProxy
		if bestSource == teamRepositoryOwnershipSource {
			basis = contextfabric.FactScopeBasisDirect
		}
		candidates = append(candidates, repositoryCandidate{
			repoID: repoID, repoSlug: repoSlug, basis: basis, attributionSource: bestSource,
			originRoot: originsByID[originTeamID],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.Truncated = repositoryRows >= limitPlusOne
	unlinked, err := e.teamUnlinkedIssueCount(ctx, orgID, teamIDs)
	if err != nil {
		return nil, contextfabric.FactScopeExpansionCounts{}, err
	}
	counts.MissingNextHopCount += unlinked
	// NOT trimmed to limit: the resolver's overflow-row detection needs the
	// extra row intact.
	return candidates, counts, nil
}

// teamAttributionSourceRankSQL mirrors ops' own precedence order
// (ops/src/dev_health_ops/metrics/compute_work_items.py's _SOURCE_ORDER): when
// several attributions with DIFFERENT sources reach the SAME work item, the
// lowest rank wins. An unrecognised value ranks last (7), failing toward the
// weaker classification. Read only by the team -> ISSUE traversal; the team ->
// repository hop no longer consults attributions.
const teamAttributionSourceRankSQL = `multiIf(` +
	`a.source = 'native_team', 0, ` +
	`a.source = 'issue_project', 1, ` +
	`a.source = 'project_ownership', 2, ` +
	`a.source = 'repo_ownership', 3, ` +
	`a.source = 'assignee_membership', 4, ` +
	`a.source = 'linked_issue', 5, ` +
	`a.source = 'manual_fallback', 6, ` +
	`7)`
