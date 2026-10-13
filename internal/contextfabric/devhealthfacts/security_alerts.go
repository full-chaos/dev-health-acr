package devhealthfacts

import (
	"context"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

const (
	securityBasisOpenOnly      = "open_alerts_only"
	securityBasisNoRows        = "no_alerts_stored_collection_state_unknown"
	securityBasisNotCollected  = "not_collected"
	securityAlertBasisText     = "Counts cover stored alerts in an open state (open, detected, confirmed) as of the last sync. Repository security advisories carry no open or closed state in this sync, so they are counted apart as advisory_stored_count and not in open totals. Closed, fixed, resolved and dismissed alerts are not counted: the sync stores open alerts only, so a closed count is not collected, never zero."
	securityClosedNotCollected = "not_collected"
	securityAdvisorySource     = "advisory"
	securityGroupRowLimit      = 1000
	securityOpenStatesSQL      = "('open', 'detected', 'confirmed')"
)

// securitySourcesByProvider names the stored alert sources each provider's
// sync writes. A provider absent here has no security source.
var securitySourcesByProvider = map[string][]string{
	"github": {"dependabot", "code_scanning", "advisory"},
	"gitlab": {"gitlab_vulnerability", "gitlab_dependency"},
}

// securitySeverities are the severity labels with a field of their own; any
// other label, or none, is counted under open_other_severity_count.
var securitySeverities = []string{"critical", "high", "medium", "low"}

// SecurityAlertsProvider implements contextfabric.FactProvider for
// FactSecurityAlerts from security_alerts: raw counts of stored open alerts
// per repository, by source and by severity. It never scores, and it serves
// no alert title, URL or package name.
type SecurityAlertsProvider struct{ facts clickhouseFacts }

func newSecurityAlertsProvider(client contextpacket.ClickHouseQueryClient) *SecurityAlertsProvider {
	return &SecurityAlertsProvider{facts: clickhouseFacts{client: client}}
}

func (p *SecurityAlertsProvider) Capability() contextfabric.FactCapability {
	return newCapability(contextfabric.FactSecurityAlerts, "devhealthfacts.security_alerts", []contextfabric.SubjectKind{
		contextfabric.SubjectRepository,
	})
}

type securityAlertGroup struct {
	source, severity string
	open, stored     int64
	oldest, newest   time.Time
}

type securityAlertRepo struct {
	rowsStored int64
	groups     []securityAlertGroup
	lastSynced time.Time
}

func (p *SecurityAlertsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	if refused, ok := refuseHistoricalFact(query); ok {
		return refused, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	rejectedCount := 0
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.security_alerts", contextfabric.FactSecurityAlerts, rejectedCount)
		}
	}()

	ids, bySubject, rejected := subjectIndex(subjectsOfKind(query.Subjects, contextfabric.SubjectRepository), repositoryPrefix)
	rejectedCount += rejected
	facts := make([]contextfabric.CanonicalFact, 0, len(ids))
	truncated := false
	if len(ids) > 0 {
		identities, readErr := readers.ReadRepositoryIdentityWithRowLimit(ctx, p.facts.client, orgID, ids, maxFactRowsProbe)
		if readErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query repository provider", readErr)
		}
		providerByRepo := make(map[string]string, len(identities))
		var collected []string
		for _, identityRow := range identities {
			provider := strings.ToLower(identityRow.Provider)
			providerByRepo[identityRow.ID] = provider
			if _, ok := securitySourcesByProvider[provider]; ok {
				collected = append(collected, identityRow.ID)
			}
		}
		stored, rowCount, readErr := p.readStoredAlerts(ctx, orgID, collected)
		if readErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query security alerts", readErr)
		}
		truncated = rowCount > securityGroupRowLimit
		budget := newFactBudget()
		for _, identityRow := range identities {
			subject, ok := bySubject[identityRow.ID]
			if !ok || !budget.admit() {
				continue
			}
			facts = append(facts, contextfabric.CanonicalFact{
				Kind: contextfabric.FactSecurityAlerts, Subject: subject,
				Fields:         p.alertFields(providerByRepo[identityRow.ID], stored[identityRow.ID]),
				EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, identityRow.ID)},
			})
		}
		budget.observe(len(identities))
		truncated = truncated || budget.truncated()
	}
	state, emptyReason := currentAxisReadState(len(facts))
	return contextfabric.FactProviderResult{Facts: facts, State: state, Reason: emptyReason, Version: QueryVersion, Grain: grainExact, Truncated: truncated}, nil
}

