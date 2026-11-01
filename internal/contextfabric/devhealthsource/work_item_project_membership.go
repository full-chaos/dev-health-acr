package devhealthsource

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12226: a work item's CURRENT project membership, stated on its own
// entity write as canonical project.v2 identities
// (ContextFabricEntityProjection.ProjectIdentities). The source of record is
// the issue's own project link as the ops view project_membership_presence
// serves it: the work item's project_id column, or, for a work item with
// membership history, its latest transition into a project. Never a
// repository, an issue-key prefix or team ownership (CHAOS-12107 v3 V1).
//
// Both reads are on only when WithWorkItemProjectMembership enables them.
// The tables of this source share one cursor (cursor.go), so turning them on
// in an organization already projected states membership only for work items
// written afterwards and for membership changes after that cursor; the
// backfill is a from-zero rebuild (snapshotRead reads every table whole), a
// roll step chris approves (CHAOS-12107 ruling), never a default. While they
// are off, membership changes are not followed, so a value written while
// they were on can go stale: any change of the flag goes with a rebuild.

// workItemMembershipMarker names the page membership read.
const workItemMembershipMarker = "AS membership_repo_id"

// workItemMembershipRefreshMarker names the refresh read's change column.
const workItemMembershipRefreshMarker = "AS membership_changed_at"

// workItemMembershipReadKeys bounds how many work items one membership
// statement names.
const workItemMembershipReadKeys = 100

type workItemKey struct{ repoID, workItemID string }

// queryWorkItemsWithMembership is queryWorkItems with each emitted work item's
// current project membership stated.
func queryWorkItemsWithMembership(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	items, truncated, err := queryWorkItems(ctx, client, orgID, cursor, limit)
	if err != nil {
		return nil, false, err
	}
	if err := stateWorkItemProjectMemberships(ctx, client, orgID, items, membershipStatedByEntity); err != nil {
		return nil, false, err
	}
	return items, truncated, nil
}

// queryWorkItemMembershipChanges is the refresh table (R3-2): it re-emits,
// through the one work-item row builder and with the full current set, every
// work item whose membership inputs changed since its own cursor without the
// work item itself syncing:
//   - a transition ingested for it (an add, a move, a REMOVE, the loss of its
//     last project);
//   - a change to a project its membership names, by id or by any key the
//     project carries in an unmerged version (resolution, ambiguity, rename).
//
// A change to the work item's own project_id column moves w.last_synced and
// is already re-read by queryWorkItems.
func queryWorkItemMembershipChanges(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
	statement := workItemColumns + `, c.membership_changed_at
FROM work_items AS w FINAL
INNER JOIN ` + workItemMembershipChangesSubquery(cursor) + ` AS c ON c.repo_id = w.repo_id AND c.subject_id = w.work_item_id
LEFT JOIN repos AS r FINAL ON r.id = w.repo_id AND r.org_id = w.org_id
WHERE w.org_id = {org_id:String}` + sincePredicate(cursor, "c.membership_changed_at", workItemRowKey) + orderBy("c.membership_changed_at", workItemRowKey)
	items, truncated, err := fetchIngest(ctx, client, statement, rowLimitBindings(orgID, cursor, limit), limit, scanWorkItemRow)
	if err != nil {
		return nil, false, err
	}
	if err := stateWorkItemProjectMemberships(ctx, client, orgID, items, membershipStatedByRefresh); err != nil {
		return nil, false, err
	}
	return items, truncated, nil
}

// workItemMembershipChangesSubquery is one row per work item with its latest
// membership-input stamp. With a cursor, each arm keeps only stamps at or
// after it, so a work item none of them stamped since yields no row; the
// latest kept stamp is then the latest overall for every work item kept.
func workItemMembershipChangesSubquery(cursor cursorState) string {
	transitionsSince, projectsSince := "", ""
	if !cursor.Since.IsZero() {
		transitionsSince = " AND ingested_at >= " + sinceBoundSQL
		projectsSince = " HAVING changed_at >= " + sinceBoundSQL
	}
	return `(
  SELECT repo_id, subject_id, max(stamp) ` + workItemMembershipRefreshMarker + `
  FROM (
    SELECT repo_id, subject_id, ingested_at AS stamp
    FROM project_membership_transitions
    WHERE org_id = {org_id:String} AND subject_kind = 'work_item'` + transitionsSince + `
    UNION ALL
    SELECT m.repo_id AS repo_id, m.subject_id AS subject_id, pc.changed_at AS stamp
    FROM project_membership_presence AS m
    INNER JOIN (
      SELECT provider, arrayJoin(keys) AS join_key, changed_at
      FROM (
        SELECT provider, arrayDistinct(arrayConcat([id], groupArrayIf(ifNull(project_key, ''), ifNull(project_key, '') != ''))) AS keys, max(last_synced) AS changed_at
        FROM projects
        WHERE org_id = {org_id:String}
        GROUP BY provider, id` + projectsSince + `
      )
    ) AS pc ON pc.provider = m.provider AND pc.join_key = m.project_id
    WHERE m.org_id = {org_id:String} AND m.subject_kind = 'work_item'
  )
  GROUP BY repo_id, subject_id
)`
}

