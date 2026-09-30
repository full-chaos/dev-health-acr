package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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

// The counterpart's canonical work item id (CHAOS-7120). A dependency row
// names the OTHER work item by its bare work_item_id, which carries no
// repository and is not unique across repositories, so the direct read gate
// cannot decide it. Both providers therefore also resolve the counterpart's
// repository and emit its canonical "work_item.v2:<repo_id>:<work_item_id>"
// id beside the bare one (blocked_by_work_item_ref /
// required_child_work_item_ref), which the gate decides exactly like a work
// item root. The bare-id field is kept unchanged, so the engine and any
// stored answer read the same value as before; the direct read tool declares
// it opaque.
//
// The id is emitted only when the bare id resolves to exactly ONE
// repository in the organization: with two, which one the row means is not
// knowable from the row, and guessing would name a subject the row may not
// mean. The LEFT JOIN's unmatched default (count 0) also emits nothing.
const counterpartRepositoryColumnsSQL = `ifNull(c.counterpart_repo_id, ''), toUInt64(ifNull(c.counterpart_repo_count, 0))`

func counterpartRepositoryJoinSQL(counterpartColumn string) string {
	return `LEFT JOIN (SELECT work_item_id, toString(any(repo_id)) AS counterpart_repo_id, uniqExact(repo_id) AS counterpart_repo_count
  FROM work_items FINAL WHERE org_id = {org_id:String} GROUP BY work_item_id) AS c ON c.work_item_id = ` + counterpartColumn
}

// counterpartWorkItemRef returns the counterpart's canonical work item id,
// or false when the bare id does not resolve to exactly one repository or
// the id cannot be derived.
func counterpartWorkItemRef(repoID string, repoCount uint64, workItemID string) (string, bool) {
	if repoCount != 1 || repoID == "" || workItemID == "" {
		return "", false
	}
	canonical, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, workItemID}, nil)
	if err != nil || omitted {
		return "", false
	}
	return canonical, true
}

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

func (p *BlockersProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.blockers", contextfabric.FactBlockers, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-5438: ONE owner -- see factBudget, and WorkProvider's identical
	// note on why a single-branch provider uses it too.
	budget := newFactBudget()
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
	// CHAOS-5438: the probe limit, not the output limit -- see
	// maxFactRowsProbe. Dependency cardinality can exceed subject
	// cardinality (one work item can have thousands of blockers), so this
	// provider is exactly the shape the row bound was introduced for, and
	// exactly the shape where reporting a full page as truncated is wrong.
	//
	// BOTH ENDS pass the shared work-item authorization rule: the blocked
	// subject and the blocking item it names. A blocker is itself a work
	// item's identity, so an authorized subject must never carry the id of
	// one the principal may not see.
	authorized, authorizationBindings := workItemAuthorizedIDsSQL(workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope))
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", settingsErr)
	}
	statement := readers.WithSettings(withRowProbeLimit(`SELECT d.source_work_item_id, d.target_work_item_id, toString(t.repo_id), `+counterpartRepositoryColumnsSQL+`
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS t FINAL ON t.org_id = d.org_id AND t.work_item_id = d.target_work_item_id
`+counterpartRepositoryJoinSQL("d.source_work_item_id")+`
WHERE d.org_id = {org_id:String} AND concat(toString(t.repo_id), ':', d.target_work_item_id) IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) = '`+blockerRelationshipType+`'
  AND d.target_work_item_id IN `+authorized+` AND d.source_work_item_id IN `+authorized+`
ORDER BY toString(t.repo_id), d.target_work_item_id, d.source_work_item_id
LIMIT 1 BY toString(t.repo_id), d.target_work_item_id, d.source_work_item_id`), settings)
	rowCount := 0
	seenBlockers := map[string]struct{}{}
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemBlockers", statement, orgID, ids, func(row readers.RowScanner) error {
		var sourceID, targetID, targetRepoID, sourceRepoID string
		var sourceRepoCount uint64
		if err := row.Scan(&sourceID, &targetID, &targetRepoID, &sourceRepoID, &sourceRepoCount); err != nil {
			return err
		}
		subject, ok := bySubject[targetRepoID+":"+targetID]
		if !ok {
			return nil
		}
		// CHAOS-7177: the predicate above is case-insensitive, and the table key
		// carries the raw type, so 'blocks' and 'BLOCKS' rows for one pair both
		// survive FINAL. One pair blocks once.
		blockKey := targetRepoID + "\x00" + targetID + "\x00" + sourceID
		if _, dup := seenBlockers[blockKey]; dup {
			return nil
		}
		seenBlockers[blockKey] = struct{}{}
		rowCount++
		if !budget.admit() {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"blocked_by_work_item_id": contextfabric.StringFactValue(sourceID)}
		if ref, ok := counterpartWorkItemRef(sourceRepoID, sourceRepoCount, sourceID); ok {
			fields["blocked_by_work_item_ref"] = contextfabric.StringFactValue(ref)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactBlockers, Subject: subject,
			Fields:         fields,
			EvidenceRefIDs: []string{dependencyEvidenceRefID(sourceID, targetID, dependencyrelation.Key(blockerRelationshipType))},
		})
		return nil
	}, authorizationBindings...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item blockers", scanErr)
	}
	budget.observe(rowCount)
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
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

