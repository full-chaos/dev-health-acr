package devhealthfacts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	stateCountPairSeparator  = "\x1e"
	stateCountFieldSeparator = "\x1f"
)

// pullRequestsByStateBasis is served beside the table: what the counts are and
// how to reach the pull requests themselves.
const pullRequestsByStateBasis = "counts of the pull requests of the repositories this team reaches, by stored state, not windowed; the pull requests themselves are read by asking for the pull requests of the team (a list, or a state such as open), or by reading pull_request subjects returned by find_subjects"

// pullRequestStateCounts is, for one repository, the number of pull requests
// holding each stored state. A state with no pull request is absent, never a
// zero row.
type pullRequestStateCounts map[string]int64

// pullRequestStateExpression is the state of one pull request as the per
// pull request fact serves it (readers.ReadPullRequestState): the stored
// state column on a current read, the derived merged / closed / open state on
// a bounded historical read.
func pullRequestStateExpression(alias string, bound factTimeBound) string {
	if !bound.active {
		return "ifNull(" + alias + ".state, '')"
	}
	asOf := bound.neutral().AsOfExpression()
	return "multiIf(" +
		alias + ".merged_at IS NOT NULL AND " + alias + ".merged_at <= " + asOf + ", 'merged', " +
		alias + ".closed_at IS NOT NULL AND " + alias + ".closed_at <= " + asOf + ", 'closed', " +
		"'open')"
}

// statePairsExpression folds the (state, count) rows of one group into one
// string so a repository is always one row, whatever page it lands on.
func statePairsExpression() string {
	return "arrayStringConcat(arrayMap(x -> concat(x.1, '" + stateCountFieldSeparator + "', toString(x.2)), " +
		"arraySort(groupArray((st, c)))), '" + stateCountPairSeparator + "')"
}

