package devhealthfacts

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-go/readers"
)

// legacySplitCore is the split as it stood before the latest-row tuple and
// the requested-repository unit filter: the differential oracle's old side.
func legacySplitCore(memberships []string, unitCols, unitAgg string, spanSentinel bool, scope membershipScope, perRef bool) string {
	refCol, refGroup := "", ""
	if perRef {
		refCol = "\n\t\t\t\tparsed.pr_number AS pr_number,"
		refGroup = ", pr_number"
	}
	wins := "arrayFilter(w -> w >= 0, [" + strings.Join(memberships, ", ") + "])"
	if spanSentinel {
		wins = "arrayConcat([" + spanSentinelWindow + "], " + wins + ")"
	}
	return `			SELECT parsed.win AS win, parsed.work_unit_id AS work_unit_id,
				if(parsed.uuid_direct != '', parsed.uuid_direct, ifNull(rl.repo_uuid, '')) AS repo_uuid,` + refCol + `
				uniqExact(if(repo_uuid = '', parsed.ref_text, concat(repo_uuid, '#', parsed.pr_number))) AS c,
				any(parsed.effort_value) AS effort_value,
				any(parsed.theme_distribution_json) AS theme_distribution_json,
				any(parsed.bugfix_share) AS bugfix_share,
				any(parsed.span_from) AS span_from` + unitAgg + `
			FROM (
				SELECT work_unit_id, effort_value, theme_distribution_json, bugfix_share, span_from, win,` + unitCols + `
					ref.1 AS uuid_direct, ref.2 AS lookup_provider, ref.3 AS lookup_repo, ref.4 AS pr_number, ref.5 AS ref_text
				FROM (
					SELECT work_unit_id, repo_id, effort_value, theme_distribution_json, from_ts AS span_from,` + unitCols + `
						ifNull(subcategory_distribution_json['` + readers.BugfixSubcategoryKey + `'], 0.0) AS bugfix_share,
						` + wins + ` AS wins,
						arrayConcat(
							arrayMap(r -> (splitByString('#pr', r)[1], '', '', splitByString('#pr', r)[2], r),
								arrayFilter(r -> match(r, '^[0-9a-fA-F-]{36}#pr[0-9]+$'), JSONExtract(structural_evidence_json, 'prs', 'Array(String)'))),
							arrayMap(r -> ('', 'github', substring(splitByChar('#', r)[1], 6), splitByChar('#', r)[2], r),
								arrayFilter(r -> match(r, '^ghpr:[^#]+#[0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)'))),
							arrayMap(r -> ('', 'gitlab', substring(splitByChar('!', r)[1], 8), splitByChar('!', r)[2], r),
								arrayFilter(r -> match(r, '^gitlab:[^!]+![0-9]+$'), JSONExtract(structural_evidence_json, 'issues', 'Array(String)')))
						) AS pr_refs,
						` + repoRefsExpression + ` AS refs
					FROM (
						SELECT work_unit_id,
							(argMax(tuple(repo_id), computed_at)).1 AS repo_id,
							argMax(from_ts, computed_at) AS from_ts,
							argMax(to_ts, computed_at) AS to_ts,
							argMax(effort_value, computed_at) AS effort_value,
							argMax(theme_distribution_json, computed_at) AS theme_distribution_json,
							argMax(subcategory_distribution_json, computed_at) AS subcategory_distribution_json,
							argMax(structural_evidence_json, computed_at) AS structural_evidence_json
						FROM work_unit_investments
						WHERE org_id = {org_id:String}` + supersededWorkUnitIDsFilter() + scope.filter() + `
						GROUP BY work_unit_id
					)
				)
				ARRAY JOIN wins AS win
				ARRAY JOIN refs AS ref
			) AS parsed
			LEFT JOIN (
				SELECT toString(id) AS repo_uuid, argMax(repo, last_synced) AS repo,
					if(uniqExact(provider) = 1, argMax(provider, last_synced), '') AS provider
				FROM repos
				WHERE org_id = {org_id:String}
				GROUP BY id
			) AS rl ON rl.provider = parsed.lookup_provider AND rl.repo = parsed.lookup_repo
			GROUP BY win, work_unit_id, repo_uuid` + refGroup + `
`
}

// SplitStatementPair is one production statement built on the split, as it
// is now (New) and as it was (Old), with the bindings both take besides
// org_id and ids.
type SplitStatementPair struct {
	Name     string
	Old, New string
	Bindings []readers.Binding
}