// readStoredAlerts returns, per repository, the open-state alert counts
// grouped by source and severity, read from the newest stored version of
// each alert.
func (p *SecurityAlertsProvider) readStoredAlerts(ctx context.Context, orgID string, repoIDs []string) (map[string]*securityAlertRepo, int, error) {
	out := map[string]*securityAlertRepo{}
	if len(repoIDs) == 0 {
		return out, 0, nil
	}
	statement := withRowLimitOf(`SELECT toString(repo_id), latest_source, ifNull(latest_severity, ''),
	toInt64(countIf(lower(ifNull(latest_state, '')) IN `+securityOpenStatesSQL+`)),
	toInt64(count()),
	minIf(latest_created_at, lower(ifNull(latest_state, '')) IN `+securityOpenStatesSQL+`),
	maxIf(latest_created_at, lower(ifNull(latest_state, '')) IN `+securityOpenStatesSQL+`),
	max(latest_synced)
FROM (
	SELECT repo_id, alert_id,
		argMax(source, last_synced) AS latest_source,
		argMax(ifNull(severity, ''), last_synced) AS latest_severity,
		argMax(ifNull(state, ''), last_synced) AS latest_state,
		argMax(created_at, last_synced) AS latest_created_at,
		max(last_synced) AS latest_synced
	FROM security_alerts
	WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
	GROUP BY repo_id, alert_id
)
GROUP BY repo_id, latest_source, latest_severity
ORDER BY repo_id, latest_source, latest_severity`, securityGroupRowLimit+1)
	rowCount := 0
	err := p.facts.query(ctx, statement, orgID, repoIDs, func(row contextpacket.ClickHouseRowScanner) error {
		rowCount++
		var repoID string
		var g securityAlertGroup
		var lastSynced time.Time
		if err := row.Scan(&repoID, &g.source, &g.severity, &g.open, &g.stored, &g.oldest, &g.newest, &lastSynced); err != nil {
			return err
		}
		if rowCount > securityGroupRowLimit {
			return nil
		}
		repo := out[repoID]
		if repo == nil {
			repo = &securityAlertRepo{}
			out[repoID] = repo
		}
		repo.rowsStored += g.stored
		if lastSynced.After(repo.lastSynced) {
			repo.lastSynced = lastSynced
		}
		repo.groups = append(repo.groups, g)
		return nil
	})
	return out, rowCount, err
}

func (p *SecurityAlertsProvider) alertFields(provider string, stored *securityAlertRepo) map[string]contextfabric.FactValue {
	fields := map[string]contextfabric.FactValue{
		"provider":              contextfabric.StringFactValue(provider),
		"closed_alert_basis":    contextfabric.StringFactValue(securityClosedNotCollected),
		"secret_scanning_basis": contextfabric.StringFactValue(securityBasisNotCollected),
	}
	sources, collected := securitySourcesByProvider[provider]
	if !collected {
		fields["collection_basis"] = contextfabric.StringFactValue(securityBasisNotCollected)
		return fields
	}
	if stored == nil || stored.rowsStored == 0 {
		fields["collection_basis"] = contextfabric.StringFactValue(securityBasisNoRows)
		return fields
	}
	fields["collection_basis"] = contextfabric.StringFactValue(securityBasisOpenOnly)
	fields["alert_basis"] = contextfabric.StringFactValue(securityAlertBasisText)
	var open int64
	bySource := make(map[string]int64, len(sources))
	bySeverity := make(map[string]int64, len(securitySeverities)+1)
	var oldest, newest time.Time
	var advisoryStored int64
	for _, g := range stored.groups {
		if g.source == securityAdvisorySource {
			advisoryStored += g.stored
			continue
		}
		open += g.open
		bySource[g.source] += g.open
		if known := containsString(securitySeverities, strings.ToLower(g.severity)); known {
			bySeverity[strings.ToLower(g.severity)] += g.open
		} else {
			bySeverity["other"] += g.open
		}
		if g.open == 0 {
			continue
		}
		if oldest.IsZero() || g.oldest.Before(oldest) {
			oldest = g.oldest
		}
		if newest.IsZero() || g.newest.After(newest) {
			newest = g.newest
		}
	}
	fields["open_alert_count"] = contextfabric.IntegerFactValue(open)
	for _, source := range sources {
		if source == securityAdvisorySource {
			fields["advisory_stored_count"] = contextfabric.IntegerFactValue(advisoryStored)
			continue
		}
		fields["open_"+source+"_count"] = contextfabric.IntegerFactValue(bySource[source])
	}
	for _, severity := range securitySeverities {
		fields["open_"+severity+"_count"] = contextfabric.IntegerFactValue(bySeverity[severity])
	}
	fields["open_other_severity_count"] = contextfabric.IntegerFactValue(bySeverity["other"])
	if open > 0 {
		fields["oldest_open_created_at"] = contextfabric.StringFactValue(oldest.UTC().Format(time.RFC3339))
		fields["newest_open_created_at"] = contextfabric.StringFactValue(newest.UTC().Format(time.RFC3339))
	}
	fields["latest_synced_at"] = contextfabric.StringFactValue(stored.lastSynced.UTC().Format(time.RFC3339))
	return fields
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
