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
	add("m_per_pr", `SELECT count() FROM (SELECT repo_uuid, pr_number,
	sumMap(mapApply((k, v) -> (k, v * effort), theme_distribution_json)) AS theme_effort,
	sum(bugfix_share * effort) AS bugfix_effort
FROM (`+m+`)
GROUP BY repo_uuid, pr_number)`)
	add("candidate_per_pr_first", withRowLimitOf(`SELECT lr.team_id AS team_id,
	uniqExact(m.repo_uuid) AS repos,
	sumMap(m.theme_effort) AS theme_effort,
	sum(m.bugfix_effort) AS bugfix_effort
FROM (
	SELECT repo_uuid, pr_number,
		sumMap(mapApply((k, v) -> (k, v * effort), theme_distribution_json)) AS theme_effort,
		sum(bugfix_share * effort) AS bugfix_effort
	FROM (
`+m+`
	)
	GROUP BY repo_uuid, pr_number
) AS m
INNER JOIN `+lr+` AS lr
	ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num
WHERE (lr.team_id, m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM `+owned+`)
GROUP BY lr.team_id
ORDER BY lr.team_id`, linkedMixTeamChunk+1))
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
	add("m_join_lr_grouped", `SELECT lr.team_id AS team_id, uniqExact(m.repo_uuid) AS repos,
	sumMap(mapApply((k, v) -> (k, v * m.effort), m.theme_distribution_json)) AS theme_effort,
	sum(m.bugfix_share * m.effort) AS bugfix_effort
FROM (`+m+`) AS m INNER JOIN `+lr+` AS lr ON m.repo_uuid = lr.pr_repo AND m.pr_number = lr.pr_num
GROUP BY lr.team_id`)
	add("m_not_in_owned", "SELECT count(), sum(length(mapKeys(theme_distribution_json))), sum(effort) FROM ("+m+") AS m WHERE ({ids:Array(String)}[1], m.repo_uuid) NOT IN (SELECT team_id, repo_key FROM "+owned+")")
	add("lr_rows", "SELECT count() FROM "+lr)
	// The same statement with the linked set tested as IN sets, so the set is
	// never a literal array inside an expression of the main pipeline.
	directOld := "hasAny(direct_refs, arrayMap(x -> (x.1, x.2), " + linked + "))"
	directNew := "arrayExists(r -> r IN (SELECT (l.1, l.2) FROM (SELECT arrayJoin(" + linked + ") AS l)), direct_refs)"
	lookupOldHead := "hasAny(lookup_refs, (\n\t\t\t\t\t\tSELECT groupUniqArray((rl.provider, rl.repo, lx.lp_pr)) FROM ("
	lookupNewHead := "arrayExists(r -> r IN (\n\t\t\t\t\t\tSELECT rl.provider, rl.repo, lx.lp_pr FROM ("
	lookupOldTail := "AS rl ON rl.repo_uuid = lx.lp_repo)))"
	lookupNewTail := "AS rl ON rl.repo_uuid = lx.lp_repo), lookup_refs))"
	if strings.Count(statement, directOld) != 1 || strings.Count(statement, lookupOldHead) != 1 || strings.Count(statement, lookupOldTail) != 1 {
		return nil, "statement_sets"
	}
	sets := strings.Replace(statement, directOld, directNew, 1)
	sets = strings.Replace(sets, lookupOldHead, lookupNewHead, 1)
	sets = strings.Replace(sets, lookupOldTail, lookupNewTail, 1)
	add("statement_sets", sets)
	fragments = append(fragments, LinkedMixFragment{Name: "statement_one_thread", SQL: statement, Bindings: bindings, Settings: "max_threads = 1"})
	fragments = append(fragments, LinkedMixFragment{Name: "statement_no_join_swap", SQL: linkedPullRequestMixStatement(bound, scope, linkedMixTeamChunk+1), Bindings: bindings, Settings: "query_plan_join_swap_table = 0"})
	add("owned_repositories", "SELECT count() FROM "+ownedRepositoriesSource(` AND team_id IN {ids:Array(String)}`+ownershipValidityPredicate(bound)))
	return fragments, ""
}