func parseStatePairs(raw string) pullRequestStateCounts {
	out := pullRequestStateCounts{}
	if raw == "" {
		return out
	}
	for _, pair := range strings.Split(raw, stateCountPairSeparator) {
		state, count, ok := strings.Cut(pair, stateCountFieldSeparator)
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(count, 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		out[state] += n
	}
	return out
}

// ownedPullRequestStateCounts counts the pull requests of each owned
// repository by state.
func ownedPullRequestStateCounts(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, repoKeys []string, bound factTimeBound) (map[string]pullRequestStateCounts, error) {
	out := map[string]pullRequestStateCounts{}
	if len(repoKeys) == 0 {
		return out, nil
	}
	statement := `SELECT repo, ` + statePairsExpression() + `
FROM (
	SELECT toString(p.repo_id) AS repo, ` + pullRequestStateExpression("p", bound) + ` AS st, toInt64(count()) AS c
	FROM git_pull_requests AS p FINAL
	WHERE p.org_id = {org_id:String} AND p.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({ids:Array(String)}) AS id))` + bound.neutral().ExistencePredicate("p.created_at") + `
	GROUP BY repo, st
)
GROUP BY repo
ORDER BY repo`
	err := forEachRepoKeyChunk(repoKeys, func(chunk []string) error {
		return readers.QueryOrgScopedNamed(ctx, client, "ReadTeamPullRequestStates", statement, orgID, chunk, func(row contextpacket.ClickHouseRowScanner) error {
			var repoID, pairs string
			if err := row.Scan(&repoID, &pairs); err != nil {
				return err
			}
			out[repoID] = parseStatePairs(pairs)
			return nil
		}, bound.neutral().Bindings()...)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// teamLinkedPullRequestStateCounts counts, per (team, project-reached
// repository), the distinct pull requests linked to an issue of a project the
// team owns, by state. The key is team id + blockerPairSeparator + repository
// id, the same key teamLinkedPullRequestCounts uses.
func teamLinkedPullRequestStateCounts(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string, reachedOnly map[string][]teamReachedRepo, bound factTimeBound) (map[string]pullRequestStateCounts, error) {
	counts := map[string]pullRequestStateCounts{}
	if reachedRepositoryCount(reachedOnly) == 0 {
		return counts, nil
	}
	err := pagedOrgScopedTeamRepoRead(ctx, client, "ReadTeamProjectLinkedPullRequestStates", orgID, teamIDs,
		teamRepoCursor{teamExpr: "team_id", repoExpr: "repo", repoType: "String"},
		append(boundReaderBindings(bound), readers.Binding{Name: "reach_repo_ids", Value: reachedRepoKeys(reachedOnly)}),
		func(having string, limit int) string {
			return `SELECT team_id, repo, ` + statePairsExpression() + `
FROM (
	SELECT lr.team_id AS team_id, toString(lr.pr_repo_id) AS repo, ` + pullRequestStateExpression("lp", bound) + ` AS st, toInt64(count()) AS c
	FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + teamProjectLinkRowsSQL("ids", bound) + `) AS lr
	INNER JOIN git_pull_requests AS lp FINAL ON lp.org_id = {org_id:String} AND lp.repo_id = lr.pr_repo_id AND lp.number = lr.pr_number
	WHERE lp.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({reach_repo_ids:Array(String)}) AS id))` + bound.neutral().ExistencePredicate("lp.created_at") + `
	GROUP BY team_id, repo, st
)
GROUP BY team_id, repo
` + having + `
ORDER BY team_id, repo
LIMIT ` + fmt.Sprint(limit)
		},
		func(row contextpacket.ClickHouseRowScanner) (string, string, error) {
			var teamID, repoID, pairs string
			if err := row.Scan(&teamID, &repoID, &pairs); err != nil {
				return "", "", err
			}
			counts[teamID+blockerPairSeparator+repoID] = parseStatePairs(pairs)
			return teamID, repoID, nil
		})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// reachedRepoKeys is the set of project-reached repository ids the linked
// pull request state read is pruned to.
func reachedRepoKeys(reachedOnly map[string][]teamReachedRepo) []string {
	seen := map[string]struct{}{}
	var keys []string
	for _, repos := range reachedOnly {
		for _, repo := range repos {
			if _, ok := seen[repo.key]; !ok {
				seen[repo.key] = struct{}{}
				keys = append(keys, repo.key)
			}
		}
	}
	sort.Strings(keys)
	return keys
}

// withoutState is the number of pull requests whose stored state is empty. The
// per pull request fact serves that as a null state, so the table does not
// invent a category for it: they are counted beside the table instead.
func (c pullRequestStateCounts) withoutState() int64 { return c[""] }

// pullRequestsByStateRows lays one count per (repository, state) row, in
// repository then state order.
func pullRequestsByStateRows(repoID, repoName, reachBasis, linkTier string, counts pullRequestStateCounts) []contextfabric.FactValueRow {
	states := make([]string, 0, len(counts))
	for state := range counts {
		if state == "" {
			continue
		}
		states = append(states, state)
	}
	sort.Strings(states)
	rows := make([]contextfabric.FactValueRow, 0, len(states))
	for _, state := range states {
		cells := map[string]contextfabric.FactValue{
			"repository_id":      contextfabric.StringFactValue(repoID),
			"state":              contextfabric.StringFactValue(state),
			"pull_request_count": contextfabric.IntegerFactValue(counts[state]),
			"reach_basis":        contextfabric.StringFactValue(reachBasis),
		}
		if repoName != "" {
			cells["repository_name"] = contextfabric.StringFactValue(repoName)
		}
		if linkTier != "" {
			cells["link_tier"] = contextfabric.StringFactValue(linkTier)
		}
		rows = append(rows, contextfabric.FactValueRow{Fields: cells})
	}
	return rows
}

func (c pullRequestStateCounts) add(other pullRequestStateCounts) {
	for state, n := range other {
		c[state] += n
	}
}

// pullRequestsByStateTotalRows lays one row per state over everything the
// team reaches. A multi-team read serves this grain: the per-repository
// tables are omitted there, the counts are not.
func pullRequestsByStateTotalRows(counts pullRequestStateCounts) []contextfabric.FactValueRow {
	states := make([]string, 0, len(counts))
	for state := range counts {
		if state == "" {
			continue
		}
		states = append(states, state)
	}
	sort.Strings(states)
	rows := make([]contextfabric.FactValueRow, 0, len(states))
	for _, state := range states {
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"state":              contextfabric.StringFactValue(state),
			"pull_request_count": contextfabric.IntegerFactValue(counts[state]),
			"reach_basis":        contextfabric.StringFactValue(repositoryReachTeamTotal),
		}})
	}
	return rows
}

const repositoryReachTeamTotal = "team_total"

func pullRequestsByStateFactValue(rows []contextfabric.FactValueRow, grain contextfabric.TemporalGrain, perRepository bool) (value contextfabric.FactValue, omitted int, ok bool) {
	if len(rows) == 0 {
		return contextfabric.FactValue{}, 0, false
	}
	rows, omitted = capFactValueRows(rows)
	key := []string{"repository_id", "state"}
	if !perRepository {
		key = []string{"state"}
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          key,
		Measures:     []string{"pull_request_count"},
		Observations: []string{"repository_name", "reach_basis", "link_tier"},
		Grain:        grain,
		Rows:         rows,
	}), omitted, true
}
