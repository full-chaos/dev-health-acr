package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// organizationPrefix is the CanonicalID prefix devhealthsource/clickhouse.go
// already mints for organization subjects ("organization:" + orgID) when it
// projects the organization entity itself. Reused verbatim here so a
// FactSourceHealth EvidenceRefID resolves to the same, already-projected
// organization evidence ref.
const organizationPrefix = "organization:"

// SourceHealthProvider implements contextfabric.FactProvider for
// FactSourceHealth from backfill_log -- the per-provider ingestion job
// outcome Dev Health Ops records for every backfill/sync run (status,
// duration, items synced, and any error). This is org-scoped, not
// subject-scoped in any finer-grained way: there is no per-repository or
// per-team ingestion-health column, so the only honest subject this
// provider supports is the organization itself, matching the organization
// entity devhealthsource/clickhouse.go already projects into the graph.
type SourceHealthProvider struct{ facts clickhouseFacts }

func newSourceHealthProvider(client contextpacket.ClickHouseQueryClient) *SourceHealthProvider {
	return &SourceHealthProvider{facts: clickhouseFacts{client: client}}
}

func (p *SourceHealthProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactSourceHealth, "devhealthfacts.source_health", []contextfabric.SubjectKind{contextfabric.SubjectOrganization})
}

func (p *SourceHealthProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (contextfabric.FactProviderResult, error) {
	if result, unsupported := checkCurrentTimeOnly(query); unsupported {
		return result, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	// backfill_log has no per-subject key other than org_id itself, so the
	// only requested subjects this provider ever honors are organization
	// subjects whose raw ID equals the caller's own org -- there is nothing
	// else to scope an "ids IN (...)" clause against, and the WHERE
	// org_id = {org_id:String} clause below is itself the whole scope.
	orgSubjectIDs, bySubject := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectOrganization), organizationPrefix)
	if len(orgSubjectIDs) == 0 {
		return contextfabric.FactProviderResult{Facts: nil, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
	}
	subject, requested := bySubject[orgID]
	if !requested {
		// The caller asked about a different organization's subject than
		// principal.OrgID names -- never honor it (org scoping is
		// structural, never caller-supplied).
		return contextfabric.FactProviderResult{Facts: nil, State: contextfabric.SourceAvailable, Version: queryVersion}, nil
	}
	facts := make([]contextfabric.CanonicalFact, 0, maxFactRowsPerQuery)
	// row_number() OVER (PARTITION BY provider ORDER BY created_at DESC)
	// picks each provider's single most recent ingestion job outcome.
	statement := withRowLimit(`SELECT provider, status, items_synced, duration_ms, error_message, toString(created_at)
FROM (
	SELECT provider, status, items_synced, duration_ms, error_message, created_at,
		row_number() OVER (PARTITION BY provider ORDER BY created_at DESC) AS rn
	FROM backfill_log
	WHERE org_id = {org_id:String}
)
WHERE rn = 1`)
	rowCount := 0
	scanErr := p.facts.query(ctx, statement, orgID, orgSubjectIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var provider, status, errorMessage, createdAt string
		var itemsSynced, durationMS int64
		if err := row.Scan(&provider, &status, &itemsSynced, &durationMS, &errorMessage, &createdAt); err != nil {
			return err
		}
		fields := map[string]contextfabric.FactValue{
			"provider":       stringOrNull(provider),
			"status":         stringOrNull(status),
			"items_synced":   contextfabric.IntegerFactValue(itemsSynced),
			"duration_ms":    contextfabric.IntegerFactValue(durationMS),
			"last_synced_at": contextfabric.StringFactValue(createdAt),
		}
		if errorMessage != "" {
			fields["error_message"] = contextfabric.StringFactValue(errorMessage)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactSourceHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("organization", orgID)},
		})
		return nil
	})
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query source health", scanErr)
	}
	return contextfabric.FactProviderResult{Facts: facts, State: contextfabric.SourceAvailable, Version: queryVersion, Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
