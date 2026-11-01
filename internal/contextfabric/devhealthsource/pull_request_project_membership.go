package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12308: a pull request's CURRENT project membership, stated on its own
// entity write. A pull request reaches a project only through its issues'
// actual linked rows (the entity tree), so its set is the union of the current
// sets (CHAOS-12226) of the issues linked to it in work_graph_issue_pr, read
// through the same rules as the link producer: a lower-tier row a native row
// covers contributes nothing, and neither does an unknown tier or a
// pull-request-typed work item. Never a repository, an issue-key prefix, team
// ownership, or the pull_request arm of project_membership_presence.
//
// project_membership_tier names the weakest tier among the links that
// contributed, so a lower tier is never presented as native. work_items is
// keyed by (org_id, repo_id, work_item_id) and a link row names no issue
// repository, so one link id can join several rows: the row in the pull
// request's own repository is the issue, and with none the link is ambiguous
// and names no project (resolveLinkedIssues). When no linked
// issue contributes, the pull request states nothing (unstated, not empty):
// it stays on the disclosed path and is never read as "in no project". A set
// over the contract's bound is unstated too: a partial set is never stated.
//
// Both reads share WithWorkItemProjectMembership: on together, off together,
// and the backfill is the same from-zero rebuild, a roll step chris approves.

// pullRequestMembershipLinkMarker names the link read of the membership.
const pullRequestMembershipLinkMarker = "AS membership_link_repo_id"

// pullRequestMembershipRefreshMarker names the refresh read's change column.
const pullRequestMembershipRefreshMarker = "AS pull_request_membership_changed_at"

// pullRequestProjectMembershipTable is the refresh table's registry entry.
const pullRequestProjectMembershipTable = "pull_requests_project_membership"

// pullRequestKey is a pull request by its key in the link table.
type pullRequestKey struct {
	repoID string
	number uint32
}

// pullRequestIssueLink is one contributing link row of a pull request.
type pullRequestIssueLink struct {
	issue workItemKey
	tier  string
}

// queryPullRequestsWithMembership is queryPullRequests with each emitted pull
// request's current project membership stated.
func queryPullRequestsWithMembership(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	items, truncated, err := queryPullRequests(ctx, client, orgID, cursor, limit)
	if err != nil {
		return nil, false, err
	}
	if err := statePullRequestProjectMemberships(ctx, client, orgID, items); err != nil {
		return nil, false, err
	}
	return items, truncated, nil
}

// queryPullRequestMembershipChanges is the refresh table: it re-emits, through
// the one pull request row builder and with the full current set, every pull
// request whose membership inputs changed since its own cursor without the
// pull request itself syncing: a link row of it moved, or a linked issue or
// its membership input moved. A link the ops build DELETES leaves no stamp here;
// the graph side clears that pull request's set when it retracts the edge
// (falkorgraph retractOutrankedLinks).
func queryPullRequestMembershipChanges(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	const rowKey = "concat(toString(p.repo_id), ':', toString(p.number))"
	statement := `SELECT toString(p.repo_id) AS page_repo_id, r.repo, p.number, p.last_synced, c.changed_at ` + pullRequestMembershipRefreshMarker + `
FROM git_pull_requests AS p FINAL
INNER JOIN repos AS r FINAL ON r.id = p.repo_id AND r.org_id = p.org_id
INNER JOIN ` + pullRequestMembershipChangesSubquery(cursor) + ` AS c ON c.repo_id = p.repo_id AND c.pr_number = p.number
WHERE p.org_id = {org_id:String}` + sincePredicate(cursor, "c.changed_at", rowKey) + orderBy("c.changed_at", rowKey)
	keys, stamps, truncated, err := pullRequestMembershipPageKeys(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit)
	if err != nil {
		return nil, false, err
	}
	items, err := readPullRequestRows(ctx, client, orgID, keys, scanPullRequestRow)
	if err != nil {
		return nil, false, err
	}
	for i := range items {
		items[i].cursorAt = stamps[items[i].sortKey]
	}
	if err := statePullRequestProjectMemberships(ctx, client, orgID, items); err != nil {
		return nil, false, err
	}
	return items, truncated, nil
}

