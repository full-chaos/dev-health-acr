package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// IncidentsProvider implements contextfabric.FactProvider for FactIncidents
// from operational_incidents.normalized_status/normalized_severity (falling
// back to the raw_* columns) -- the same columns and fallback
// devhealthsource/tables.go's queryIncidents already reads. A soft-deleted
// incident (is_deleted = 1, devhealthsource's one confirmed soft-delete
// signal for this table) yields no fact entry for that subject, the same as
// any other zero-row match, rather than reporting stale data.
//
// work_graph_deployment_incident_edges (queried by
// devhealthsource/tables.go's queryDeploymentIncidentEdges) is not joined
// in here: it would only add a deployment linkage, which doesn't fit this
// fact kind's scalar Fields cleanly (an incident can correlate to more than
// one deployment) -- left for a future FactKind or a repeated-fact
// refinement rather than guessing a shape now.
type IncidentsProvider struct{ facts clickhouseFacts }

func newIncidentsProvider(client contextpacket.ClickHouseQueryClient) *IncidentsProvider {
	return &IncidentsProvider{facts: clickhouseFacts{client: client}}
}

func (p *IncidentsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactIncidents, "devhealthfacts.incidents", []contextfabric.SubjectKind{contextfabric.SubjectIncident})
}

func (p *IncidentsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	ids, bySubject := subjectIndex(query.Subjects, "incident:")
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	// CHAOS-3781 Tier B: an incident that had not resolved yet at the
	// requested time was open then, whatever its status reads today.
	// Severity is not derived: it can be revised in place with no
	// recorded history, so the current value is reported unchanged and
	// its limitation rides the answer's overall temporal label rather
	// than being silently presented as the severity at that time.
	statusExpression := "ifNull(i.normalized_status, ifNull(i.raw_status, ''))"
	if timeBound.active {
		statusExpression = "if(i.resolved_at IS NOT NULL AND i.resolved_at <= " + timeBound.asOfExpression() +
			", " + statusExpression + ", 'open')"
	}
	statement := withRowLimit(`SELECT i.id, ` + statusExpression + `, ifNull(i.normalized_severity, ifNull(i.raw_severity, ''))
FROM operational_incidents AS i FINAL
WHERE i.org_id = {org_id:String} AND i.id IN {ids:Array(String)} AND i.is_deleted = 0` + timeBound.existencePredicate("i.started_at"))
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, ids, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var incidentID, status, severity string
		if err := row.Scan(&incidentID, &status, &severity); err != nil {
			return err
		}
		subject, ok := bySubject[incidentID]
		if !ok {
			return nil
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(status)}
		if severity != "" {
			fields["severity"] = contextfabric.StringFactValue(severity)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactIncidents, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("incident", incidentID)},
		})
		return nil
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query incidents", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
