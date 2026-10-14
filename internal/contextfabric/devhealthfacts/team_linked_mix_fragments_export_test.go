package devhealthfacts

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

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
//   - statement: the production statement.
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
	linked := linkedPullRequestsOf(bound)
	statement := linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1)
	add := func(name, sql string) {
		fragments = append(fragments, LinkedMixFragment{Name: name, SQL: sql, Bindings: bindings})
	}
	add("statement", statement)
	add("legacy_statement", legacyLinkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1))

	const countsNow = "2 AS c, n,"
	const lengthInFold = ",\n\t\tlength(pairs) AS n\n\tFROM ("
	if strings.Count(statement, countsNow) != 1 || strings.Count(statement, lengthInFold) != 1 {
		return nil, "reference_array_above_expansion"
	}
	above := strings.Replace(statement, countsNow, "countEqual(pairs, (rp.1, concat(rp.1, '#', rp.2))) AS c, length(pairs) AS n,", 1)
	above = strings.Replace(above, lengthInFold, "\n\tFROM (", 1)
	add("reference_array_above_expansion", above)
	add("repeats_counted", strings.Replace(statement, countsNow, "3 AS c, n,", 1))
	add("split_rows", "SELECT count() FROM ("+linkedPullRequestSplitRows(memberships, scope, linked)+")")
	add("linked_set", "SELECT length("+linked+")")
	return fragments, ""
}