// pullRequestMembershipChangesSubquery is one row per pull request with its
// latest membership-input stamp: its own link rows; each linked issue's own
// row, because a project column change moves no membership-change arm (the
// work_items producer re-states the issue itself, but not its pull requests);
// and each linked issue's membership inputs
// (workItemMembershipChangesSubquery). With a cursor, each arm keeps only
// stamps at or after it.
func pullRequestMembershipChangesSubquery(cursor cursorState) string {
	linksSince, issuesSince := "", ""
	if !cursor.Since.IsZero() {
		linksSince = " AND last_synced >= " + sinceBoundSQL
		issuesSince = " AND w.last_synced >= " + sinceBoundSQL
	}
	return `(
  SELECT repo_id, pr_number, max(stamp) AS changed_at
  FROM (
    SELECT repo_id, pr_number, last_synced AS stamp
    FROM work_graph_issue_pr FINAL
    WHERE org_id = {org_id:String}` + linksSince + `
    UNION ALL
    SELECT l.repo_id AS repo_id, l.pr_number AS pr_number, w.last_synced AS stamp
    FROM work_graph_issue_pr AS l FINAL
    INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
    WHERE l.org_id = {org_id:String}` + issuesSince + `
    UNION ALL
    SELECT l.repo_id AS repo_id, l.pr_number AS pr_number, m.` + strings.TrimPrefix(workItemMembershipRefreshMarker, "AS ") + ` AS stamp
    FROM work_graph_issue_pr AS l FINAL
    INNER JOIN ` + workItemMembershipChangesSubquery(cursor) + ` AS m ON m.subject_id = l.work_item_id
    WHERE l.org_id = {org_id:String}
  )
  GROUP BY repo_id, pr_number
)`
}

// pullRequestMembershipPageKeys is pullRequestPageKeys with each row's change
// stamp, the row's place in the shared cursor (candidate.cursorAt).
func pullRequestMembershipPageKeys(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding, limit int) ([]pullRequestPageKey, map[string]time.Time, bool, error) {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return nil, nil, false, err
	}
	defer rows.Close()
	var keys []pullRequestPageKey
	stamps := map[string]time.Time{}
	for rows.Next() {
		var key pullRequestPageKey
		var stamp time.Time
		if err := rows.Scan(&key.repoID, &key.repoSlug, &key.number, &key.version, &stamp); err != nil {
			return nil, nil, false, err
		}
		keys = append(keys, key)
		stamps[fmt.Sprintf("%s:%d", key.repoID, key.number)] = stamp.UTC()
	}
	if err := rows.Err(); err != nil {
		return nil, nil, false, err
	}
	truncated := len(keys) > limit
	if truncated {
		keys = keys[:limit]
	}
	return keys, stamps, truncated, nil
}

