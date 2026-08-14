package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const workItemPrefix = "work_item:"

// StatusProvider implements contextfabric.FactProvider for FactStatus from
// work_items.status -- the same column devhealthsource/tables.go's
// queryWorkItems already reads.
type StatusProvider struct{ facts clickhouseFacts }

func newStatusProvider(client contextpacket.ClickHouseQueryClient) *StatusProvider {
	return &StatusProvider{facts: clickhouseFacts{client: client}}
}

func (p *StatusProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactStatus, "devhealthfacts.status", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *StatusProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := refuseHistoricalFact(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, workItemPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := withRowLimit(`SELECT w.work_item_id, ifNull(w.status, '')
FROM work_items AS w FINAL
WHERE w.org_id = {org_id:String} AND w.work_item_id IN {ids:Array(String)}`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var id, status string
		if err := row.Scan(&id, &status); err != nil {
			return err
		}
		subject, ok := bySubject[id]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactStatus, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(status)},
			EvidenceRefIDs: []string{evidenceRefID("work-item", id)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item status", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: QueryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}

// WorkProvider implements contextfabric.FactProvider for FactWork -- minimal
// work descriptors (title) from work_items.title, the same column
// devhealthsource/tables.go's queryWorkItems already reads.
type WorkProvider struct{ facts clickhouseFacts }

func newWorkProvider(client contextpacket.ClickHouseQueryClient) *WorkProvider {
	return &WorkProvider{facts: clickhouseFacts{client: client}}
}

func (p *WorkProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactWork, "devhealthfacts.work", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *WorkProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := refuseHistoricalFact(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, workItemPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := withRowLimit(`SELECT w.work_item_id, ifNull(w.title, '')
FROM work_items AS w FINAL
WHERE w.org_id = {org_id:String} AND w.work_item_id IN {ids:Array(String)}`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var id, title string
		if err := row.Scan(&id, &title); err != nil {
			return err
		}
		subject, ok := bySubject[id]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWork, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"title": stringOrNull(title)},
			EvidenceRefIDs: []string{evidenceRefID("work-item", id)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item work descriptors", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: QueryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}

// ActualCompletionProvider implements contextfabric.FactProvider for
// FactActualCompletion from work_items.completed_at.
//
// Deviation from devhealthsource: devhealthsource/tables.go's queryWorkItems
// never selects completed_at (it only needed status/title/url/updated_at for
// projection), but the column is real -- it is seeded by
// testdata/fullstack/v1/seed/clickhouse/001_widget_service.sql's
// `INSERT INTO work_items (... completed_at, closed_at ...)` -- and
// FactActualCompletion has no honest way to answer "did this actually
// complete, and when" without it; a heuristic guess at which work_items.status
// strings count as "done" would be exactly the kind of invented vocabulary
// the fact/evidence semantics this package must not invent. "completed" is
// defined as completed_at being non-null; completed_at is only present in
// Fields when non-null.
type ActualCompletionProvider struct{ facts clickhouseFacts }

func newActualCompletionProvider(client contextpacket.ClickHouseQueryClient) *ActualCompletionProvider {
	return &ActualCompletionProvider{facts: clickhouseFacts{client: client}}
}

func (p *ActualCompletionProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactActualCompletion, "devhealthfacts.actual_completion", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *ActualCompletionProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, workItemPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// isNotNull/ifNull avoid ever scanning a bare Nullable(DateTime64) column
	// into Go, matching devhealthsource/tables.go's convention of only ever
	// scanning coalesced, non-null timestamps.
	// CHAOS-3781 Tier B: completion is the one work-item fact with a
	// recorded timestamp, so "was it done at T" is answerable exactly --
	// unlike the status vocabulary next door, which has no history at all
	// and refuses (refuseHistoricalFact). An item completed AFTER the
	// requested time reads as not completed then, which is what the row
	// actually records.
	completedExpression := "isNotNull(w.completed_at)"
	if timeBound.active {
		completedExpression = "toUInt8(w.completed_at IS NOT NULL AND w.completed_at <= " + timeBound.asOfExpression() + ")"
	}
	statement := withRowLimit(`SELECT w.work_item_id, ` + completedExpression + `, ifNull(w.completed_at, toDateTime64(0, 6, 'UTC'))
FROM work_items AS w FINAL
WHERE w.org_id = {org_id:String} AND w.work_item_id IN {ids:Array(String)}` + timeBound.existencePredicate("w.created_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var id string
		var isCompleted uint8
		var completedAt time.Time
		if err := row.Scan(&id, &isCompleted, &completedAt); err != nil {
			return err
		}
		subject, ok := bySubject[id]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"completed": contextfabric.BooleanFactValue(isCompleted != 0)}
		if isCompleted != 0 {
			fields["completed_at"] = contextfabric.StringFactValue(completedAt.UTC().Format(time.RFC3339))
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactActualCompletion, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("work-item", id)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item actual completion", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
