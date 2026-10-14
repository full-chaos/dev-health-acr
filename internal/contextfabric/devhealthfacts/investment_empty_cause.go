package devhealthfacts

import (
	"context"
	"fmt"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// Closed causes of a team that owns repositories and has no mix in the window,
// chosen from two counts of the units persisted on its owned repositories.
const (
	investmentTeamUnitsOutsideRun = "investment_team_units_outside_membership_run"
	investmentTeamUnitsUnattrib   = "investment_team_units_unattributed"
)

const (
	investmentTeamUnitsOutsideRunReason = investmentTeamUnitsOutsideRun + ": the team's owned repositories have persisted work units in the requested window, and none of them is in the latest complete membership run"
	investmentTeamUnitsUnattribReason   = investmentTeamUnitsUnattrib + ": the team's owned repositories have persisted work units in scope, and none of them reached a positive share (a reference that resolves to no repository; the count follows)"
)

// ownedUnitCountStatement counts, per repository, the distinct latest work
// units whose own repository is in {ids} and that overlap the window. It never
// follows references: it names which stage dropped a team's units, it serves
// no mix.
func ownedUnitCountStatement(bound factTimeBound, scope membershipScope) string {
	return `SELECT toString(repo_id) AS repo, uniqExact(work_unit_id) AS units, uniqExactIf(work_unit_id, effort_value > 0) AS positive
FROM (
	SELECT work_unit_id,
		(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
		argMax(from_ts, computed_at) AS from_ts,
		argMax(to_ts, computed_at) AS to_ts,
		argMax(effort_value, computed_at) AS effort_value
	FROM work_unit_investments
	WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + scope.filter() + `
	GROUP BY work_unit_id
)
WHERE repo_id IS NOT NULL AND toString(repo_id) IN {ids:Array(String)} AND ` + mixWindowPredicate(0, bound) + `
GROUP BY repo`
}

type ownedUnitCount struct{ units, positive uint64 }

func (p *InvestmentProvider) ownedUnitCounts(ctx context.Context, orgID string, repoIDs []string, bound factTimeBound, scope membershipScope) (map[string]ownedUnitCount, error) {
	counts := map[string]ownedUnitCount{}
	var extra []readers.Binding
	for _, tb := range bound.bindings() {
		extra = append(extra, readers.Binding{Name: tb.Name, Value: tb.Value})
	}
	extra = append(extra, scope.bindings()...)
	for start := 0; start < len(repoIDs); start += repoMixChunk {
		end := start + repoMixChunk
		if end > len(repoIDs) {
			end = len(repoIDs)
		}
		err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadOwnedUnitCounts", ownedUnitCountStatement(bound, scope), orgID, repoIDs[start:end], func(row contextpacket.ClickHouseRowScanner) error {
			var repo string
			var units, positive uint64
			if err := row.Scan(&repo, &units, &positive); err != nil {
				return err
			}
			c := counts[repo]
			c.units += units
			c.positive += positive
			counts[repo] = c
			return nil
		}, extra...)
		if err != nil {
			return nil, fmt.Errorf("read owned unit counts: %w", err)
		}
	}
	return counts, nil
}

// teamEmptyCauses names why each given team (team id -> owned repository ids)
// has no mix: "" keeps the generic no-unit-in-window reason.
func (p *InvestmentProvider) teamEmptyCauses(ctx context.Context, orgID string, owned map[string][]string, bound factTimeBound) (map[string]emptyCause, error) {
	repoSet := map[string]bool{}
	for _, repos := range owned {
		for _, id := range repos {
			repoSet[id] = true
		}
	}
	if len(repoSet) == 0 {
		return nil, nil
	}
	repoIDs := make([]string, 0, len(repoSet))
	for id := range repoSet {
		repoIDs = append(repoIDs, id)
	}
	sort.Strings(repoIDs)
	everything, err := p.ownedUnitCounts(ctx, orgID, repoIDs, bound, membershipScope{mode: membershipScopeNone})
	if err != nil {
		return nil, err
	}
	scope, err := p.resolveMembershipScope(ctx, orgID)
	if err != nil {
		return nil, err
	}
	scoped, err := p.ownedUnitCounts(ctx, orgID, repoIDs, bound, scope)
	if err != nil {
		return nil, err
	}
	causes := make(map[string]emptyCause, len(owned))
	for teamID, repos := range owned {
		var all, in, positive uint64
		for _, id := range repos {
			all += everything[id].units
			in += scoped[id].units
			positive += scoped[id].positive
		}
		switch {
		case all > in:
			causes[teamID] = emptyCause{token: investmentTeamUnitsOutsideRun, outside: all - in}
		case in > 0 && positive > 0:
			causes[teamID] = emptyCause{token: investmentTeamUnitsUnattrib, positive: positive}
		}
	}
	return causes, nil
}

// emptyCause is a team's named cause with the counts behind it.
type emptyCause struct {
	token    string
	outside  uint64
	positive uint64
}

func (c emptyCause) reason() string {
	switch c.token {
	case investmentTeamUnitsOutsideRun:
		return fmt.Sprintf("%s (%d units outside the run)", investmentTeamUnitsOutsideRunReason, c.outside)
	case investmentTeamUnitsUnattrib:
		return fmt.Sprintf("%s (%d units with positive effort dropped by a reference that resolves to no repository)", investmentTeamUnitsUnattribReason, c.positive)
	}
	return investmentTeamNoUnitReason
}

// summary is the one-line disclosure of a cause in the request note.
func (c emptyCause) summary() string {
	switch c.token {
	case investmentTeamUnitsOutsideRun:
		return "owned repositories have persisted work units in the requested window, and at least one is outside the latest complete membership run"
	case investmentTeamUnitsUnattrib:
		return "owned repositories have persisted work units in scope, and none reached a positive share because a reference resolves to no repository (counts per team in the subject reason)"
	}
	return investmentTeamNoUnitReason
}
