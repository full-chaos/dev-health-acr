package devhealthfacts

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// DiagLinkRowsVariants names the link-row variants the diagnostic measures.
var DiagLinkRowsVariants = []string{"V0", "V1", "V2", "V3", "VM", "V4", "V5"}

// DiagLinkRowsUsesTable reports whether a variant reads _team_link_candidates.
func DiagLinkRowsUsesTable(variant string) bool {
	return variant == "V0" || variant == "V1" || variant == "V2"
}

const diagCandidateIDsParam = "link_candidate_ids"

func diagLinkRows(variant string) string {
	bound := factTimeBound{}
	statement := teamProjectLinkRowsSQL("ids", bound)
	cwCut := [2]string{"cw.work_item_id IN " + teamLinkCandidatesSent, "1"}
	lCut := [2]string{admittedLinkedPullRequestRowsAmongSQL(teamLinkCandidateHashesSent), admittedLinkedPullRequestRowsSQL}
	list := `(SELECT arrayJoin({` + diagCandidateIDsParam + `:Array(String)}))`
	listHashes := `(SELECT cityHash64(arrayJoin({` + diagCandidateIDsParam + `:Array(String)})))`
	apply := func(cuts ...[2]string) string {
		out := statement
		for _, cut := range cuts {
			if strings.Count(out, cut[0]) != 1 {
				panic("diag: fragment not found: " + cut[0])
			}
			out = strings.Replace(out, cut[0], cut[1], 1)
		}
		return out
	}
	switch variant {
	case "V0":
		return statement
	case "V1":
		return apply(cwCut)
	case "V2":
		return apply(lCut)
	case "V3":
		return apply(cwCut, lCut)
	case "VM":
		mainCandidates := `(SELECT cp.work_item_id FROM work_items AS cp WHERE cp.org_id = {org_id:String} AND cp.work_item_id IN ` + linkedWorkItemIDsSQL + `
      AND cp.project_id IN ` + teamOwnedProjectKeyCandidatesSQL("ids", bound) + `)`
		return apply([2]string{"cw.work_item_id IN " + teamLinkCandidatesSent, "cw.work_item_id IN " + mainCandidates}, lCut)
	case "V4":
		return apply([2]string{"cw.work_item_id IN " + teamLinkCandidatesSent, "cw.work_item_id IN " + list}, lCut)
	case "V5":
		return strings.ReplaceAll(apply([2]string{"cw.work_item_id IN " + teamLinkCandidatesSent, "cw.work_item_id IN " + list}), teamLinkCandidateHashesSent, listHashes)
	}
	panic("diag: unknown variant " + variant)
}

// DiagReaderStatement is one of the three readers' statements over a link-row
// variant, with its bindings (org_id and ids ride with the reader call).
type DiagReaderStatement struct {
	Reader   string
	SQL      string
	Bindings []readers.Binding
}

// DiagReaderStatementsForTest renders the reached-repository, linked pull
// request count and state statements (first page) over the variant's link rows.
func DiagReaderStatementsForTest(variant string, start, end time.Time, reachRepoIDs, candidates []string) []DiagReaderStatement {
	bound := factTimeBound{}
	window := rollupWindow{hasStart: true, hasEnd: true, start: start, end: end, basis: "range"}
	lr := diagLinkRows(variant)
	extra := func(bindings ...readers.Binding) []readers.Binding {
		if variant == "V4" || variant == "V5" {
			bindings = append(bindings, readers.Binding{Name: diagCandidateIDsParam, Value: candidates})
		}
		return bindings
	}
	limit := fmt.Sprint(teamReadPageRows + 1)
	reachIDs := readers.Binding{Name: "reach_repo_ids", Value: reachRepoIDs}
	return []DiagReaderStatement{
		{Reader: "reach", Bindings: extra(boundReaderBindings(bound)...), SQL: `SELECT lr.team_id, toString(lr.pr_repo_id), ifNull(any(r.repo), ''), toInt64(min(lr.link_rank)), toInt64(uniqExact(lr.pr_number))
FROM ` + lr + ` AS lr
LEFT JOIN repos AS r FINAL ON r.id = lr.pr_repo_id AND r.org_id = {org_id:String}
GROUP BY lr.team_id, lr.pr_repo_id
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + limit},
		{Reader: "counts", Bindings: extra(append(append(window.bindings(), boundReaderBindings(bound)...), reachIDs)...), SQL: `SELECT lr.team_id, toString(lr.pr_repo_id),
	toInt64(countIf(` + window.timestampExpr("pr.created_at") + `)),
	toInt64(countIf(pr.merged_at IS NOT NULL AND ` + window.timestampExpr("pr.merged_at") + `)),
	toInt64(countIf(pr.merged_at IS NULL AND pr.closed_at IS NOT NULL AND ` + window.timestampExpr("pr.closed_at") + `))
FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + lr + `) AS lr
INNER JOIN git_pull_requests AS pr FINAL ON pr.org_id = {org_id:String} AND pr.repo_id = lr.pr_repo_id AND pr.number = lr.pr_number
WHERE pr.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({reach_repo_ids:Array(String)}) AS id))
GROUP BY lr.team_id, lr.pr_repo_id
ORDER BY lr.team_id, lr.pr_repo_id
LIMIT ` + limit},
		{Reader: "states", Bindings: extra(append(boundReaderBindings(bound), reachIDs)...), SQL: `SELECT team_id, repo, ` + statePairsExpression() + `
FROM (
	SELECT lr.team_id AS team_id, toString(lr.pr_repo_id) AS repo, ` + pullRequestStateExpression("lp", bound) + ` AS st, toInt64(count()) AS c
	FROM (SELECT DISTINCT team_id, pr_repo_id, pr_number FROM ` + lr + `) AS lr
	INNER JOIN git_pull_requests AS lp FINAL ON lp.org_id = {org_id:String} AND lp.repo_id = lr.pr_repo_id AND lp.number = lr.pr_number
	WHERE lp.repo_id IN (SELECT toUUIDOrZero(id) FROM (SELECT arrayJoin({reach_repo_ids:Array(String)}) AS id))` + bound.neutral().ExistencePredicate("lp.created_at") + `
	GROUP BY team_id, repo, st
)
GROUP BY team_id, repo
ORDER BY team_id, repo
LIMIT ` + limit},
	}
}

// DiagTeamLinkCandidatesForTest reads the teams' link candidates (deduped) with
// the production statement.
func DiagTeamLinkCandidatesForTest(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, teamIDs []string) ([]string, error) {
	var ids []string
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadTeamLinkCandidates", teamLinkCandidatesStatement(factTimeBound{}), orgID, teamIDs, func(row contextpacket.ClickHouseRowScanner) error {
		return row.Scan(&ids)
	})
	return slices.Compact(slices.Sorted(slices.Values(ids))), err
}
