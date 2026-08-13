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
	// created_at DESC alone is not a TOTAL order (Codex round-2 finding
	// M1) -- two backfill jobs for the same provider could start in the
	// same second. backfill_log's real sorting key is
	// (org_id, job_id, chunk_index) -- job_id alone is NOT per-row unique,
	// a single backfill job is chunked into several rows sharing one
	// job_id (Codex round-3 correction) -- so this provider tiebreaks on
	// job_id, then chunk_index, both real columns, no value hash needed.
	// toInt64 on items_synced (UInt32) and duration_ms (UInt64): the
	// clickhouse-go driver rejects scanning either width into an *int64
	// destination, the same class of defect CHAOS-3789 fixed for
	// git_pull_requests.number and CHAOS-3781 round-2 F1 fixed for this
	// package's pull-request reader. Found by the schema parity guard
	// rather than by a fixture, because this package's fixtures modeled
	// both columns as int64.
	//
	// Converted in SQL rather than by widening the Go destinations, so the
	// scan shape stays independent of each column's exact source width --
	// the same convention every other numeric projection in this package
	// already uses (toInt64(commits_count), toInt64(backlog_size)).
	statement := withRowLimit(`SELECT provider, status, toInt64(items_synced), duration_ms, error_message, toString(created_at)
FROM (
	SELECT provider, status, items_synced, duration_ms, error_message, created_at,
		row_number() OVER (PARTITION BY provider ORDER BY created_at DESC, job_id DESC, chunk_index DESC) AS rn
	FROM backfill_log
	WHERE org_id = {org_id:String}` + timeBound.timestampPredicate("created_at") + `
)
WHERE rn = 1`)
	rowCount := 0
	omittedUnrepresentable := false
	scanErr := p.facts.query(ctx, statement, orgID, orgSubjectIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var provider, status, errorMessage, createdAt string
		var itemsSynced int64
		// duration_ms is UInt64 and is NOT wrapped with toInt64 in SQL
		// (round-3 F2): the wrap is what silently turned a value above
		// MaxInt64 negative. Scanned raw and range-checked here instead.
		var rawDurationMS uint64
		if err := row.Scan(&provider, &status, &itemsSynced, &rawDurationMS, &errorMessage, &createdAt); err != nil {
			return err
		}
		durationMS, representable := representableInt64(rawDurationMS)
		if !representable {
			omittedUnrepresentable = true
			return nil
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
	}, timeBound.bindings()...)
	if scanErr != nil {
		return contextfabric.FactProviderResult{}, readFailure("query source health", scanErr)
	}
	state, retentionReason := timeBound.retentionState(rowCount)
	if omittedUnrepresentable && retentionReason == "" {
		retentionReason = unrepresentableValueReason
	}
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: queryVersion, Grain: timeBound.effectiveGrain(grainExact), Truncated: rowCount >= maxFactRowsPerQuery}, nil
}
