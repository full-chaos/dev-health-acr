package devhealthfacts

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	commitmentStatusStated     = "stated"
	commitmentStatusNoneStated = "none_stated"

	commitmentItemsUnreachableReason = "project_key_ambiguous"
	commitmentItemsUnreadableReason  = "items_not_readable_for_caller"
	commitmentSprintUnresolvedReason = "sprint_not_found_or_without_end"
	commitmentNotCollected           = "not_collected"

	commitmentAsOfParam = "as_of"
)

var commitmentSprintKindByProvider = map[string]string{
	"jira":   "sprint",
	"linear": "cycle",
	"github": "milestone",
	"gitlab": "milestone",
}

func commitmentSprintKind(provider string) (string, bool) {
	kind, ok := commitmentSprintKindByProvider[strings.ToLower(strings.TrimSpace(provider))]
	return kind, ok
}

const commitmentProjectRowsSQL = `SELECT concat(p.provider, ':', p.id) AS project_key, p.provider, toString(p.state), toString(p.state_source), ifNull(toString(p.target_date), ''),
  toUInt8(concat(p.provider, ':', p.id) IN (SELECT project_key FROM ` + projectJoinKeysSQL + `)) AS items_reachable
FROM projects AS p FINAL
WHERE p.org_id = {org_id:String} AND concat(p.provider, ':', p.id) IN {ids:Array(String)}
ORDER BY project_key`

const commitmentProjectItemIDsSQL = `(SELECT w.work_item_id FROM work_items AS w FINAL
  INNER JOIN ` + projectJoinKeysSQL + ` AS k ON k.provider = w.provider AND k.join_key = w.project_id
  WHERE w.org_id = {org_id:String})`

const commitmentClosedStatusSQL = `('done', '` + workItemCancelledStatus + `')`

var commitmentOpenStatusSQL = func() string {
	open := []string{}
	for _, status := range contextfabric.WorkItemStatusVocabulary() {
		switch status {
		case "done", workItemCancelledStatus, "unknown":
			continue
		}
		open = append(open, "'"+status+"'")
	}
	return "(" + strings.Join(open, ", ") + ")"
}()

var commitmentOpenStatuses = func() map[string]bool {
	open := map[string]bool{}
	for _, status := range contextfabric.WorkItemStatusVocabulary() {
		switch status {
		case "done", workItemCancelledStatus, "unknown":
			continue
		}
		open[status] = true
	}
	return open
}()

func commitmentProjectItemsCTE(authorized string) string {
	return `WITH project_rows AS (
  SELECT k.project_key AS project_key, w.provider AS provider, w.work_item_id AS work_item_id,
    argMax(tuple(lower(w.status), w.due_at, w.sprint_id), w.last_synced) AS latest
  FROM work_items AS w FINAL
  INNER JOIN ` + projectJoinKeysSQL + ` AS k ON k.provider = w.provider AND k.join_key = w.project_id
  WHERE w.org_id = {org_id:String} AND ` + workItemIsIssueSQL("w") + `
  GROUP BY project_key, provider, work_item_id
),
project_items AS (
  SELECT project_key, provider, work_item_id, tupleElement(latest, 1) AS status, tupleElement(latest, 2) AS due_at, tupleElement(latest, 3) AS sprint_id
  FROM project_rows
  WHERE work_item_id IN ` + authorized + `
)
`
}

const (
	commitmentDueDatesCap       = 25
	commitmentDueDatesTable     = "issue_due_dates"
	commitmentDueDatesTruncated = "issue_due_dates_truncated"
)

func commitmentDueDateRowsSQL(authorized string) string {
	return `SELECT * FROM (
` + commitmentProjectItemsCTE(authorized) + `SELECT project_key, work_item_id, status, toInt64(toUnixTimestamp64Milli(assumeNotNull(due_at))) AS due_ms
FROM project_items
WHERE due_at IS NOT NULL
ORDER BY project_key,
  (status IN ` + commitmentOpenStatusSQL + ` AND due_at < {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')}) DESC,
  due_at ASC, work_item_id ASC
LIMIT ` + strconv.Itoa(commitmentDueDatesCap+1) + ` BY project_key
) AS commitment_due_dates`
}

