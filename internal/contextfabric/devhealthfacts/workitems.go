package devhealthfacts

import (
	"context"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

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
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-4377: the SQL build + scan half moved to
	// github.com/full-chaos/dev-health-go/readers.ReadWorkItemStatus.
	rows, scanErr := readers.ReadWorkItemStatus(ctx, p.facts.client, orgID, ids)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item status", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ID]
		if !ok {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactStatus, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"status": stringOrNull(row.Status)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
		})
	}
	state, emptyReason := currentAxisReadState(len(facts))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: len(rows) >= maxFactRowsPerQuery}
	applySubjectShapeRejection(&result, "devhealthfacts.status", contextfabric.FactStatus, rejected)
	return result, nil
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
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-4377: the SQL build + scan half moved to
	// github.com/full-chaos/dev-health-go/readers.ReadWorkItemTitle.
	rows, scanErr := readers.ReadWorkItemTitle(ctx, p.facts.client, orgID, ids)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item work descriptors", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ID]
		if !ok {
			continue
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactWork, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"title": stringOrNull(row.Title)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
		})
	}
	state, emptyReason := currentAxisReadState(len(facts))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: len(rows) >= maxFactRowsPerQuery}
	applySubjectShapeRejection(&result, "devhealthfacts.work", contextfabric.FactWork, rejected)
	return result, nil
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
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-4377: the SQL build + scan half (the isNotNull/ifNull
	// coalescing, the Tier B "was it done at T" derivation) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadWorkItemCompletion;
	// its doc comment carries that reasoning now.
	rows, scanErr := readers.ReadWorkItemCompletion(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item actual completion", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.RepoID+":"+row.ID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{"completed": contextfabric.BooleanFactValue(row.IsCompleted != 0)}
		if row.IsCompleted != 0 {
			fields["completed_at"] = contextfabric.StringFactValue(row.CompletedAt.UTC().Format(time.RFC3339))
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactActualCompletion, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItem, row.RepoID+":"+row.ID)},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery}
	applySubjectShapeRejection(&result, "devhealthfacts.actual_completion", contextfabric.FactActualCompletion, rejected)
	return result, nil
}
