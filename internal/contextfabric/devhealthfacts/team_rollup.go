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
}