func commitmentItemRowsSQL(authorized string) string {
	return `SELECT * FROM (
` + commitmentProjectItemsCTE(authorized) + `SELECT a.project_key, a.items_all,
  toInt64(ifNull(i.items_total, 0)), toInt64(ifNull(i.items_open, 0)), toInt64(ifNull(i.items_closed, 0)), toInt64(ifNull(i.items_unknown_status, 0)),
  toInt64(ifNull(i.items_with_due_at, 0)), toInt64(ifNull(i.items_past_due_open, 0)),
  toInt64(ifNull(s.sprint_count, 0)), toInt64(ifNull(s.next_count, 0)), toInt64(ifNull(s.next_end_ms, 0)), toInt64(ifNull(s.latest_end_ms, 0)),
  toInt64(ifNull(u.sprint_refs_unresolved, 0))
FROM (
  SELECT project_key, toInt64(count()) AS items_all
  FROM project_rows
  GROUP BY project_key
) AS a
LEFT JOIN (
  SELECT project_key,
    count() AS items_total,
    countIf(status IN ` + commitmentOpenStatusSQL + `) AS items_open,
    countIf(status IN ` + commitmentClosedStatusSQL + `) AS items_closed,
    countIf(status NOT IN ` + commitmentOpenStatusSQL + ` AND status NOT IN ` + commitmentClosedStatusSQL + `) AS items_unknown_status,
    countIf(due_at IS NOT NULL) AS items_with_due_at,
    countIf(status IN ` + commitmentOpenStatusSQL + ` AND due_at IS NOT NULL AND due_at < {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')}) AS items_past_due_open
  FROM project_items
  GROUP BY project_key
) AS i ON i.project_key = a.project_key
LEFT JOIN (
  SELECT pi.project_key AS project_key,
    uniqExact(sp.sprint_id) AS sprint_count,
    countIf(sp.sprint_end >= {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')}) AS next_count,
    toUnixTimestamp64Milli(minIf(sp.sprint_end, sp.sprint_end >= {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')})) AS next_end_ms,
    toUnixTimestamp64Milli(max(sp.sprint_end)) AS latest_end_ms
  FROM project_items AS pi
  INNER JOIN (
    SELECT provider, sprint_id, assumeNotNull(ended_at) AS sprint_end
    FROM sprints FINAL
    WHERE org_id = {org_id:String} AND isNotNull(ended_at)
  ) AS sp ON sp.provider = pi.provider AND sp.sprint_id = pi.sprint_id
  WHERE pi.sprint_id != ''
  GROUP BY project_key
) AS s ON s.project_key = a.project_key
LEFT JOIN (
  SELECT pi.project_key AS project_key,
    countIf(ifNull(ended.sprint_id, '') = '') AS sprint_refs_unresolved
  FROM project_items AS pi
  LEFT JOIN (
    SELECT DISTINCT provider, sprint_id
    FROM sprints FINAL
    WHERE org_id = {org_id:String} AND isNotNull(ended_at)
  ) AS ended ON ended.provider = pi.provider AND ended.sprint_id = pi.sprint_id
  WHERE pi.sprint_id != ''
  GROUP BY project_key
) AS u ON u.project_key = a.project_key
ORDER BY a.project_key
)`
}

type commitmentProjectRow struct {
	key, provider, state, stateSource, targetDate string
	itemsReachable                                bool
}

type commitmentItemRow struct {
	all, total, open, closed, unknownStatus, withDueAt, pastDueOpen int64
	sprintCount, nextCount, sprintRefsUnresolved                    int64
	nextEndMs, latestEndMs                                          int64
}

type commitmentDueDate struct {
	workItemID, status string
	dueMs              int64
	pastDue            bool
}

type CommitmentProvider struct{ facts clickhouseFacts }

func newCommitmentProvider(client contextpacket.ClickHouseQueryClient) *CommitmentProvider {
	return &CommitmentProvider{facts: clickhouseFacts{client: client}}
}

func (p *CommitmentProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactCommitment, "devhealthfacts.commitment", []contextfabric.SubjectKind{contextfabric.SubjectProject})
}

func (p *CommitmentProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(subjectsOfKind(query.Subjects, contextfabric.SubjectProject), identity.KindProject)
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.commitment", contextfabric.FactCommitment, rejected)
		}
	}()
	if len(ids) == 0 {
		state, reason := currentAxisReadState(0)
		return contextfabric.FactProviderResult{State: state, Reason: reason, Version: QueryVersion}, nil
	}
	now := clock().UTC()
	asOf := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	projects, err := p.readProjectRows(ctx, orgID, ids)
	if err != nil {
		return contextfabric.FactProviderResult{}, readFailure("query project commitments", err)
	}
	items, err := p.readItemRows(ctx, principal, orgID, query.RequestedRepositoryScope, ids, asOf)
	if err != nil {
		return contextfabric.FactProviderResult{}, readFailure("query project commitment items", err)
	}
	dueDates, err := p.readDueDateRows(ctx, principal, orgID, query.RequestedRepositoryScope, ids, asOf)
	if err != nil {
		return contextfabric.FactProviderResult{}, readFailure("query project commitment due dates", err)
	}
	sort.Strings(ids)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	for _, key := range ids {
		project, ok := projects[key]
		if !ok {
			continue
		}
		var counts *commitmentItemRow
		var dated []commitmentDueDate
		if project.itemsReachable {
			row := items[key]
			counts = &row
			dated = dueDates[key]
		}
		_, id := splitProjectKey(key)
		facts = append(facts, contextfabric.CanonicalFact{
			Kind:           contextfabric.FactCommitment,
			Subject:        bySubject[key],
			Fields:         p.commitmentFields(project, counts, dated, asOf),
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityProject, project.provider+":"+id)},
		})
	}
	state, reason := currentAxisReadState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: reason, Version: QueryVersion}, nil
}