// stateWorkItemProjectMemberships sets ProjectIdentities on every work-item
// entity among items: the full current set, an empty list when the work item
// is in no project. A failed read returns the error, so the page fails and
// nothing is written: a partial set is never stated as current.
//
// It logs the outcome of the read at Info (CHAOS-12300, the #1821 review
// F3): how many work items it stated as members, and how many it stated in
// no project, by why, so a read regression that empties memberships is never
// silent. Counts only, never ids.
func stateWorkItemProjectMemberships(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, items []candidate, by membershipStatedBy) error {
	var keys []workItemKey
	targets := map[workItemKey][]*contractsv1.ContextFabricEntityProjection{}
	for i := range items {
		entity := items[i].entity
		if entity == nil || entity.Subject.Kind != contractsv1.ContextFabricSubjectWorkItem {
			continue
		}
		segments, ok := identity.Segments(identity.KindWorkItem, entity.Subject.CanonicalID)
		if !ok || len(segments) != 2 {
			return fmt.Errorf("devhealthsource: work item canonical id does not parse")
		}
		key := workItemKey{repoID: segments[0], workItemID: segments[1]}
		if _, seen := targets[key]; !seen {
			keys = append(keys, key)
		}
		targets[key] = append(targets[key], entity)
	}
	sets, rowed, err := readWorkItemProjectMemberships(ctx, client, orgID, keys)
	if err != nil {
		return err
	}
	// A refresh pass that re-read nothing still writes its line, with zeros:
	// a normal empty refresh is visible, and differs from a pass that never
	// ran. One line per pass, never per work item.
	if len(keys) > 0 || by == membershipStatedByRefresh {
		recordMembershipOutcomes(ctx, orgID, by, membershipOutcomesOf(keys, sets, rowed, by))
	}
	for key, entities := range targets {
		for _, entity := range entities {
			set := append([]string{}, sets[key]...)
			entity.ProjectIdentities = &set
		}
	}
	return nil
}

// readWorkItemProjectMemberships reads the current membership of exactly the
// named work items: every row of project_membership_presence for them,
// resolved to the project row by (provider, id or key). A row that resolves
// to no project, or to more than one, names no project and is left out, the
// rule the BELONGS_TO_PROJECT producer applies (teams_projects_edges.go).
// A work item no row resolves for is absent from the result: in no project.
//
// rowed holds every work item at least one membership row named, resolved or
// not: a work item in rowed with no set had memberships that named no single
// project.
func readWorkItemProjectMemberships(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, keys []workItemKey) (sets map[workItemKey][]string, rowed map[workItemKey]bool, err error) {
	// A work item no row resolves for has no entry; its caller states the
	// empty set for it (stateWorkItemProjectMemberships copies sets[key]).
	sets = make(map[workItemKey][]string, len(keys))
	rowed = make(map[workItemKey]bool, len(keys))
	for start := 0; start < len(keys); start += workItemMembershipReadKeys {
		chunk := keys[start:min(start+workItemMembershipReadKeys, len(keys))]
		bindings := []contextpacket.ClickHouseBinding{{Name: "org_id", Value: orgID}}
		terms := make([]string, len(chunk))
		for i, key := range chunk {
			terms[i] = fmt.Sprintf("(m.repo_id = toUUID({r%[1]d:String}) AND m.subject_id = {w%[1]d:String})", i)
			bindings = append(bindings,
				contextpacket.ClickHouseBinding{Name: fmt.Sprintf("r%d", i), Value: key.repoID},
				contextpacket.ClickHouseBinding{Name: fmt.Sprintf("w%d", i), Value: key.workItemID})
		}
		statement := `SELECT toString(m.repo_id) ` + workItemMembershipMarker + `, m.subject_id, m.provider, ifNull(p.id, ''), toUInt64(ifNull(p.key_resolution_count, 0))
FROM project_membership_presence AS m
LEFT JOIN ` + resolvedProjectsSubquery + ` AS p ON p.provider = m.provider AND p.join_key = m.project_id
WHERE m.org_id = {org_id:String} AND m.subject_kind = 'work_item' AND (` + strings.Join(terms, " OR ") + `)`
		if err := readWorkItemMembershipChunk(ctx, client, statement, bindings, sets, rowed); err != nil {
			return nil, nil, err
		}
	}
	for key, set := range sets {
		sort.Strings(set)
		sets[key] = set
	}
	return sets, rowed, nil
}

