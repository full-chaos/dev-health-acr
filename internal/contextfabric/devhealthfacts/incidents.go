package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
	// CHAOS-4377: the SQL build + scan half (the status/severity Tier
	// B/Tier C split, the soft-delete guard) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadIncidents; its doc
	// comment carries that reasoning now.
	//
	// The severity omission still surfaces here: a historical read comes
	// back with severity forced to an empty string by the reader, and
	// stringOrNull below turns that into an absent field -- an absent
	// field is unknown, never a guess (§19.8.3, §3.5). The exclusion is
	// named in the provider's own reason (incidentSeverityOmittedReason
	// below) so a reader learns which field went missing and why, rather
	// than silently receiving a thinner fact.
	rows, scanErr := readers.ReadIncidents(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query incidents", scanErr)
	}
	for _, row := range rows {
		subject, ok := bySubject[row.ID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(row.Status)}
		if row.Severity != "" {
			fields["severity"] = contextfabric.StringFactValue(row.Severity)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactIncidents, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("incident", row.ID)},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows))
	result := contextfabric.FactProviderResult{
		Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion,
		Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery,
	}
	// Retention wins over the severity note: with no rows at all there is
	// no fact whose severity could have been omitted, and reporting the
	// omission would point a reader at the wrong limitation.
	if timeBound.active && retentionReason == "" {
		result.Reason = incidentSeverityOmittedReason
	}
	return result, nil
}
