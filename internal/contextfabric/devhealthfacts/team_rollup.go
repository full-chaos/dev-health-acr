package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-go/readers"
)

// A team subject's deployments, incidents, pull requests and blockers are
// served as a rollup over the repositories the team OWNS (team_repo_ownership,
// resolved by ownedRepositoriesSource -- never person -> membership -> team),
// plus an explicit owned_repositories table so a client can continue per
// repository. Every kind shares this one resolution and this one window rule;
// each kind adds only its own per-repository query over the owned set.
//
// A team also owns projects (team_project_ownership), and a project reaches
// repositories only through its issues' linked pull request rows
// (teamProjectLinkRowsSQL). Pull requests and blockers add that reach (linked
// pull requests, never a reached repository's whole activity); deployments,
// incidents and metrics are repository-level and name the reached repositories
// they have no rows for.

// teamRollupBasis is the rollup_basis value every team rollup fact carries.
const teamRollupBasis = "team_owned_repositories"

// teamNoOwnedRepositoriesReason is the not_applicable reason when every
// requested team owns no resolvable repository: the kind cannot be rolled up
// for it, which is distinct from a measured zero.
const teamNoOwnedRepositoriesReason = "devhealthfacts: the team owns no repository in synced ownership, so no rollup applies; this is not a measured zero"

// teamOwnedRepositoriesOverflowReason is the failure reason when the ownership
// read hit its bound: a rollup over a partial repository set would be a wrong
// total, so the read fails loudly instead.
const teamOwnedRepositoriesOverflowReason = "query team owned repositories exceeded its bound"

// maxTeamOwnedRepositoryRows bounds the ownership read across every requested
// team. The query reads one row past it so overflow is distinguishable from a
// read of exactly this many rows.
const maxTeamOwnedRepositoryRows = 2000

// The per-repository tables of a team rollup (owned_repositories and the
// kind's repository_breakdown) serve a single-team read: they let a client
// continue per repository. A read over several teams carries the scalar
// totals and counts only, so an answer about many teams stays bounded; the
// basis field says which one the fact is.
const (
	teamRollupTablesIncludedBasis = "included"
	teamRollupTablesOmittedBasis  = "omitted_multi_team_read"
)

// teamRollupTablesIncluded says whether a read over teamCount team subjects
// carries the per-repository tables.
func teamRollupTablesIncluded(teamCount int) bool { return teamCount <= 1 }

// teamRollupTablesBasis is the repository_tables_basis value for a read.
func teamRollupTablesBasis(included bool) string {
	if included {
		return teamRollupTablesIncludedBasis
	}
	return teamRollupTablesOmittedBasis
}

const (
	rollupStartParam = "rollup_start"
	rollupEndParam   = "rollup_end"
)

// teamOwnedRepo is one repository a team owns: repos.id as a string, and the
// full name ownership carries (a label, not an identity).
type teamOwnedRepo struct {
	key  string
	name string
}

// teamOwnedRepositories reads team -> owned repositories as of bound, deduped
// and ordered by repository id. A team with none is absent from the map.
func teamOwnedRepositories(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (map[string][]teamOwnedRepo, error) {
	statement := `SELECT team_id, repo_key, any(repo_full_name) FROM ` +
		ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound)) + `
GROUP BY team_id, repo_key
ORDER BY team_id, repo_key
LIMIT ` + fmt.Sprint(maxTeamOwnedRepositoryRows+1)
	extra := make([]readers.Binding, 0, 2)
	for _, binding := range bound.bindings() {
		extra = append(extra, readers.Binding{Name: binding.Name, Value: binding.Value})
	}
	owned := map[string][]teamOwnedRepo{}
	rows := 0
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamOwnedRepositoriesRollup", statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rows++
		var teamID string
		var repo teamOwnedRepo
		if err := row.Scan(&teamID, &repo.key, &repo.name); err != nil {
			return err
		}
		owned[teamID] = append(owned[teamID], repo)
		return nil
	}, extra...); err != nil {
		return nil, err
	}
	if rows > maxTeamOwnedRepositoryRows {
		return nil, fmt.Errorf("%s", teamOwnedRepositoriesOverflowReason)
	}
	return owned, nil
}

