package devhealthfacts

import (
	"context"
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-go/readers"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// inlineLinkedSet is the linked set as the statement read it before it was
// sent as an external table: the link chain inlined.
func inlineLinkedSet(bound factTimeBound) string {
	return `(SELECT groupUniqArray((toString(pr_repo_id), toString(pr_number), team_id)) FROM ` + inlineTeamLinkCandidates(teamProjectLinkRowsSQL("ids", bound), bound) + `)`
}

// inlineSentLinkedMix puts the linked set and the scope ids the production
// statement takes as external tables back into its text, so a fragment runs
// with bindings only. It panics when the statement does not read both the way
// it is built to.
func inlineSentLinkedMix(statement string, bound factTimeBound) string {
	if n := strings.Count(statement, linkedPullRequestsSent); n != 4 {
		panic(fmt.Sprintf("the team linked mix reads its linked set %d times, want 4", n))
	}
	statement = strings.ReplaceAll(statement, linkedPullRequestsSent, inlineLinkedSet(bound))
	sent := membershipScope{mode: membershipScopeTableIDs}.filter()
	return strings.ReplaceAll(statement, sent, membershipScope{mode: membershipScopeIDs}.filter())
}

// TeamLinkedMixStatementsForTest returns the production statement as sent and
// with its external tables inlined (no bound, membership scope by ids).
func TeamLinkedMixStatementsForTest() (sent, inlined string) {
	scope := membershipScope{mode: membershipScopeIDs, ids: []string{"a"}}
	sent = linkedPullRequestMixStatement(factTimeBound{}, scope, linkedMixTeamChunk+1)
	return sent, inlineSentLinkedMix(sent, factTimeBound{})
}

// TeamLinkedMixInlineStatementForTest is the production statement for a
// current read with its external tables inlined: under the membership scope by
// ids (bound as scope_json) or, with subquery set, under the scope subqueries.
func TeamLinkedMixInlineStatementForTest(subquery bool) string {
	scope := membershipScope{mode: membershipScopeIDs, ids: []string{"a"}}
	if subquery {
		scope = subqueryMembershipScope
	}
	return inlineSentLinkedMix(linkedPullRequestMixStatement(factTimeBound{}, scope, linkedMixTeamChunk+1), factTimeBound{})
}

// LinkedMixValues is one team's row of the team linked pull request mix.
type LinkedMixValues struct {
	Repos  uint64
	Theme  map[string]float64
	Bugfix float64
}

// TeamLinkedMixRowsForTest runs the production read (linked set, then the mix
// with its external tables) for a current read and returns every team row.
func TeamLinkedMixRowsForTest(ctx context.Context, provider contextfabric.FactProvider, orgID string, teams []string) (map[string]LinkedMixValues, error) {
	p, ok := provider.(*InvestmentProvider)
	if !ok {
		return nil, fmt.Errorf("provider %T is not the investment provider", provider)
	}
	rows, err := p.readLinkedPullRequestMixRows(ctx, orgID, teams, factTimeBound{})
	if err != nil {
		return nil, err
	}
	out := make(map[string]LinkedMixValues, len(rows))
	for team, r := range rows {
		out[team] = LinkedMixValues{Repos: r.repos, Theme: r.theme, Bugfix: r.bugfix}
	}
	return out, nil
}

// LinkedMixFragment is one measured form of the team linked pull request mix:
// SQL takes org_id, ids and Bindings.
type LinkedMixFragment struct {
	Name     string
	SQL      string
	Bindings []readers.Binding
}

// TeamLinkedMixFragmentsForTest renders the team linked pull request mix as
// the API sends it for a current read (no bound) under the membership scope
// of the given unit ids, beside the forms the prod-shape row compares it
// with. missing names the first form whose marker is not in the rendered
// text.
//
//   - statement: the production statement with its external tables inlined.
//   - legacy_statement: the statement before the folded split.
//   - reference_array_above_expansion: the production statement with c and n
//     read from the unit's reference array above the expansion, as it was
//     before the count moved into the unit fold.
//   - repeats_counted: the production statement with c forced to 2, the
//     value a pull request named twice would get if mentions were counted.
//   - split_rows: the rows of the production split, counted.
//   - linked_set: the linked set on its own.
func TeamLinkedMixFragmentsForTest(scopeIDs []string) (fragments []LinkedMixFragment, missing string) {
	bound := factTimeBound{}
	scope := membershipScope{mode: membershipScopeIDs, ids: scopeIDs}
	bindings := append(boundReaderBindings(bound), scope.bindings()...)
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	linked := inlineLinkedSet(bound)
	statement := inlineSentLinkedMix(linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1), bound)
	add := func(name, sql string) {
		fragments = append(fragments, LinkedMixFragment{Name: name, SQL: sql, Bindings: bindings})
	}
	add("statement", statement)
	add("legacy_statement", legacyLinkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1))

	const countsNow = "1 AS c, n,"
	const lengthInFold = ",\n\t\tlength(pairs) AS n\n\tFROM ("
	if strings.Count(statement, countsNow) != 1 || strings.Count(statement, lengthInFold) != 1 {
		return nil, "reference_array_above_expansion"
	}
	above := strings.Replace(statement, countsNow, "countEqual(pairs, (rp.1, concat(rp.1, '#', rp.2))) AS c, length(pairs) AS n,", 1)
	above = strings.Replace(above, lengthInFold, "\n\tFROM (", 1)
	add("reference_array_above_expansion", above)
	add("repeats_counted", strings.Replace(statement, countsNow, "2 AS c, n,", 1))
	add("split_rows", "SELECT count() FROM ("+linkedPullRequestSplitRows(memberships, scope, linked)+")")
	add("linked_set", "SELECT length("+linked+")")
	return fragments, ""
}
