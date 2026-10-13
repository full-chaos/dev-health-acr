package devhealthfacts

import (
	"context"
	"sort"
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

func commitmentItemRowsSQL(authorized string) string {
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
SELECT a.project_key, a.items_all,
  toInt64(ifNull(i.items_total, 0)), toInt64(ifNull(i.items_open, 0)), toInt64(ifNull(i.items_closed, 0)), toInt64(ifNull(i.items_unknown_status, 0)),
  toInt64(ifNull(i.items_with_due_at, 0)), toInt64(ifNull(i.items_past_due_open, 0)),
  toInt64(ifNull(s.sprint_count, 0)), toInt64(ifNull(s.next_count, 0)), ifNull(s.next_end, toDateTime64(0, 3, 'UTC')), ifNull(s.latest_end, toDateTime64(0, 3, 'UTC'))
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
    uniqExact(s.sprint_id) AS sprint_count,
    countIf(s.ended_at >= {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')}) AS next_count,
    minIf(s.ended_at, s.ended_at >= {` + commitmentAsOfParam + `:DateTime64(3, 'UTC')}) AS next_end,
    max(s.ended_at) AS latest_end
  FROM project_items AS pi
  INNER JOIN (
    SELECT provider, sprint_id, assumeNotNull(ended_at) AS ended_at
    FROM sprints FINAL
    WHERE org_id = {org_id:String} AND ended_at IS NOT NULL
  ) AS s ON s.provider = pi.provider AND s.sprint_id = pi.sprint_id
  WHERE pi.sprint_id != ''
  GROUP BY project_key
) AS s ON s.project_key = a.project_key
ORDER BY a.project_key`
}

type commitmentProjectRow struct {
	key, provider, state, stateSource, targetDate string
	itemsReachable                                bool
}

type commitmentItemRow struct {
	all, total, open, closed, unknownStatus, withDueAt, pastDueOpen int64
	sprintCount, nextCount                                          int64
	nextEnd, latestEnd                                              time.Time
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
	asOf := clock().UTC()
	projects, err := p.readProjectRows(ctx, orgID, ids)
	if err != nil {
		return contextfabric.FactProviderResult{}, readFailure("query project commitments", err)
	}
	items, err := p.readItemRows(ctx, principal, orgID, query.RequestedRepositoryScope, ids, asOf)
	if err != nil {
		return contextfabric.FactProviderResult{}, readFailure("query project commitment items", err)
	}
	sort.Strings(ids)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	for _, key := range ids {
		project, ok := projects[key]
		if !ok {
			continue
		}
		var counts *commitmentItemRow
		if project.itemsReachable {
			row := items[key]
			counts = &row
		}
		_, id := splitProjectKey(key)
		facts = append(facts, contextfabric.CanonicalFact{
			Kind:           contextfabric.FactCommitment,
			Subject:        bySubject[key],
			Fields:         commitmentFields(project, counts, asOf),
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

func (p *CommitmentProvider) readItemRows(ctx context.Context, principal storage.Principal, orgID string, requestedScope, ids []string, asOf time.Time) (map[string]commitmentItemRow, error) {
	authorized, bindings := workItemAuthorizedIDsAmongSQL(workItemRepositoryAuthorization(principal, requestedScope), commitmentProjectItemIDsSQL)
	settings, err := workItemReaderSettings(ctx)
	if err != nil {
		return nil, err
	}
	bindings = append(bindings, readers.Binding{Name: commitmentAsOfParam, Value: asOf})
	rows := map[string]commitmentItemRow{}
	err = readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadProjectCommitmentItems", readers.WithSettings(commitmentItemRowsSQL(authorized), settings), orgID, ids, func(row readers.RowScanner) error {
		var key string
		var r commitmentItemRow
		if err := row.Scan(&key, &r.all, &r.total, &r.open, &r.closed, &r.unknownStatus, &r.withDueAt, &r.pastDueOpen, &r.sprintCount, &r.nextCount, &r.nextEnd, &r.latestEnd); err != nil {
			return err
		}
		rows[key] = r
		return nil
	}, bindings...)
	return rows, err
}

func commitmentFields(project commitmentProjectRow, items *commitmentItemRow, asOf time.Time) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{
		"as_of":         contextfabric.StringFactValue(asOf.Format(time.RFC3339)),
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
	if strings.EqualFold(strings.TrimSpace(project.provider), "github") {
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
		fields["latest_sprint_end"] = contextfabric.StringFactValue(items.latestEnd.UTC().Format(time.RFC3339))
		if items.nextCount > 0 {
			fields["next_sprint_end"] = contextfabric.StringFactValue(items.nextEnd.UTC().Format(time.RFC3339))
		}
	}
	unreadable := max(items.all-items.total, 0)
	fields["items_unreadable_count"] = contextfabric.IntegerFactValue(unreadable)
	if unreadable > 0 {
		fields["items_unreadable_reason"] = contextfabric.StringFactValue(commitmentItemsUnreadableReason)
	}
	switch {
	case stated:
		fields["commitment_status"] = contextfabric.StringFactValue(commitmentStatusStated)
	case unreadable == 0:
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

func commitmentDaysBetween(asOf, target time.Time) int64 {
	day := time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)
	return int64(target.Sub(day).Hours() / 24)
}
