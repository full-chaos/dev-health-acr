package devhealthfacts

import (
	"context"
	"sort"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// A project reaches its issues directly (work_items.project_id, resolved to
// exactly one project) and reaches pull requests only through those issues'
// linked pull request rows (work_graph_issue_pr). The project rollups of the
// pull request and blocker kinds count over that tree and nothing else: never
// an issue's own repository column, never a team's repositories.
//
// Every work_items read below is narrowed to the ids a link row or a blocker
// edge names, or to the requested projects' work items, before it is joined,
// so a statement's cost follows the links, edges and projects it serves, not
// the size of the organization's work item table.

// projectRollupBasis is the rollup_basis value of a project rollup fact.
const projectRollupBasis = "project_issues"

// projectJoinKeysSQL is the requested projects' work item join keys: a project
// key that resolves to more than one project is omitted, never guessed. ids
// holds "<provider>:<id>" keys (v2Index of identity.KindProject).
const projectJoinKeysSQL = `(SELECT provider, join_key, concat(provider, ':', id) AS project_key FROM ` + resolvedProjectsSQL + `
  WHERE key_resolution_count = 1 AND concat(provider, ':', id) IN {ids:Array(String)})`

// projectRollupWorkItemIDsSQL is the ids of the requested projects' work
// items (project_id, in any stored version, is a requested project's join
// key) that carry a link row: a superset of the issues
// projectLinkedPullRequestsSQL keeps. It is one scalar subquery, which
// ClickHouse runs once per statement however often it is referenced, so the
// statement reads the organization's link ids and the project's work items
// once and every other read follows that small set.
const projectRollupWorkItemIDsSQL = `(SELECT arrayJoin((SELECT groupUniqArray(work_item_id) FROM work_items WHERE org_id = {org_id:String} AND (provider, project_id) IN (SELECT provider, join_key FROM ` + projectJoinKeysSQL + `) AND work_item_id IN ` + linkedWorkItemIDsSQL + `)))`

// projectLinkedPullRequestsSQL is one row per (project, linked pull request)
// with the strongest known link tier among the project's issues that link it.
var projectLinkedPullRequestsSQL = `(
  SELECT pi.project_key AS project_key, l.pr_repo_id AS pr_repo_id, l.pr_number AS pr_number, max(l.link_rank) AS link_rank
  FROM ` + admittedIssueLinksAmongSQL(projectRollupWorkItemIDsSQL) + ` AS l
  INNER JOIN (
    SELECT DISTINCT w.work_item_id AS work_item_id, k.project_key AS project_key
    FROM work_items AS w FINAL
    INNER JOIN ` + projectJoinKeysSQL + ` AS k ON k.provider = w.provider AND k.join_key = w.project_id
    WHERE w.org_id = {org_id:String} AND ` + workItemIsIssueSQL("w") + ` AND w.work_item_id IN ` + projectRollupWorkItemIDsSQL + `
  ) AS pi ON pi.work_item_id = l.work_item_id
  GROUP BY project_key, pr_repo_id, pr_number
)`

// pullRequestOpenStateSQL is the open rule of the pull request fact and the
// status filter: the stored state is exactly open (ops stores it normalized).
// An unknown state is not open.
const pullRequestOpenStateSQL = `ifNull(pr.state, '') = 'open'`

// projectPullRequestRollupRow is one (project, link tier) group.
type projectPullRequestRollupRow struct {
	tier                                 int64
	open, opened, merged, closedUnmerged int64
}

// readProjectRollup serves a project subject: the distinct pull requests
// linked to the project's issues that exist in git_pull_requests, counted
// open now and opened / merged / closed without merging inside the window,
// each pull request once under its strongest link tier. A project with no
// linked pull request row has no fact: absent, never zero.
func (p *PullRequestsProvider) readProjectRollup(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, evidence *contractsv1.ContextFabricRequestedEvidenceWindow) (rejected int, linkUnwindowed []contextfabric.SubjectRef, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return rejected, nil, nil
	}
	window := resolveRollupWindow(timeBound, evidence, clock())
	statement := `SELECT lp.project_key, toInt64(lp.link_rank),
  toInt64(countIf(` + pullRequestOpenStateSQL + `)),
  toInt64(countIf(` + window.timestampExpr("pr.created_at") + `)),
  toInt64(countIf(pr.merged_at IS NOT NULL AND ` + window.timestampExpr("pr.merged_at") + `)),
  toInt64(countIf(pr.merged_at IS NULL AND pr.closed_at IS NOT NULL AND ` + window.timestampExpr("pr.closed_at") + `))
FROM ` + projectLinkedPullRequestsSQL + ` AS lp
INNER JOIN (
  SELECT repo_id, number, state, created_at, merged_at, closed_at FROM git_pull_requests FINAL
  WHERE org_id = {org_id:String} AND (repo_id, number) IN (SELECT repo_id, pr_number FROM work_graph_issue_pr WHERE org_id = {org_id:String} AND work_item_id IN ` + projectRollupWorkItemIDsSQL + `)
) AS pr ON pr.repo_id = lp.pr_repo_id AND pr.number = lp.pr_number
GROUP BY lp.project_key, lp.link_rank
ORDER BY lp.project_key, lp.link_rank`
	byProject := map[string][]projectPullRequestRollupRow{}
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectPullRequestRollup", statement, orgID, ids, func(row readers.RowScanner) error {
		var key string
		var r projectPullRequestRollupRow
		if err := row.Scan(&key, &r.tier, &r.open, &r.opened, &r.merged, &r.closedUnmerged); err != nil {
			return err
		}
		if blockerLinkTierName(r.tier) == "" {
			return nil
		}
		byProject[key] = append(byProject[key], r)
		return nil
	}, window.bindings()...)
	if err != nil {
		return rejected, nil, err
	}
	sort.Strings(ids)
	for _, key := range ids {
		rows := byProject[key]
		if len(rows) == 0 {
			continue
		}
		var open, opened, merged, closedUnmerged int64
		weakest := int64(0)
		tierRows := make([]contextfabric.FactValueRow, 0, len(rows))
		sort.Slice(rows, func(i, j int) bool { return rows[i].tier > rows[j].tier })
		for _, r := range rows {
			open += r.open
			opened += r.opened
			merged += r.merged
			closedUnmerged += r.closedUnmerged
			if weakest == 0 || r.tier < weakest {
				weakest = r.tier
			}
			cells := map[string]contextfabric.FactValue{
				"link_tier":                            contextfabric.StringFactValue(blockerLinkTierName(r.tier)),
				"pull_requests_opened_window":          contextfabric.IntegerFactValue(r.opened),
				"pull_requests_merged_window":          contextfabric.IntegerFactValue(r.merged),
				"pull_requests_closed_unmerged_window": contextfabric.IntegerFactValue(r.closedUnmerged),
			}
			if !timeBound.active {
				cells["pull_requests_open_current"] = contextfabric.IntegerFactValue(r.open)
			}
			tierRows = append(tierRows, contextfabric.FactValueRow{Fields: cells})
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                         contextfabric.StringFactValue(projectRollupBasis),
			"window_basis":                         contextfabric.StringFactValue(window.basis),
			"pull_request_link_basis":              contextfabric.StringFactValue(blockerLinkTierName(weakest)),
			"pull_requests_opened_window":          contextfabric.IntegerFactValue(opened),
			"pull_requests_merged_window":          contextfabric.IntegerFactValue(merged),
			"pull_requests_closed_unmerged_window": contextfabric.IntegerFactValue(closedUnmerged),
		}
		measures := []string{"pull_requests_opened_window", "pull_requests_merged_window", "pull_requests_closed_unmerged_window"}
		// Open is a current state: the stored row cannot say which pull
		// requests were open at a historical read's end, so it is withheld.
		if timeBound.active {
			linkUnwindowed = append(linkUnwindowed, bySubject[key])
		} else {
			fields["pull_requests_open_current"] = contextfabric.IntegerFactValue(open)
			measures = append([]string{"pull_requests_open_current"}, measures...)
		}
		fields["link_tier_breakdown"] = contextfabric.TableFactValue(contextfabric.FactTable{
			Shape:    contextfabric.FactTableBreakdown,
			Key:      []string{"link_tier"},
			Measures: measures,
			Grain:    grainExact,
			Rows:     tierRows,
		})
		if value, ok := window.startValue(); ok {
			fields["window_start"] = value
		}
		if value, ok := window.endValue(); ok {
			fields["window_end"] = value
		}
		provider, id := splitProjectKey(key)
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactPullRequests, Subject: bySubject[key], Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, provider+":"+id)},
		})
	}
	return rejected, linkUnwindowed, nil
}