// statePullRequestProjectMemberships sets ProjectIdentities and the tier
// property on every pull request entity among items whose linked issues state
// a project. A failed read returns the error, so the page fails and nothing is
// written: a partial set is never stated as current.
func statePullRequestProjectMemberships(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, items []candidate) error {
	var keys []pullRequestKey
	targets := map[pullRequestKey][]*contractsv1.ContextFabricEntityProjection{}
	for i := range items {
		entity := items[i].entity
		if entity == nil || entity.Subject.Kind != contractsv1.ContextFabricSubjectPullRequest {
			continue
		}
		key, ok := pullRequestKeyOf(entity.Subject.CanonicalID)
		if !ok {
			return fmt.Errorf("devhealthsource: pull request canonical id does not parse")
		}
		if _, seen := targets[key]; !seen {
			keys = append(keys, key)
		}
		targets[key] = append(targets[key], entity)
	}
	links, ambiguous, err := readPullRequestIssueLinks(ctx, client, orgID, keys)
	if err != nil {
		return err
	}
	var issues []workItemKey
	seen := map[workItemKey]bool{}
	for _, key := range keys {
		for _, link := range links[key] {
			if !seen[link.issue] {
				seen[link.issue] = true
				issues = append(issues, link.issue)
			}
		}
	}
	sets, _, err := readWorkItemProjectMemberships(ctx, client, orgID, issues)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	statedByTier := map[string]int{}
	overBound := 0
	for key, entities := range targets {
		set, tier := pullRequestMembershipOf(links[key], sets)
		if len(set) == 0 {
			continue
		}
		if len(set) > contractsv1.ContextFabricEntityProjectIdentitiesMax {
			overBound++
			slog.WarnContext(ctx, "context fabric: pull request project membership over the contract bound, left unstated", "identities", len(set), "bound", contractsv1.ContextFabricEntityProjectIdentitiesMax)
			continue
		}
		statedByTier[tier]++
		for _, entity := range entities {
			stated := append([]string{}, set...)
			entity.ProjectIdentities = &stated
			setStringProperty(entity.Properties, contractsv1.ContextFabricProjectMembershipTierProperty, tier, 0)
		}
	}
	slog.InfoContext(ctx, "context fabric: pull request project membership stated",
		"pull_requests", len(keys), "stated_native", statedByTier["native"], "stated_explicit_text", statedByTier["explicit_text"],
		"stated_heuristic", statedByTier["heuristic"], "over_bound", overBound, "ambiguous_links", ambiguous)
	return nil
}

// pullRequestMembershipOf is the union of the sets of a pull request's linked
// issues and the weakest tier among the links that contributed to it.
func pullRequestMembershipOf(links []pullRequestIssueLink, sets map[workItemKey][]string) ([]string, string) {
	union := map[string]bool{}
	tier := ""
	for _, link := range links {
		set := sets[link.issue]
		if len(set) == 0 {
			continue
		}
		for _, identity := range set {
			union[identity] = true
		}
		if tier == "" || issuePullRequestLinkTierRank[link.tier] < issuePullRequestLinkTierRank[tier] {
			tier = link.tier
		}
	}
	out := make([]string, 0, len(union))
	for identity := range union {
		out = append(out, identity)
	}
	sort.Strings(out)
	return out, tier
}

// pullRequestKeyOf parses pullRequestCanonicalID.
func pullRequestKeyOf(canonicalID string) (pullRequestKey, bool) {
	rest, ok := strings.CutPrefix(canonicalID, "pull_request:")
	if !ok {
		return pullRequestKey{}, false
	}
	cut := strings.LastIndexByte(rest, ':')
	if cut <= 0 {
		return pullRequestKey{}, false
	}
	number, err := strconv.ParseUint(rest[cut+1:], 10, 32)
	if err != nil {
		return pullRequestKey{}, false
	}
	return pullRequestKey{repoID: rest[:cut], number: uint32(number)}, true
}

