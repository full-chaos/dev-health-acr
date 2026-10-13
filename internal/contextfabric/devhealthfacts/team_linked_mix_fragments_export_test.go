package devhealthfacts

import (
	"fmt"
	"strings"

	"github.com/full-chaos/dev-health-go/readers"
)

// LinkedMixFragment is one measured part of the team linked pull request mix
// statement: SQL takes org_id, ids and Bindings.
type LinkedMixFragment struct {
	Name     string
	SQL      string
	Bindings []readers.Binding
}

// TeamLinkedMixFragmentsForTest renders the team linked pull request mix as
// the API sends it for a current read (no bound) under the membership scope
// of the given unit ids, then each fragment of it on its own, cut from the
// same rendered text. missing names the first fragment whose cut marker is
// not in the text.
func TeamLinkedMixFragmentsForTest(scopeIDs []string) (fragments []LinkedMixFragment, missing string) {
	bound := factTimeBound{}
	scope := membershipScope{mode: membershipScopeIDs, ids: scopeIDs}
	bindings := append(boundReaderBindings(bound), scope.bindings()...)
	memberships := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, bound))}
	linked := linkedPullRequestsOf(bound)
	split := linkedPullRequestSplitRows(memberships, scope, linked)

	cut := func(s, from, to string, includeTo bool) (string, bool) {
		i := strings.Index(s, from)
		if i < 0 {
			return "", false
		}
		j := strings.Index(s[i:], to)
		if j < 0 {
			return "", false
		}
		end := i + j
		if includeTo {
			end += len(to)
		}
		return s[i:end], true
	}
	add := func(name, sql string) {
		fragments = append(fragments, LinkedMixFragment{Name: name, SQL: sql, Bindings: bindings})
	}
	add("statement", linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1))
	add("legacy_statement", legacyLinkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1))
	add("linked_scalar", "SELECT length("+linked+")")
	add("link_rows", "SELECT count() FROM "+teamProjectLinkRowsSQL("ids", bound))
	add("unit_scan", "SELECT count(), sum(length(structural_evidence_json)) FROM work_unit_investments WHERE org_id = {org_id:String}"+supersededWorkUnitIDsFilter()+scope.filter())
	reachScan, ok := cut(split, "SELECT work_unit_id, computed_at, repo_id", "\n\t\t\t\t\t)\n\t\t\t\t\tGROUP BY work_unit_id", false)
	if !ok {
		return nil, "reach_scan"
	}
	add("reach_scan", "SELECT count(), countIf(reaches), sum(length(pr_refs)) FROM ("+reachScan+")")
	latest, ok := cut(split, "SELECT work_unit_id,\n\t\t\t\t\t\targMaxIf", "HAVING argMax(reaches, computed_at)", true)
	if !ok {
		return nil, "unit_latest_group"
	}
	add("unit_latest_group", "SELECT count() FROM ("+latest+")")
	expandAt := strings.Index(split, "\nARRAY JOIN repo_prs AS rp")
	foldAt := strings.Index(split, "\nFROM (\n")
	if expandAt < 0 || foldAt < 0 || foldAt > expandAt {
		return nil, "fold_group"
	}
	fold := strings.TrimSpace(split[foldAt+len("\nFROM (\n") : expandAt])
	fold = strings.TrimSuffix(fold, ")")
	add("fold_group", "SELECT count(), sum(length(pairs)), sum(length(repo_prs)) FROM ("+fold+")")
	add("split_rows", "SELECT count() FROM ("+split+")")
	add("owned_repositories", "SELECT count() FROM "+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound)))
	return fragments, ""
}
