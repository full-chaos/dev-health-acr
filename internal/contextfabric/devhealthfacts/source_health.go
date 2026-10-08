package devhealthfacts

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// organizationPrefix is the CanonicalID prefix devhealthsource/clickhouse.go
// already mints for organization subjects ("organization:" + orgID) when it
// projects the organization entity itself. Reused verbatim here so a
// FactSourceHealth EvidenceRefID resolves to the same, already-projected
// organization evidence ref.
const organizationPrefix = "organization:"

// Closed limitation reasons the provider answers instead of facts.
const (
	sourceHealthRestrictedReason = "org-level fact; caller scope is repository-bound"
	sourceHealthUnsetReason      = "source health read is not configured: no operation runner"
	sourceHealthNotServedReason  = "source health root not served: "
	sourceHealthUnreadableReason = "source health answer could not be read"
	sourceHealthIncompleteReason = "source health answer is incomplete"
)

// SourceHealthProvider implements contextfabric.FactProvider for
// FactSourceHealth from the ops GraphQL root sourceHealth: one fact per sync
// configuration of the caller's organization (provider, closed scope code,
// last successful sync, latest failure time and closed stage). The root is
// org-level, so the only honest subject is the organization itself and a
// repository-scoped caller gets a limitation, never rows.
type SourceHealthProvider struct{ operations *OperationHolder }

func newSourceHealthProvider(operations *OperationHolder) *SourceHealthProvider {
	return &SourceHealthProvider{operations: operations}
}

func (p *SourceHealthProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactSourceHealth, "devhealthfacts.source_health", []contextfabric.SubjectKind{contextfabric.SubjectOrganization})
}

type sourceHealthFailure struct {
	OccurredAt string `json:"occurredAt"`
	Stage      string `json:"stage"`
}

type sourceHealthRow struct {
	Provider    string               `json:"provider"`
	Scope       string               `json:"scope"`
	LastSyncAt  *string              `json:"lastSyncAt"`
	LastFailure *sourceHealthFailure `json:"lastFailure"`
}

type sourceHealthData struct {
	SourceHealth *[]sourceHealthRow `json:"sourceHealth"`
}

func sourceHealthRestricted(principal storage.Principal) bool {
	return len(principal.RepositoryScopes) > 0 && !slices.ContainsFunc(principal.RepositoryScopes, func(scope string) bool { return strings.TrimSpace(scope) == "*" })
}

func (p *SourceHealthProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	// The root has no history: any non-current axis is not applicable.
	if refused, unsupported := refuseHistoricalFact(query); unsupported {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	orgSubjectIDs, bySubject, rejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectOrganization), organizationPrefix)
	// ONE result variable and a deferred disclosure so no branch can skip the
	// shape-rejection note (same construction as every provider here).
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.source_health", contextfabric.FactSourceHealth, rejected)
		}
	}()
	result = contextfabric.FactProviderResult{Facts: nil, State: contextfabric.SourceAvailable, Version: QueryVersion}
	subject, requested := bySubject[orgID]
	switch {
	case len(orgSubjectIDs) == 0:
		// Nothing asked, or every organization subject was rejected for shape.
	case !requested:
		// A different organization than the caller's: never honored.
	case sourceHealthRestricted(principal):
		result = limitation(contextfabric.SourceNotApplicable, sourceHealthRestrictedReason)
	default:
		result = p.read(ctx, principal, orgID, subject)
	}
	return result, nil
}

func limitation(state contextfabric.SourceState, reason string) contextfabric.FactProviderResult {
	return contextfabric.FactProviderResult{Facts: nil, State: state, Reason: reason, Version: QueryVersion}
}

func (p *SourceHealthProvider) read(ctx context.Context, principal storage.Principal, orgID string, subject contextfabric.SubjectRef) contextfabric.FactProviderResult {
	if p == nil || !p.operations.IsSet() {
		return limitation(contextfabric.SourceUnconfigured, sourceHealthUnsetReason)
	}
	outcome, callErr := p.operations.CallOperation(ctx, principal, SourceHealthOperationName)
	if callErr != nil {
		return limitation(contextfabric.SourceUnavailable, sourceHealthNotServedReason+"call_failed")
	}
	if !outcome.Served {
		reason := outcome.Reason
		if reason == "" {
			reason = "unknown"
		}
		return limitation(contextfabric.SourceUnavailable, sourceHealthNotServedReason+reason)
	}
	var data sourceHealthData
	if err := json.Unmarshal(outcome.Data, &data); err != nil {
		return limitation(contextfabric.SourceUnavailable, sourceHealthUnreadableReason)
	}
	if data.SourceHealth == nil {
		return limitation(contextfabric.SourceUnavailable, sourceHealthUnreadableReason)
	}
	rows := *data.SourceHealth
	facts := make([]contextfabric.CanonicalFact, 0, len(rows))
	for _, row := range rows {
		fields := map[string]contextfabric.FactValue{
			"provider":                 contextfabric.StringFactValue(row.Provider),
			"scope":                    contextfabric.StringFactValue(row.Scope),
			"last_sync_at":             nullableString(row.LastSyncAt),
			"last_failure_occurred_at": contextfabric.NullFactValue(),
			"last_failure_stage":       contextfabric.NullFactValue(),
		}
		if row.LastFailure != nil {
			fields["last_failure_occurred_at"] = stringOrNull(row.LastFailure.OccurredAt)
			fields["last_failure_stage"] = stringOrNull(row.LastFailure.Stage)
		}
		facts = append(facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactSourceHealth, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityOrganization, orgID)},
		})
	}
	state := contextfabric.SourceAvailable
	reason := ""
	switch {
	case len(facts) == 0:
		state = contextfabric.SourceNoData
	case !outcome.Complete:
		state, reason = contextfabric.SourceTruncated, sourceHealthIncompleteReason
	}
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: reason, Version: QueryVersion, Truncated: state == contextfabric.SourceTruncated}
}

func nullableString(value *string) contextfabric.FactValue {
	if value == nil {
		return contextfabric.NullFactValue()
	}
	return stringOrNull(*value)
}