// splitProjectKey splits a "<provider>:<id>" project key at its first colon.
func splitProjectKey(key string) (provider, id string) {
	for i := 0; i < len(key); i++ {
		if key[i] == ':' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

// projectBlockerEdgeRowsSQL is the distinct (project, blocked issue, blocker)
// rows of the requested projects' issues, with whether the blocker is open.
// Both work_items reads are narrowed to the blocker edge endpoints; both ends
// pass the work-item authorization rule.
func projectBlockerEdgeRowsSQL(authorized, current string, withStart bool) string {
	startOutput, startJoin := blockerStartParts(withStart)
	return `SELECT t.project_key AS project_key, ` + blockedExpr("d") + ` AS tgt, ` + blockerExpr("d") + ` AS src, toUInt8(` + blockerOpenPredicate + `) AS is_open, max(toUInt8(` + relationWriterCodeSQL("d") + ` = 4)) AS writer_unresolved` + startOutput + `
  FROM work_item_dependencies AS d FINAL` + startJoin + `
  INNER JOIN (
    SELECT DISTINCT w.work_item_id AS work_item_id, k.project_key AS project_key
    FROM work_items AS w FINAL
    INNER JOIN ` + projectJoinKeysSQL + ` AS k ON k.provider = w.provider AND k.join_key = w.project_id
    WHERE w.org_id = {org_id:String} AND ` + workItemIsIssueSQL("w") + ` AND w.work_item_id IN ` + blockerEndpointIDsSQL + `
  ) AS t ON t.work_item_id = ` + blockedExpr("d") + `
  INNER JOIN ` + blockerWorkItemsSQL("s") + ` ON s.org_id = d.org_id AND s.work_item_id = ` + blockerExpr("d") + `
  WHERE ` + blockerRowsWhereSQL(authorized) + current + `
  GROUP BY project_key, tgt, src, is_open`
}

// readProjectRollup serves a project subject on the current axis: the
// project's issues blocked by an open blocker, the open blocker edges, and the
// edges the blocker's status excluded, over the caller's authorized items. A
// project with no counted edge has no fact: absent, never zero.
func (p *BlockersProvider) readProjectRollup(ctx context.Context, principal storage.Principal, orgID string, requestedScope []string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact) (rejected int, err error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindProject)
	if len(ids) == 0 {
		return rejected, nil
	}
	settings, err := workItemReaderSettings(ctx)
	if err != nil {
		return rejected, err
	}
	authorized, current, authorizationBindings, err := p.blockerScope(ctx, principal, orgID, requestedScope, settings)
	if err != nil {
		return rejected, err
	}
	statement := blockerStatement(`SELECT project_key, toInt64(uniqExactIf(tgt, is_open = 1)), toInt64(uniqExactIf((tgt, src), is_open = 1)), toInt64(uniqExactIf((tgt, src), is_open = 0)), toInt64(uniqExactIf((tgt, src), is_open = 1 AND writer_unresolved = 1))
FROM (`+projectBlockerEdgeRowsSQL(authorized, current, false)+`)
GROUP BY project_key
ORDER BY project_key`, settings)
	type totals struct{ blocked, edges, excluded, unresolved int64 }
	byProject := map[string]totals{}
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectBlockerRollup", statement, orgID, ids, func(row readers.RowScanner) error {
		var key string
		var t totals
		if err := row.Scan(&key, &t.blocked, &t.edges, &t.excluded, &t.unresolved); err != nil {
			return err
		}
		byProject[key] = t
		return nil
	}, authorizationBindings...)
	if err != nil {
		return rejected, err
	}
	ageStatement := blockerStatement(`SELECT project_key, toInt64(uniqExactIf((tgt, src), is_open = 1)), toInt64(uniqExactIf((tgt, src), is_open = 1 AND start_unix IS NOT NULL)), toInt64(uniqExactIf((tgt, src), is_open = 1 AND start_exact = 1)),
  ifNull(toInt64(minIf(start_unix, is_open = 1)), 0)
FROM (`+projectBlockerEdgeRowsSQL(authorized, current, true)+`)
GROUP BY project_key
ORDER BY project_key`, settings)
	ages := map[string]blockerTeamAge{}
	ageUnavailable := false
	ageErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectBlockerOldestAge", ageStatement, orgID, ids, func(row readers.RowScanner) error {
		var key string
		var a blockerTeamAge
		if err := row.Scan(&key, &a.openEdges, &a.edgesWithStart, &a.edgesWithExactStart, &a.oldestStartMillis); err != nil {
			return err
		}
		ages[key] = a
		return nil
	}, authorizationBindings...)
	if ageErr != nil {
		if err := ageReadFailure(ctx, "project", ageErr); err != nil {
			return rejected, err
		}
		ageUnavailable, ages = true, map[string]blockerTeamAge{}
	}
	sort.Strings(ids)
	for _, key := range ids {
		t, ok := byProject[key]
		if !ok {
			continue
		}
		provider, id := splitProjectKey(key)
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                                    contextfabric.StringFactValue(projectRollupBasis),
			"blocker_status_basis":                            contextfabric.StringFactValue(blockerStatusBasisOpen),
			"blocked_work_items_current":                      contextfabric.IntegerFactValue(t.blocked),
			"blocker_dependencies_current":                    contextfabric.IntegerFactValue(t.edges),
			"blocker_dependencies_excluded_by_status_current": contextfabric.IntegerFactValue(t.excluded),
			"blocker_dependencies_writer_unresolved_current":  contextfabric.IntegerFactValue(t.unresolved),
		}
		if days, basis, reason, ok := blockerOldestAgeOrUnavailable(ages[key], clock(), ageUnavailable); ok {
			fields["oldest_blocked_age_days"] = contextfabric.IntegerFactValue(days)
			fields["oldest_blocked_age_basis"] = contextfabric.StringFactValue(basis)
		} else {
			fields["oldest_blocked_age_unavailable_reason"] = contextfabric.StringFactValue(reason)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: bySubject[key], Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, provider+":"+id)},
		})
	}
	return rejected, nil
}
