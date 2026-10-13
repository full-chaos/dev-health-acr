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
	// Settings are extra statement settings ("name = value, ...").
	Settings string
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
	m := `SELECT repo_uuid, pr_number, c / n * effort_value AS effort, theme_distribution_json, bugfix_share
	FROM (
` + split + `	)`
	lr := `(SELECT l.1 AS pr_repo, l.2 AS pr_num, l.3 AS team_id FROM (SELECT arrayJoin(` + linked + `) AS l))`
	owned := ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}` + ownershipValidityPredicate(bound))
	add("m_rows", "SELECT count(), sum(length(mapKeys(theme_distribution_json))) FROM ("+m+")")
	add("m_join_lr", "SELECT count() FROM ("+m+") AS m INNER JOIN "+lr+" AS lr ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num")
	statement := linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1)
	notInAt := strings.Index(statement, "\nWHERE (lr.team_id, m.repo_uuid) NOT IN (")
	groupAt := strings.LastIndex(statement, "\nGROUP BY lr.team_id")
	if notInAt < 0 || groupAt < notInAt {
		return nil, "statement_no_not_in"
	}
	add("statement_no_not_in", statement[:notInAt]+statement[groupAt:])
	add("m_aggregated_no_join", `SELECT uniqExact(repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * effort), theme_distribution_json)) AS theme_effort,
	sum(bugfix_share * effort) AS bugfix_effort
FROM (`+m+`)`)
	add("m_join_lr_payload", "SELECT count(), sum(length(mapKeys(m.theme_distribution_json))), sum(m.effort), sum(m.bugfix_share) FROM ("+m+") AS m INNER JOIN "+lr+" AS lr ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num")
	add("m_not_in_owned", "SELECT count(), sum(length(mapKeys(theme_distribution_json))), sum(effort) FROM ("+m+") AS m WHERE ({ids:Array(String)}[1], m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM "+owned+")")
	add("lr_rows", "SELECT count() FROM "+lr)
	// Candidates that never carry the unit's reference array (pairs) to the
	// rows of the expansion. A linked pull request rp comes from a reference
	// row of the unit, so (rp.1, rp.1#rp.2) is in the unit's distinct pairs
	// exactly once: c is 1.
	cOld := "countEqual(pairs, (rp.1, concat(rp.1, '#', rp.2))) AS c, length(pairs) AS n,"
	pairsOld := "groupUniqArray((ref_repo, if(ref_repo = '', ref_text, concat(ref_repo, '#', pr_number)))) AS pairs\n\tFROM ("
	expandOld := "ARRAY JOIN repo_prs AS rp\nARRAY JOIN wins AS win\nWHERE rp.1 != '' AND rp IN ("
	if strings.Count(statement, cOld) != 1 || strings.Count(statement, pairsOld) != 1 || strings.Count(statement, expandOld) != 1 {
		return nil, "statement_unit_counts"
	}
	lengthInFold := strings.Replace(statement, cOld, "1 AS c, n,", 1)
	lengthInFold = strings.Replace(lengthInFold, pairsOld, strings.Replace(pairsOld, "AS pairs\n", "AS pairs,\n\t\tlength(pairs) AS n\n", 1), 1)
	add("statement_n_length_in_fold", lengthInFold)
	uniqInFold := strings.Replace(statement, cOld, "1 AS c, n,", 1)
	uniqInFold = strings.Replace(uniqInFold, pairsOld, "uniqExact((ref_repo, if(ref_repo = '', ref_text, concat(ref_repo, '#', pr_number)))) AS n\n\tFROM (", 1)
	add("statement_n_uniq_in_fold", uniqInFold)
	filtered := strings.Replace(uniqInFold, expandOld, "ARRAY JOIN arrayFilter(x -> x.1 != '' AND x IN (SELECT (l.1, l.2) FROM (SELECT arrayJoin("+linked+") AS l)), repo_prs) AS rp\nARRAY JOIN wins AS win\nWHERE rp.1 != '' AND rp IN (", 1)
	add("statement_n_uniq_filtered_first", filtered)
	add("m_effort_only", "SELECT sum(effort) FROM ("+m+")")
	add("m_bugfix_only", "SELECT sum(bugfix_share) FROM ("+m+")")
	add("m_theme_only", "SELECT sum(length(mapKeys(theme_distribution_json))) FROM ("+m+")")
	fragments = append(fragments, LinkedMixFragment{Name: "statement_one_thread", SQL: statement, Bindings: bindings, Settings: "max_threads = 1"})
	add("owned_repositories", "SELECT count() FROM "+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound)))
	return fragments, ""
}
