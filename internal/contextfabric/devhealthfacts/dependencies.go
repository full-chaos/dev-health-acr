package devhealthfacts

import (
	"context"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// blockerRelationshipType is an internal Go constant, not a caller
	// supplied value, so it is safe to inline as a SQL string literal here
	// rather than a bound parameter.
	//
	// work_item_dependencies carries no repo_id of its own (mirroring
	// devhealthsource/tables.go's queryWorkItemDependencies), so the INNER
	// JOIN to work_items resolves the target's repo_id -- the same repo_id
	// component v2Index decoded out of the subject's own canonical id --
	// letting the WHERE clause scope on the composite key rather than the
	// bare (cross-repo-collidable) target_work_item_id alone.
	statement := withRowLimit(`SELECT d.source_work_item_id, d.target_work_item_id, toString(t.repo_id)
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS t FINAL ON t.org_id = d.org_id AND t.work_item_id = d.target_work_item_id
WHERE d.org_id = {org_id:String} AND concat(toString(t.repo_id), ':', d.target_work_item_id) IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) = '` + blockerRelationshipType + `'`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var sourceID, targetID, targetRepoID string
		if err := row.Scan(&sourceID, &targetID, &targetRepoID); err != nil {
			return err
		}
		subject, ok := bySubject[targetRepoID+":"+targetID]
		if !ok {
			return nil
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: subject,
			Fields:         map[string]contextfabric.FactValue{"blocked_by_work_item_id": contextfabric.StringFactValue(sourceID)},
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, sourceID+":"+targetID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", scanErr)
	}
	state, emptyReason := currentAxisReadState(len(facts))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: rowCount >= maxFactRowsPerQuery}
	applySubjectShapeRejection(&result, "devhealthfacts.blockers", contextfabric.FactBlockers, rejected)
	return result, nil
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
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// See BlockersProvider's doc comment on the same JOIN: work_item_dependencies
	// has no repo_id of its own, so the source's repo_id is resolved via
	// work_items the same way devhealthsource's own producer does.
	statement := withRowLimit(`SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, ''), toString(s.repo_id)
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS s FINAL ON s.org_id = d.org_id AND s.work_item_id = d.source_work_item_id
WHERE d.org_id = {org_id:String} AND concat(toString(s.repo_id), ':', d.source_work_item_id) IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) != '` + blockerRelationshipType + `'`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var sourceID, targetID, relationshipType, sourceRepoID string
		if err := row.Scan(&sourceID, &targetID, &relationshipType, &sourceRepoID); err != nil {
			return err
		}
		subject, ok := bySubject[sourceRepoID+":"+sourceID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"required_child_work_item_id": contextfabric.StringFactValue(targetID)}
		if relationshipType != "" {
			fields["relationship_type"] = contextfabric.StringFactValue(strings.ToLower(relationshipType))
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactRequiredChildren, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityWorkItemDependency, sourceID+":"+targetID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", scanErr)
	}
	state, emptyReason := currentAxisReadState(len(facts))
	result := contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: rowCount >= maxFactRowsPerQuery}
	applySubjectShapeRejection(&result, "devhealthfacts.required_children", contextfabric.FactRequiredChildren, rejected)
	return result, nil
}