func (p *CommitmentProvider) readProjectRows(ctx context.Context, orgID string, ids []string) (map[string]commitmentProjectRow, error) {
	rows := map[string]commitmentProjectRow{}
	err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectCommitments", commitmentProjectRowsSQL, orgID, ids, func(row readers.RowScanner) error {
		var r commitmentProjectRow
		var reachable uint8
		if err := row.Scan(&r.key, &r.provider, &r.state, &r.stateSource, &r.targetDate, &reachable); err != nil {
			return err
		}
		r.itemsReachable = reachable == 1
		rows[r.key] = r
		return nil
	})
	return rows, err
}

func commitmentAuthorizedItemIDsSQL(principal storage.Principal, requestedScope []string) (string, []readers.Binding) {
	return workItemAuthorizedIDsAmongSQL(workItemRepositoryAuthorization(principal, requestedScope), commitmentProjectItemIDsSQL)
}

func (p *CommitmentProvider) readItemRows(ctx context.Context, principal storage.Principal, orgID string, requestedScope, ids []string, asOf time.Time) (map[string]commitmentItemRow, error) {
	authorized, bindings := commitmentAuthorizedItemIDsSQL(principal, requestedScope)
	settings, err := workItemReaderSettings(ctx)
	if err != nil {
		return nil, err
	}
	bindings = append(bindings, readers.Binding{Name: commitmentAsOfParam, Value: asOf})
	rows := map[string]commitmentItemRow{}
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectCommitmentItems", readers.WithSettings(commitmentItemRowsSQL(authorized), settings), orgID, ids, func(row readers.RowScanner) error {
		var key string
		var r commitmentItemRow
		if err := row.Scan(&key, &r.all, &r.total, &r.open, &r.closed, &r.unknownStatus, &r.withDueAt, &r.pastDueOpen, &r.sprintCount, &r.nextCount, &r.nextEndMs, &r.latestEndMs, &r.sprintRefsUnresolved); err != nil {
			return err
		}
		rows[key] = r
		return nil
	}, bindings...)
	return rows, err
}

func (p *CommitmentProvider) readDueDateRows(ctx context.Context, principal storage.Principal, orgID string, requestedScope, ids []string, asOf time.Time) (map[string][]commitmentDueDate, error) {
	authorized, bindings := commitmentAuthorizedItemIDsSQL(principal, requestedScope)
	settings, err := workItemReaderSettings(ctx)
	if err != nil {
		return nil, err
	}
	bindings = append(bindings, readers.Binding{Name: commitmentAsOfParam, Value: asOf})
	rows := map[string][]commitmentDueDate{}
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectCommitmentDueDates", readers.WithSettings(commitmentDueDateRowsSQL(authorized), settings), orgID, ids, func(row readers.RowScanner) error {
		var key string
		var r commitmentDueDate
		if err := row.Scan(&key, &r.workItemID, &r.status, &r.dueMs); err != nil {
			return err
		}
		r.pastDue = commitmentOpenStatuses[r.status] && r.dueMs < asOf.UnixMilli()
		rows[key] = append(rows[key], r)
		return nil
	}, bindings...)
	return rows, err
}

// commitmentDueDatesFact orders past due open issues first, then by due date,
// and keeps the first commitmentDueDatesCap. The statement reads one row more
// than the cap, so a longer list is named truncated and never cut silently.
func commitmentDueDatesFact(dueDates []commitmentDueDate) (table contextfabric.FactValue, truncated, ok bool) {
	if len(dueDates) == 0 {
		return contextfabric.FactValue{}, false, false
	}
	sorted := append([]commitmentDueDate(nil), dueDates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case a.pastDue != b.pastDue:
			return a.pastDue
		case a.dueMs != b.dueMs:
			return a.dueMs < b.dueMs
		default:
			return a.workItemID < b.workItemID
		}
	})
	if len(sorted) > commitmentDueDatesCap {
		sorted, truncated = sorted[:commitmentDueDatesCap], true
	}
	rows := make([]contextfabric.FactValueRow, 0, len(sorted))
	for _, d := range sorted {
		rows = append(rows, contextfabric.FactValueRow{Fields: map[string]contextfabric.FactValue{
			"work_item_id": contextfabric.StringFactValue(d.workItemID),
			"due_at":       contextfabric.StringFactValue(time.UnixMilli(d.dueMs).UTC().Format(time.RFC3339)),
			"status":       contextfabric.StringFactValue(d.status),
			"past_due":     contextfabric.BooleanFactValue(d.pastDue),
		}})
	}
	return contextfabric.TableFactValue(contextfabric.FactTable{
		Shape:        contextfabric.FactTableBreakdown,
		Key:          []string{"work_item_id"},
		Observations: []string{"due_at", "status", "past_due"},
		Rows:         rows,
	}), truncated, true
}