// readWorkItemMembershipChunk runs one membership statement and adds every
// row that names exactly one project to sets.
func readWorkItemMembershipChunk(ctx context.Context, client contextpacket.ClickHouseQueryClient, statement string, bindings []contextpacket.ClickHouseBinding, sets map[workItemKey][]string, rowed map[workItemKey]bool) error {
	rows, err := client.Query(ctx, statement, bindings)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[workItemKey]map[string]bool{}
	for rows.Next() {
		var repoID, workItemID, provider, resolvedProjectID string
		var keyResolutionCount uint64
		if err := rows.Scan(&repoID, &workItemID, &provider, &resolvedProjectID, &keyResolutionCount); err != nil {
			return err
		}
		// A row for a work item the statement did not name lands under its
		// own key, which stateWorkItemProjectMemberships never reads: a set
		// is attached only to the work item it was asked for.
		key := workItemKey{repoID: repoID, workItemID: workItemID}
		rowed[key] = true
		if keyResolutionCount != 1 || resolvedProjectID == "" || provider == "" {
			continue
		}
		projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{provider, resolvedProjectID}, nil)
		if err != nil {
			return err
		}
		// The contract refuses '|' in every authorization value (it is the
		// separator a delimited backend uses), and EncodeSegment does not
		// escape it. A project whose provider or id holds one cannot be
		// stated; it is left out, like an unresolved one, so the work item's
		// batch stays valid instead of failing the whole page.
		if omitted || strings.ContainsRune(projectCanonicalID, '|') {
			continue
		}
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		if seen[key][projectCanonicalID] {
			continue
		}
		seen[key][projectCanonicalID] = true
		sets[key] = append(sets[key], projectCanonicalID)
	}
	return rows.Err()
}

// workItemProjectMembershipTable is the refresh table's registry entry.
const workItemProjectMembershipTable = "work_items_project_membership"

// withWorkItemProjectMembership returns a copy of tables with the work_items
// producer stating membership and the refresh table right after it. It
// emits work items only.
func withWorkItemProjectMembership(tables []entityTable) []entityTable {
	out := make([]entityTable, 0, len(tables)+1)
	for _, table := range tables {
		if table.name == "work_items" {
			table.query = queryWorkItemsWithMembership
			out = append(out, table, entityTable{
				name:         workItemProjectMembershipTable,
				query:        queryWorkItemMembershipChanges,
				subjectKinds: []contractsv1.ContextFabricSubjectKind{contractsv1.ContextFabricSubjectWorkItem},
			})
			continue
		}
		out = append(out, table)
	}
	return out
}

// membershipStatedBy names the producer that stated a work item's membership.
type membershipStatedBy string

const (
	// membershipStatedByEntity: the work-item entity table, on the work
	// item's own sync.
	membershipStatedByEntity membershipStatedBy = "work_items"
	// membershipStatedByRefresh: the refresh table (R3-2), on a change to the
	// work item's membership inputs.
	membershipStatedByRefresh membershipStatedBy = workItemProjectMembershipTable
)

// membershipOutcomes counts one read's outcome per distinct work item
// (CHAOS-12300). An empty set is never one "absent" bucket:
//   - missing: no membership row names the work item (the entity table);
//   - stale: rows name it, but none resolves to exactly one statable
//     project (unresolved, ambiguous, or an id the contract cannot carry);
//   - cleared: the refresh table re-read it after a membership change and
//     no row names it any more (the loss of its last project).
type membershipOutcomes struct {
	WorkItems, Member, Missing, Stale, Cleared int
}

func membershipOutcomesOf(keys []workItemKey, sets map[workItemKey][]string, rowed map[workItemKey]bool, by membershipStatedBy) membershipOutcomes {
	out := membershipOutcomes{WorkItems: len(keys)}
	for _, key := range keys {
		switch {
		case len(sets[key]) > 0:
			out.Member++
		case rowed[key]:
			out.Stale++
		case by == membershipStatedByRefresh:
			out.Cleared++
		default:
			out.Missing++
		}
	}
	return out
}

// membershipOutcomeLogger is the logger membership outcomes go to; a test
// replaces it.
var membershipOutcomeLogger = slog.Default

// WorkItemMembershipOutcomeLogMessage is the Info line of one membership read.
const WorkItemMembershipOutcomeLogMessage = "context fabric work item project membership stated"

func recordMembershipOutcomes(ctx context.Context, orgID string, by membershipStatedBy, out membershipOutcomes) {
	membershipOutcomeLogger().InfoContext(ctx, WorkItemMembershipOutcomeLogMessage,
		"org_id", contextfabric.SanitizeLogAttr(orgID), "table", string(by),
		"work_items", out.WorkItems, "member", out.Member,
		"missing", out.Missing, "stale", out.Stale, "cleared", out.Cleared)
}
