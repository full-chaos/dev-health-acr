package devhealthfacts

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// work_item_dependencies has one free-text relationship_type column (see
// devhealthsource/tables.go's queryWorkItemDependencies and its seed fixture
// testdata/fullstack/v1/seed/clickhouse/001_widget_service.sql, whose only
// seeded row uses 'blocks'); there is no published canonical vocabulary for
// every other value the column can hold. blockerRelationshipType is the one
// value this package treats as confirmed: a row where relationship_type is
// this value and target_work_item_id is the subject means the row's source
// work item blocks the subject. Every other relationship_type value sourced
// from the subject is treated as a required-child/dependency edge by
// BlockersProvider's sibling, RequiredChildrenProvider -- see its doc
// comment for why that split, rather than a second hardcoded string, is the
// honest choice here.
const blockerRelationshipType = "blocks"

// BlockersProvider implements contextfabric.FactProvider for FactBlockers:
// for a work item subject, every work_item_dependencies row where that
// subject is the target and relationship_type is blockerRelationshipType --
// i.e. another work item blocking it.
type BlockersProvider struct{ facts clickhouseFacts }

func newBlockersProvider(client contextpacket.ClickHouseQueryClient) *BlockersProvider {
	return &BlockersProvider{facts: clickhouseFacts{client: client}}
}

func (p *BlockersProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactBlockers, "devhealthfacts.blockers", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *BlockersProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := refuseHistoricalFact(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, workItemPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// blockerRelationshipType is an internal Go constant, not a caller
	// supplied value, so it is safe to inline as a SQL string literal here
	// rather than a bound parameter.
	statement := withRowLimit(`SELECT d.source_work_item_id, d.target_work_item_id
FROM work_item_dependencies AS d FINAL
WHERE d.org_id = {org_id:String} AND d.target_work_item_id IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) = '` + blockerRelationshipType + `'`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var sourceID, targetID string
		if err := row.Scan(&sourceID, &targetID); err != nil {
			return err
		}
		subject, ok := bySubject[targetID]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"blocked_by_work_item_id": contextfabric.StringFactValue(sourceID)},
			EvidenceRefIDs: []string{evidenceRefID("work-item-dependency", sourceID+":"+targetID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}

// RequiredChildrenProvider implements contextfabric.FactProvider for
// FactRequiredChildren: for a work item subject, every
// work_item_dependencies row where that subject is the source and
// relationship_type is anything other than blockerRelationshipType -- i.e.
// a dependency this work item requires, as distinct from a blocking
// relationship (which BlockersProvider already reports, from the other
// side, as a blocker of whichever work item is being blocked).
type RequiredChildrenProvider struct{ facts clickhouseFacts }

func newRequiredChildrenProvider(client contextpacket.ClickHouseQueryClient) *RequiredChildrenProvider {
	return &RequiredChildrenProvider{facts: clickhouseFacts{client: client}}
}

func (p *RequiredChildrenProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactRequiredChildren, "devhealthfacts.required_children", []contextfabric.SubjectKind{contextfabric.SubjectWorkItem})
}

func (p *RequiredChildrenProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := refuseHistoricalFact(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, workItemPrefix)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	statement := withRowLimit(`SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, '')
FROM work_item_dependencies AS d FINAL
WHERE d.org_id = {org_id:String} AND d.source_work_item_id IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) != '` + blockerRelationshipType + `'`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var sourceID, targetID, relationshipType string
		if err := row.Scan(&sourceID, &targetID, &relationshipType); err != nil {
			return err
		}
		subject, ok := bySubject[sourceID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"required_child_work_item_id": contextfabric.StringFactValue(targetID)}
		if relationshipType != "" {
			fields["relationship_type"] = contextfabric.StringFactValue(strings.ToLower(relationshipType))
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactRequiredChildren, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("work-item-dependency", sourceID+":"+targetID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
