package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
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
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
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
		return contextfabric.FactProviderResult{Facts: nil, State: contextfabric.SourceAvailable, Version: QueryVersion}, nil
	}
	subject, requested := bySubject[orgID]
	if !requested {
		// The caller asked about a different organization's subject than
		// principal.OrgID names -- never honor it (org scoping is
		// structural, never caller-supplied).
		return contextfabric.FactProviderResult{Facts: nil, State: contextfabric.SourceAvailable, Version: QueryVersion}, nil
	}
	facts := make([]contextfabric.CanonicalFact, 0, maxFactRowsPerQuery)
	// CHAOS-4377: the SQL build + scan half (the row_number tiebreak
	// reasoning, the toInt64/raw-uint64 Scan-width reasoning) moved to
	// github.com/full-chaos/dev-health-go/readers.ReadSourceHealth; its
	// doc comment carries that reasoning now.
	rows, scanErr := readers.ReadSourceHealth(ctx, p.facts.client, orgID, orgSubjectIDs, timeBound.neutral())
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query source health", scanErr)
	}
	omittedUnrepresentableCount := 0
	for _, row := range rows {
		// duration_ms is UInt64 and is NOT wrapped with toInt64 in SQL
		// (round-3 F2): the wrap is what silently turned a value above
		// MaxInt64 negative. Scanned raw by the reader and range-checked
		// here instead.
		durationMS, representable := representableInt64(row.DurationMS)
		if !representable {
			omittedUnrepresentableCount++
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"provider":       stringOrNull(row.Provider),
			"status":         stringOrNull(row.Status),
			"items_synced":   contextfabric.IntegerFactValue(row.ItemsSynced),
			"duration_ms":    contextfabric.IntegerFactValue(durationMS),
			"last_synced_at": contextfabric.StringFactValue(row.CreatedAt),
		}
		if row.ErrorMessage != "" {
			fields["error_message"] = contextfabric.StringFactValue(row.ErrorMessage)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactSourceHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID("organization", orgID)},
		})
	}
	state, retentionReason := timeBound.retentionState(len(rows))
	// Round-4 R4-2: the COUNT travels, not just a flag. The registry
	// turns a nonzero count into a truncated/partial result, so an
	// answer can never report complete coverage while rows were dropped.
	if omittedUnrepresentableCount > 0 && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: len(rows) >= maxFactRowsPerQuery, OmittedCount: omittedUnrepresentableCount}, nil
}