// teamReachedRepo is one repository a team reaches through an owned project:
// the repository of a pull request linked to an issue of the project
// (work_graph_issue_pr). linkRank is the weakest provenance tier among the
// links that reached it (linkTierRankSQL: native 3, explicit_text 2,
// heuristic 1); linkedPullRequests counts the distinct linked pull requests.
type teamReachedRepo struct {
	key                string
	name               string
	linkRank           int64
	linkedPullRequests int64
}

const (
	repositoryReachOwned        = "owned_repository"
	repositoryReachOwnedProject = "owned_project_link"
)

// teamOwnedProjectsAsOfSQL is every (provider, project id, team) a team owns
// through team_project_ownership under the read's ownership rule: currently
// owned for an unbounded read, owned at or after the bound's start (the same
// "has not ended before the window" rule the owned-repository reads use) for a
// historical one. Identity-resolved by the library's two-armed project match.
func teamOwnedProjectsAsOfSQL(bound factTimeBound) string {
	return "(SELECT provider, id, team_id FROM " + readers.ProjectOwnershipCatalogJoinSQL(readers.OwnershipValidityPredicate(bound.neutral())) + ")"
}

// teamProjectLinkRowsSQL is the one team-to-linked-pull-request reach: a
// derived table (team_id, work_item_id, pr_repo_id, pr_number, link_rank) of
// the issues of every project a team owns (team_project_ownership, under the
// bound's ownership rule) joined to their admitted link rows
// (admittedIssueLinksSQL: the work item is an issue and the linked pull
// request exists). teamParam names the
// Array(String) binding holding the team ids; the bound's bindings must ride
// with the statement. It is the same chain teamRepositories walks.
func teamProjectLinkRowsSQL(teamParam string, bound factTimeBound) string {
	return `(
  SELECT DISTINCT t.team_id AS team_id, w.work_item_id AS work_item_id, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, l.link_rank AS link_rank
  FROM ` + teamOwnedProjectsAsOfSQL(bound) + ` AS t
  INNER JOIN ` + resolvedProjectsSQL + ` AS p ON p.provider = t.provider AND p.id = t.id
  INNER JOIN work_items AS w FINAL ON w.org_id = {org_id:String} AND w.provider = p.provider AND w.project_id = p.join_key
  INNER JOIN ` + admittedIssueLinksSQL + ` AS l ON l.work_item_id = w.work_item_id
  WHERE p.key_resolution_count = 1 AND t.team_id IN {` + teamParam + `:Array(String)}
)`
}

// boundReaderBindings are a bound's time parameters as reader bindings.
func boundReaderBindings(bound factTimeBound) []readers.Binding {
	var out []readers.Binding
	for _, binding := range bound.bindings() {
		out = append(out, readers.Binding{Name: binding.Name, Value: binding.Value})
	}
	return out
}