func swapCore(name, statement, current, legacy string) string {
	if strings.Count(statement, current) != 1 {
		panic(fmt.Sprintf("%s: the statement does not embed its split exactly once", name))
	}
	return strings.Replace(statement, current, legacy, 1)
}

const unitsCore = ",\n\t\t\t\tany(parsed.from_ts) AS from_ts, any(parsed.to_ts) AS to_ts,\n\t\t\t\tgroupUniqArray(parsed.pr_number) AS prs,\n\t\t\t\tgroupUniqArray(parsed.ref_text) AS ref_texts"

func orgRefs(core string) string {
	return strings.Replace(core, repoRefsExpression+" AS refs",
		"if(empty("+repoRefsExpression+"), "+orgNoRefUnit+", "+repoRefsExpression+") AS refs", 1)
}

// InvestmentSplitStatementPairs returns every statement built on the split for
// the range [start, end) and its prior window of equal length, under the scope
// subqueries.
func InvestmentSplitStatementPairs(start, end time.Time, unitLimit int) []SplitStatementPair {
	current := factTimeBound{active: true, hasStart: true, start: start, end: end}
	prior := factTimeBound{active: true, hasStart: true, start: start.Add(-end.Sub(start)), end: start}
	scope := subqueryMembershipScope
	window := func(i int, b factTimeBound) string {
		return fmt.Sprintf("if(%s, %d, -1)", mixWindowPredicate(i, b), i)
	}
	var mixBindings []readers.Binding
	for i, b := range []factTimeBound{current, prior} {
		startParam, endParam := mixWindowParams(i)
		mixBindings = append(mixBindings, readers.Binding{Name: startParam, Value: b.start}, readers.Binding{Name: endParam, Value: b.end})
	}
	unitBindings := append(boundReaderBindings(current), readers.Binding{Name: unitLimitParam, Value: strconv.Itoa(unitLimit)})
	two := []string{window(0, current), window(1, prior)}
	one := []string{window(0, current)}
	orgOne := []string{fmt.Sprintf("if(%s, 0, -1)", mixWindowPredicate(0, current))}

	repoMix := repoMixStatementScoped([]factTimeBound{current, prior}, scope, 100000)
	repoUnits := investmentUnitsStatementFor(current, false, scope, false)
	orgUnits := investmentUnitsStatementFor(current, false, scope, true)
	orgMix := orgMixStatement(current)
	linked := linkedPullRequestMixStatement(current, scope, 1000)
	return []SplitStatementPair{
		{Name: "repository mix", New: repoMix, Bindings: mixBindings,
			Old: swapCore("repository mix", repoMix, repoSplitCoreRequested(two, "", "", true, scope), legacySplitCore(two, "", "", true, scope, false))},
		{Name: "repository unit listing", New: repoUnits, Bindings: unitBindings,
			Old: swapCore("repository unit listing", repoUnits, repoSplitCoreRequested(one, " from_ts, to_ts,", unitsCore, false, scope), legacySplitCore(one, " from_ts, to_ts,", unitsCore, false, scope, false))},
		{Name: "organization unit listing", New: orgUnits, Bindings: unitBindings,
			Old: swapCore("organization unit listing", orgUnits, orgRefs(repoSplitCoreScoped(one, " from_ts, to_ts,", unitsCore, false, scope)), orgRefs(legacySplitCore(one, " from_ts, to_ts,", unitsCore, false, scope, false)))},
		{Name: "organization mix", New: orgMix, Bindings: boundReaderBindings(current),
			Old: swapCore("organization mix", orgMix, orgSplitCore(orgOne), orgRefs(legacySplitCore(orgOne, "", "", true, subqueryMembershipScope, false)))},
		{Name: "team linked pull request mix", New: linked, Bindings: boundReaderBindings(current),
			Old: swapCore("team linked pull request mix", linked, repoSplitCoreGrouped(orgOne, "", "", false, scope, true), legacySplitCore(orgOne, "", "", false, scope, true))},
	}
}

// RequestedUnitFilterDefects are the requested-repository unit filter with
// one clause removed; the differential must tell each apart from the old
// statement.
func RequestedUnitFilterDefects() map[string][2]string {
	return map[string][2]string{
		"direct and fallback references dropped": {"has({ids:Array(String)}, r.1)", "0"},
		"lookup references dropped":              {"(r.2, r.3))), refs)", "(r.2, r.3)) AND 0), refs)"},
	}
}