// readPullRequestIssueLinks reads the link rows of exactly the named pull
// requests, keeping what the link producer would project: a known tier, a
// non-pull-request issue row, and no lower-tier row a native row covers
// (issuePullRequestLinkOutranked). ambiguous counts the links
// resolveLinkedIssues dropped.
func readPullRequestIssueLinks(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, keys []pullRequestKey) (map[pullRequestKey][]pullRequestIssueLink, int, error) {
	links := make(map[pullRequestKey][]pullRequestIssueLink, len(keys))
	for start := 0; start < len(keys); start += workItemMembershipReadKeys {
		chunk := keys[start:min(start+workItemMembershipReadKeys, len(keys))]
		bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}}
		terms := make([]string, len(chunk))
		for i, key := range chunk {
			terms[i] = fmt.Sprintf("(l.repo_id = toUUID({r%[1]d:String}) AND l.pr_number = {n%[1]d:UInt32})", i)
			bindings = append(bindings,
				contextpacket.ClickHouseBinding{Name: fmt.Sprintf("r%d", i), Value: key.repoID},
				contextpacket.ClickHouseBinding{Name: fmt.Sprintf("n%d", i), Value: key.number})
		}
		statement := `SELECT toString(l.repo_id) ` + pullRequestMembershipLinkMarker + `, l.pr_number, toString(w.repo_id), l.work_item_id, l.provenance, ifNull(w.type, ''), ` + issuePullRequestLinkOutranked + `
FROM work_graph_issue_pr AS l FINAL
INNER JOIN work_items AS w FINAL ON w.org_id = l.org_id AND w.work_item_id = l.work_item_id
WHERE l.org_id = {org_id:String} AND (` + strings.Join(terms, " OR ") + `)`
		if err := readPullRequestIssueLinkChunk(ctx, client, statement, bindings, links); err != nil {
			return nil, 0, err
		}
	}
	ambiguous := 0
	for key, rows := range links {
		resolved, dropped := resolveLinkedIssues(key, rows)
		links[key] = resolved
		ambiguous += dropped
	}
	return links, ambiguous, nil
}

// resolveLinkedIssues keeps one issue per link id of a pull request. A link
// id that joined one row is that issue. One that joined several is the row in
// the pull request's own repository; with none, it cannot be told which issue
// the link names, so it contributes nothing and is counted: a wrong project
// would admit a project-only caller, and unstated is the disclosed path.
func resolveLinkedIssues(key pullRequestKey, rows []pullRequestIssueLink) ([]pullRequestIssueLink, int) {
	byID := map[string][]pullRequestIssueLink{}
	for _, row := range rows {
		byID[row.issue.workItemID] = append(byID[row.issue.workItemID], row)
	}
	resolved := make([]pullRequestIssueLink, 0, len(byID))
	ambiguous := 0
	for _, candidates := range byID {
		if len(candidates) == 1 {
			resolved = append(resolved, candidates[0])
			continue
		}
		own := slices.IndexFunc(candidates, func(row pullRequestIssueLink) bool { return row.issue.repoID == key.repoID })
		if own < 0 {
			ambiguous++
			continue
		}
		resolved = append(resolved, candidates[own])
	}
	return resolved, ambiguous
}

func readPullRequestIssueLinkChunk(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding, links map[pullRequestKey][]pullRequestIssueLink) error {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var prRepoID, issueRepoID, workItemID, provenance, issueType string
		var number uint32
		var outranked uint8
		if err := rows.Scan(&prRepoID, &number, &issueRepoID, &workItemID, &provenance, &issueType, &outranked); err != nil {
			return err
		}
		if outranked != 0 {
			continue
		}
		if _, known := issuePullRequestLinkTierRank[strings.TrimSpace(provenance)]; !known {
			continue
		}
		if _, isPullRequest := pullRequestWorkItemTypes[issueType]; isPullRequest {
			continue
		}
		key := pullRequestKey{repoID: prRepoID, number: number}
		links[key] = append(links[key], pullRequestIssueLink{issue: workItemKey{repoID: issueRepoID, workItemID: workItemID}, tier: strings.TrimSpace(provenance)})
	}
	return rows.Err()
}

// withPullRequestProjectMembership returns a copy of tables with the pull
// request producer stating membership and the refresh table beside the link
// table it follows.
func withPullRequestProjectMembership(tables []entityTable) []entityTable {
	out := make([]entityTable, 0, len(tables)+1)
	for _, table := range tables {
		switch table.name {
		case "git_pull_requests":
			table.query = queryPullRequestsWithMembership
			out = append(out, table)
		case "work_graph_issue_pr":
			out = append(out, table, entityTable{
				name:         pullRequestProjectMembershipTable,
				query:        queryPullRequestMembershipChanges,
				subjectKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectPullRequest},
			})
		default:
			out = append(out, table)
		}
	}
	return out
}