func (p *RequiredChildrenProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject, rejected := v2Index(query.Subjects, identity.KindWorkItem)
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.required_children", contextfabric.FactRequiredChildren, rejected)
		}
	}()
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-5438: ONE owner -- see factBudget, and WorkProvider's identical
	// note on why a single-branch provider uses it too.
	budget := newFactBudget()
	// See BlockersProvider's doc comment on the same JOIN: work_item_dependencies
	// has no repo_id of its own, so the source's repo_id is resolved via
	// work_items the same way devhealthsource's own producer does.
	// CHAOS-5438: probe limit, output bound separate -- see the same note
	// on BlockersProvider above.
	//
	// BOTH ENDS pass the shared work-item authorization rule, as on
	// BlockersProvider: the subject and the required child it names.
	authorized, authorizationBindings := workItemAuthorizedIDsSQL(workItemRepositoryAuthorization(principal, query.RequestedRepositoryScope))
	settings, settingsErr := workItemReaderSettings(ctx)
	if settingsErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", settingsErr)
	}
	statement := readers.WithSettings(withRowProbeLimit(`SELECT d.source_work_item_id, d.target_work_item_id, ifNull(d.relationship_type, ''), toString(s.repo_id), `+counterpartRepositoryColumnsSQL+`
FROM work_item_dependencies AS d FINAL
INNER JOIN work_items AS s FINAL ON s.org_id = d.org_id AND s.work_item_id = d.source_work_item_id
`+counterpartRepositoryJoinSQL("d.target_work_item_id")+`
WHERE d.org_id = {org_id:String} AND concat(toString(s.repo_id), ':', d.source_work_item_id) IN {ids:Array(String)} AND lower(ifNull(d.relationship_type, '')) != '`+blockerRelationshipType+`'
  AND d.source_work_item_id IN `+authorized+` AND d.target_work_item_id IN `+authorized+`
ORDER BY toString(s.repo_id), d.source_work_item_id, d.target_work_item_id, `+dependencyrelation.KeySQL("d.relationship_type")+`, lower(ifNull(d.relationship_type, ''))
LIMIT 1 BY toString(s.repo_id), d.source_work_item_id, d.target_work_item_id, `+dependencyrelation.KeySQL("d.relationship_type")), settings)
	rowCount := 0
	seenRelations := map[string]struct{}{}
	scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadWorkItemRequiredChildren", statement, orgID, ids, func(row readers.RowScanner) error {
		var sourceID, targetID, relationshipType, sourceRepoID, targetRepoID string
		var targetRepoCount uint64
		if err := row.Scan(&sourceID, &targetID, &relationshipType, &sourceRepoID, &targetRepoID, &targetRepoCount); err != nil {
			return err
		}
		subject, ok := bySubject[sourceRepoID+":"+sourceID]
		if !ok {
			return nil
		}
		// CHAOS-7177: resolve the row's type through the projector's alias
		// table so a stale 'relates' row and a live 'relates_to' row for one
		// pair yield ONE fact. Dedupe before admit(): a twin must not spend
		// output budget.
		relationKey := dependencyrelation.Key(relationshipType)
		emittedType := dependencyrelation.EmittedSpelling(relationshipType)
		// Empty and whitespace-only types share one key too (':fwd').
		dedupeKey := sourceRepoID + "\x00" + sourceID + "\x00" + targetID + "\x00" + relationKey
		if _, dup := seenRelations[dedupeKey]; dup {
			return nil
		}
		seenRelations[dedupeKey] = struct{}{}
		// Count DISTINCT relations only: the SQL already collapses twins before
		// the probe limit, and a Go-side twin must not read as truncation.
		rowCount++
		if !budget.admit() {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"required_child_work_item_id": contextfabric.StringFactValue(targetID)}
		if ref, ok := counterpartWorkItemRef(targetRepoID, targetRepoCount, targetID); ok {
			fields["required_child_work_item_ref"] = contextfabric.StringFactValue(ref)
		}
		if emittedType != "" {
			fields["relationship_type"] = contextfabric.StringFactValue(emittedType)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactRequiredChildren, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{dependencyEvidenceRefID(sourceID, targetID, relationKey)},
		})
		return nil
	}, authorizationBindings...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query work item required children", scanErr)
	}
	budget.observe(rowCount)
	state, emptyReason := currentAxisReadState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Truncated: budget.truncated()}
	return result, nil
}

// dependencyEvidenceRefID is the evidence identity of ONE dependency relation:
// (source, target, canonical relation key), each component escaped
// (CHAOS-7252: work item ids and relation keys hold ':', so a bare-':' join
// named two relations). It is the same string the projection mints for the
// relation and the one the source-row statement work_item_dependencies.v2
// derives, so the ref expands to exactly this relation.
func dependencyEvidenceRefID(sourceID, targetID, relationKey string) string {
	ref, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, sourceID, targetID, relationKey)
	return ref
}