// teamProjectReachedRepositories reads team -> repositories reached through
// the team's owned projects' linked pull requests, ordered by repository id.
// A repository the team also owns appears here too; callers drop it with
// reachedOnlyRepositories so it is counted once, as owned.
func teamProjectReachedRepositories(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, bound factTimeBound) (map[string][]teamReachedRepo, error) {
	statement := `SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), ''), toInt64(min(lr.link_rank)), toInt64(uniqExact(lr.pr_number))
FROM ` + teamProjectLinkRowsSQL("ids", bound) + ` AS lr
LEFT JOIN repos AS r FINAL ON r.id = lr.pr_repo_id AND r.org_id = {org_id:String}
GROUP BY lr.team_id, lr.pr_repo_id
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + fmt.Sprint(maxTeamOwnedRepositoryRows+1)
	reached := map[string][]teamReachedRepo{}
	rows := 0
	if err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamProjectReachedRepositories", statement, orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rows++
		var teamID string
		var repo teamReachedRepo
		if err := row.Scan(&teamID, &repo.key, &repo.name, &repo.linkRank, &repo.linkedPullRequests); err != nil {
			return err
		}
		reached[teamID] = append(reached[teamID], repo)
		return nil
	}, boundReaderBindings(bound)...); err != nil {
		return nil, err
	}
	if rows > maxTeamOwnedRepositoryRows {
		return nil, fmt.Errorf("%s", teamOwnedRepositoriesOverflowReason)
	}
	return reached, nil
}

// reachedOnlyRepositories drops, per team, the reached repositories the team
// also owns: an owned repository is one repository, served as owned.
func reachedOnlyRepositories(reached map[string][]teamReachedRepo, owned map[string][]teamOwnedRepo) map[string][]teamReachedRepo {
	out := map[string][]teamReachedRepo{}
	for teamID, repos := range reached {
		ownedKeys := ownedNameByKey(owned[teamID])
		for _, repo := range repos {
			if _, ok := ownedKeys[repo.key]; ok {
				continue
			}
			out[teamID] = append(out[teamID], repo)
		}
	}
	return out
}

func reachedRepositoryCount(reached map[string][]teamReachedRepo) int {
	total := 0
	for _, repos := range reached {
		total += len(repos)
	}
	return total
}

// teamProjectReachedOnlyReason is the coverage limitation of a kind whose
// per-repository data is not linked to a project: the repositories reached
// through owned projects carry no repo-level rows for it.
func teamProjectReachedOnlyReason(count int) string {
	return fmt.Sprintf("devhealthfacts: %d repositories reached through owned projects' linked pull requests contribute no repo-level rows for this kind; this is not a measured zero", count)
}

// applyProjectReachedWithoutRows names, on a repo-level kind, the project
// reached repositories that contribute nothing: not_applicable with the cause
// when no fact was served, a reason beside the facts otherwise.
func applyProjectReachedWithoutRows(result *contextfabric.FactProviderResult, count int) {
	if count <= 0 {
		return
	}
	reason := teamProjectReachedOnlyReason(count)
	if len(result.Facts) == 0 {
		result.State = contextfabric.SourceNotApplicable
		result.Reason = reason
		return
	}
	mergeFactReadReason(result, reason)
}

// repoKeysOf is the sorted union of every team's owned repository ids.
func repoKeysOf(owned map[string][]teamOwnedRepo) []string {
	seen := map[string]bool{}
	for _, repos := range owned {
		for _, repo := range repos {
			seen[repo.key] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// rollupWindow is the period a window-bound rollup counts over. It is the same
// resolution metrics.go applies to its repository series: an explicit
// historical bound, else the server-canonical evidence window, else all_time
// (unbounded), else the platform default trailing width.
type rollupWindow struct {
	hasStart, hasEnd bool
	start, end       time.Time
	basis            string
}

func resolveRollupWindow(bound factTimeBound, evidence *contractsv1.ContextFabricRequestedEvidenceWindow, now time.Time) rollupWindow {
	switch {
	case bound.active:
		window := rollupWindow{hasEnd: true, end: bound.end, basis: "as_of"}
		if bound.hasStart {
			window.hasStart, window.start, window.basis = true, bound.start, "range"
		}
		return window
	case evidence != nil && evidence.RelativeID == contractsv1.ContextFabricRelativeWindowAllTime:
		return rollupWindow{basis: "all_time"}
	case evidence != nil && evidence.Start != nil && evidence.End != nil:
		return rollupWindow{hasStart: true, hasEnd: true, start: evidence.Start.UTC(), end: evidence.End.UTC(), basis: "evidence_window"}
	default:
		now = now.UTC()
		return rollupWindow{hasStart: true, hasEnd: true, start: now.Add(-metricsSeriesDefaultWindow), end: now, basis: "default_trailing"}
	}
}

// bindings returns the window parameters the expressions below reference.
func (w rollupWindow) bindings() []readers.Binding {
	var out []readers.Binding
	if w.hasStart {
		out = append(out, readers.Binding{Name: rollupStartParam, Value: w.start})
	}
	if w.hasEnd {
		out = append(out, readers.Binding{Name: rollupEndParam, Value: w.end})
	}
	return out
}

// dayExpr is true when a Date column falls inside the window, at day grain.
func (w rollupWindow) dayExpr(column string) string {
	parts := []string{"1"}
	if w.hasStart {
		parts = append(parts, column+" >= toDate({"+rollupStartParam+":DateTime64(6,'UTC')})")
	}
	if w.hasEnd {
		parts = append(parts, column+" <= toDate({"+rollupEndParam+":DateTime64(6,'UTC')})")
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// timestampExpr is true when a DateTime64 column falls inside the window.
func (w rollupWindow) timestampExpr(column string) string {
	parts := []string{"1"}
	if w.hasStart {
		parts = append(parts, column+" >= {"+rollupStartParam+":DateTime64(6,'UTC')}")
	}
	if w.hasEnd {
		parts = append(parts, column+" <= {"+rollupEndParam+":DateTime64(6,'UTC')}")
	}
	return "(" + strings.Join(parts, " AND ") + ")"
}

// upperExpr is true when a DateTime64 column is at or before the window end.
func (w rollupWindow) upperExpr(column string) string {
	if !w.hasEnd {
		return "1"
	}
	return "(" + column + " <= {" + rollupEndParam + ":DateTime64(6,'UTC')})"
}

func (w rollupWindow) startValue() (contextfabric.FactValue, bool) {
	if !w.hasStart {
		return contextfabric.FactValue{}, false
	}
	return contextfabric.StringFactValue(w.start.Format(time.RFC3339)), true
}

func (w rollupWindow) endValue() (contextfabric.FactValue, bool) {
	if !w.hasEnd {
		return contextfabric.FactValue{}, false
	}
	return contextfabric.StringFactValue(w.end.Format(time.RFC3339)), true
}

// ownedRepositoriesFactValue is the owned_repositories pointer table: one row
// per owned repository, keyed by the repository's canonical id (a
// repository subject reference the subject gate checks), labelled by name.
// omitted counts the rows the table bound dropped; owned_repository_count
// still carries the full total.
func ownedRepositoriesFactValue(repos []teamOwnedRepo) (value contextfabric.FactValue, omitted int, ok bool) {
	if len(repos) == 0 {
		return contextfabric.FactValue{}, 0, false
	}
	rows := make([]contextfabric.FactValueRow, 0, len(repos))
	for _, repo := range repos {
		fields := map[string]contextfabric.FactValue{"repository_id": contextfabric.StringFactValue(repo.key)}
		if repo.name != "" {
			fields["repository_name"] = contextfabric.StringFactValue(repo.name)
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: fields})
	}
	rows, omitted = capFactValueRows(rows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"repository_id"},
		Observations: []string{"repository_name"},
		Rows:         rows,
	}), omitted, true
}

// repositoryBreakdownFactValue builds a per-repository breakdown table from
// rows already carrying repository_id. measures are the numeric columns;
// observations the categorical ones.
func repositoryBreakdownFactValue(rows []contextfabric.FactValueRow, grain contextfabric.TemporalGrain, measures, observations []string) (value contextfabric.FactValue, omitted int, ok bool) {
	if len(rows) == 0 {
		return contextfabric.FactValue{}, 0, false
	}
	rows, omitted = capFactValueRows(rows)
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"repository_id"},
		Measures:     measures,
		Observations: observations,
		Grain:        grain,
		Rows:         rows,
	}), omitted, true
}

// ownedNameByKey indexes a team's owned repositories by id.
func ownedNameByKey(repos []teamOwnedRepo) map[string]string {
	out := make(map[string]string, len(repos))
	for _, repo := range repos {
		out[repo.key] = repo.name
	}
	return out
}

// applyNoOwnedRepositories folds the zero-owned-repositories outcome into a
// result: with no facts at all the kind is not_applicable for these teams; with
// other facts present the reason rides along beside them.
func applyNoOwnedRepositories(result *contextfabric.FactProviderResult, teamsWithoutRepos int) {
	if teamsWithoutRepos <= 0 {
		return
	}
	if len(result.Facts) == 0 {
		result.State = contextfabric.SourceNotApplicable
		result.Reason = teamNoOwnedRepositoriesReason
		return
	}
	mergeFactReadReason(result, teamNoOwnedRepositoriesReason)
}

// teamRollupOutcome is what one kind's team read reports back to its
// ReadFacts: how many repositories contributed data, whether a table bound
// dropped rows, the subjects skipped for shape, and the teams that own no
// repository.
type teamRollupOutcome struct {
	contributed       int
	truncated         bool
	rejected          int
	teamsWithoutRepos int
	// projectReachedWithoutRows counts the repositories reached only through
	// owned projects that a repo-level kind has no rows for.
	projectReachedWithoutRows int
	// linkRowsUnwindowed is set when a historical read counted work through
	// the issue <> pull request link rows, which carry no validity window:
	// they are read as they stand now.
	linkRowsUnwindowed bool
}

// teamProjectLinksUnwindowedReason is the limitation served beside a
// historical read that reached work through project links.
const teamProjectLinksUnwindowedReason = "devhealthfacts: project reach on a historical read uses the issue-to-pull-request links as they stand now; link rows carry no validity window"