func (p *CommitmentProvider) commitmentFields(project commitmentProjectRow, items *commitmentItemRow, dueDates []commitmentDueDate, asOf time.Time) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{
		"as_of_day":     contextfabric.StringFactValue(asOf.Format(time.DateOnly)),
		"project_state": stringOrNull(project.state),
		"state_source":  stringOrNull(project.stateSource),
	}
	stated := false
	if target, ok := commitmentTargetDate(project.targetDate); ok {
		stated = true
		fields["target_date"] = contextfabric.StringFactValue(project.targetDate)
		fields["target_date_provider"] = contextfabric.StringFactValue(project.provider)
		fields["days_to_target"] = contextfabric.IntegerFactValue(commitmentDaysBetween(asOf, target))
	}
	sourceNotCollected := strings.EqualFold(strings.TrimSpace(project.provider), "github")
	if sourceNotCollected {
		fields["iteration_basis"] = contextfabric.StringFactValue(commitmentNotCollected)
	}
	if items == nil {
		fields["items_unavailable_reason"] = contextfabric.StringFactValue(commitmentItemsUnreachableReason)
		if stated {
			fields["commitment_status"] = contextfabric.StringFactValue(commitmentStatusStated)
		}
		return fields
	}
	fields["items_total"] = contextfabric.IntegerFactValue(items.total)
	fields["items_open"] = contextfabric.IntegerFactValue(items.open)
	fields["items_closed"] = contextfabric.IntegerFactValue(items.closed)
	fields["unknown_status_count"] = contextfabric.IntegerFactValue(items.unknownStatus)
	fields["items_with_due_at"] = contextfabric.IntegerFactValue(items.withDueAt)
	fields["items_without_due_at"] = contextfabric.IntegerFactValue(items.total - items.withDueAt)
	fields["items_past_due_open"] = contextfabric.IntegerFactValue(items.pastDueOpen)
	if items.withDueAt > 0 {
		stated = true
	}
	if kind, ok := commitmentSprintKind(project.provider); ok && items.sprintCount > 0 {
		stated = true
		fields["sprint_kind"] = contextfabric.StringFactValue(kind)
		fields["latest_sprint_end"] = contextfabric.StringFactValue(time.UnixMilli(items.latestEndMs).UTC().Format(time.RFC3339))
		if items.nextCount > 0 {
			fields["next_sprint_end"] = contextfabric.StringFactValue(time.UnixMilli(items.nextEndMs).UTC().Format(time.RFC3339))
		}
	}
	fields["sprint_refs_unresolved"] = contextfabric.IntegerFactValue(items.sprintRefsUnresolved)
	if items.sprintRefsUnresolved > 0 {
		fields["sprint_refs_unresolved_reason"] = contextfabric.StringFactValue(commitmentSprintUnresolvedReason)
	}
	if table, truncated, ok := commitmentDueDatesFact(dueDates); ok {
		fields[commitmentDueDatesTable] = table
		fields[commitmentDueDatesTruncated] = contextfabric.BooleanFactValue(truncated)
	}
	unreadable := max(items.all-items.total, 0)
	fields["items_unreadable_count"] = contextfabric.IntegerFactValue(unreadable)
	if unreadable > 0 {
		fields["items_unreadable_reason"] = contextfabric.StringFactValue(commitmentItemsUnreadableReason)
	}
	switch {
	case stated:
		fields["commitment_status"] = contextfabric.StringFactValue(commitmentStatusStated)
	case unreadable == 0 && !sourceNotCollected && items.total > 0 && items.sprintRefsUnresolved == 0:
		fields["commitment_status"] = contextfabric.StringFactValue(commitmentStatusNoneStated)
	}
	return fields
}

func commitmentTargetDate(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.DateOnly, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

func commitmentDaysBetween(asOfDay, target time.Time) int64 {
	return int64(target.Sub(asOfDay).Hours() / 24)
}
