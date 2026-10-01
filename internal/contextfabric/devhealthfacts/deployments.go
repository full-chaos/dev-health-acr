package devhealthfacts

import (
	"time"

	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/readers"
)

// DeploymentsProvider implements contextfabric.FactProvider for
// FactDeployments.
//
// CHAOS-3780 shipped deployment-only, reading deployments.status/environment
// -- the same columns devhealthsource/tables.go's queryDeployments already
// reads.
//
// CHAOS-4347 adds a SECOND, repository-scoped shape reading
// deploy_metrics_daily -- Dev Health Ops' own precomputed daily deployment
// rollup (deployments_count, failed_deployments_count, and nullable
// duration percentiles). This is the same "widen by a real table, not a
// proxy" shape ContinuousIntegrationProvider's own CHAOS-4347 widening
// documents, for the identical reason: deploy_metrics_daily is keyed by
// repository, one row per repo per day, a different granularity from the
// per-deployment status/environment shape above -- so it rides under a
// DISTINCT field set on the SAME FactDeployments kind rather than
// colliding with it.
type DeploymentsProvider struct{ facts clickhouseFacts }

func newDeploymentsProvider(client contextpacket.ClickHouseQueryClient) *DeploymentsProvider {
	return &DeploymentsProvider{facts: clickhouseFacts{client: client}}
}

func (p *DeploymentsProvider) Capability() contextfabric.FactCapability {
	capability := newCapability(contextfabric.FactDeployments, "devhealthfacts.deployments", []contextfabric.SubjectKind{
		contextfabric.SubjectDeployment, contextfabric.SubjectRepository, contextfabric.SubjectTeam,
	})
	// A team's rollup carries the owned-repository pointer and the
	// per-repository breakdown, both breakdown tables.
	capability.Tables = map[contextfabric.SubjectKind][]contextfabric.FactTableShape{
		contextfabric.SubjectTeam: {contextfabric.FactTableBreakdown},
	}
	return capability
}

func (p *DeploymentsProvider) ReadFacts(ctx context.Context, principal storage.Principal, query contextfabric.FactQuery) (result contextfabric.FactProviderResult, err error) {
	timeBound, unsupportedResult, unsupported := resolveTimeBound(query)
	if unsupported {
		return unsupportedResult, nil
	}
	orgID, err := requireOrgID(principal.OrgID)
	if err != nil {
		return contextfabric.FactProviderResult{}, err
	}
	facts := make([]contextfabric.CanonicalFact, 0, len(query.Subjects))
	truncated := false
	rejectedCount := 0
	// CHAOS-5026: deferred so every return path passes through the
	// disclosure -- see ci.go's identical note.
	defer func() {
		if err == nil {
			applySubjectShapeRejection(&result, "devhealthfacts.deployments", contextfabric.FactDeployments, rejectedCount)
		}
	}()
	// See ci.go's identical comment: the coarser grain wins only once a
	// repository aggregate ACTUALLY CONTRIBUTED a fact, not merely because
	// one was attempted.
	grain := grainExact

	if deploymentSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectDeployment); len(deploymentSubjects) > 0 {
		rowCount, rejected, scanErr := p.readDeploymentStatus(ctx, orgID, deploymentSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query deployments", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		rejectedCount += rejected
	}

	if repoSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectRepository); len(repoSubjects) > 0 {
		rowCount, rejected, scanErr := p.readRepositoryAggregate(ctx, orgID, repoSubjects, &facts, timeBound)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query deployment metrics", scanErr)
		}
		truncated = truncated || rowCount >= maxFactRowsPerQuery
		rejectedCount += rejected
		if rowCount > 0 {
			grain = grainDaily
		}
	}

	teamsWithoutRepos := 0
	if teamSubjects := subjectsOfKind(query.Subjects, contextfabric.SubjectTeam); len(teamSubjects) > 0 {
		outcome, scanErr := p.readTeamRollup(ctx, orgID, teamSubjects, &facts, timeBound, query.Time.EvidenceWindow)
		if scanErr != nil {
			return contextfabric.FactProviderResult{}, readFailure("query team deployments", scanErr)
		}
		truncated = truncated || outcome.truncated
		rejectedCount += outcome.rejected
		teamsWithoutRepos += outcome.teamsWithoutRepos
		if outcome.contributed > 0 {
			grain = grainDaily
		}
	}

	state, retentionReason := timeBound.retentionState(len(facts))
	result = contextfabric.FactProviderResult{Facts: facts, State: state, Reason: retentionReason, Version: QueryVersion, Grain: timeBound.effectiveGrain(grain), Truncated: truncated}
	applyNoOwnedRepositories(&result, teamsWithoutRepos)
	return result, nil
}

// readDeploymentStatus is CHAOS-3780's original deployments read. The
// SQL/scan half now lives in readers.ReadDeploymentStatus (CHAOS-4377); see
// that function's doc comment for the CHAOS-3781 Tier B status-derivation
// reasoning this method used to carry inline.
func (p *DeploymentsProvider) readDeploymentStatus(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject, rejected := v2Index(subjects, identity.KindDeployment)
	if len(ids) == 0 {
		return 0, rejected, nil
	}
	rows, err := readers.ReadDeploymentStatus(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, rejected, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID+":"+r.DeploymentID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{"status": stringOrNull(r.Status)}
		if r.Environment != "" {
			fields["environment"] = contextfabric.StringFactValue(r.Environment)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityDeployment, r.RepoID+":"+r.DeploymentID)},
		})
	}
	return len(rows), rejected, nil
}

// readRepositoryAggregate reads deploy_metrics_daily (latest day per
// repository) -- CHAOS-4347's repository-scoped deployment aggregate. The
// SQL/scan half, including the row_number()/cityHash64 tiebreak reasoning,
// now lives in readers.ReadDeployMetricsDaily (CHAOS-4377).
func (p *DeploymentsProvider) readRepositoryAggregate(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound) (int, int, error) {
	ids, bySubject, rejected := subjectIndex(subjects, repositoryPrefix)
	if len(ids) == 0 {
		return 0, rejected, nil
	}
	rows, err := readers.ReadDeployMetricsDaily(ctx, p.facts.client, orgID, ids, timeBound.neutral())
	if err != nil {
		return 0, rejected, err
	}
	for _, r := range rows {
		subject, ok := bySubject[r.RepoID]
		if !ok {
			continue
		}
		fields := map[string]contextfabric.FactValue{
			"day":                      contextfabric.StringFactValue(r.Day),
			"deployments_count":        contextfabric.IntegerFactValue(r.DeploymentsCount),
			"failed_deployments_count": contextfabric.IntegerFactValue(r.FailedDeploymentsCount),
		}
		if r.HasDeployTime {
			fields["deploy_time_p50_hours"] = contextfabric.NumberFactValue(r.DeployTime)
		}
		if r.HasLeadTime {
			fields["lead_time_p50_hours"] = contextfabric.NumberFactValue(r.LeadTime)
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityRepository, r.RepoID)},
		})
	}
	return len(rows), rejected, nil
}

// deploymentRollupRepo is one owned repository's deploy_metrics_daily rows
// inside the rollup window: totals over the window's days, plus the latest
// day's own percentiles (never summed or averaged across days or repositories).
type deploymentRollupRepo struct {
	deployments, failed, days int64
	latestDay                 string
	hasDeployTime, hasLead    bool
	deployTime, leadTime      float64
}

// readTeamRollup serves a team subject: deployment totals over the request
// window, summed across the team's owned repositories, with the per-repository
// breakdown and the owned_repositories pointer. A repository with no daily row
// inside the window is counted as without data, never as zero.
func (p *DeploymentsProvider) readTeamRollup(ctx context.Context, orgID string, subjects []contextfabric.SubjectRef, facts *[]contextfabric.CanonicalFact, timeBound factTimeBound, evidence *contractsv1.ContextFabricRequestedEvidenceWindow) (outcome teamRollupOutcome, err error) {
	teamIDs, bySubject, rejected := subjectIndex(subjects, teamPrefix)
	outcome.rejected = rejected
	if len(teamIDs) == 0 {
		return outcome, nil
	}
	owned, err := teamOwnedRepositories(ctx, p.facts.client, orgID, teamIDs, timeBound)
	if err != nil {
		return outcome, err
	}
	window := resolveRollupWindow(timeBound, evidence, time.Now())
	byRepo := map[string]deploymentRollupRepo{}
	if repoKeys := repoKeysOf(owned); len(repoKeys) > 0 {
		statement := `SELECT toString(repo_id), toInt64(win_deployments), toInt64(win_failed), toInt64(win_days), toString(day), toUInt8(isNotNull(deploy_time)), toFloat64(ifNull(deploy_time, 0)), toUInt8(isNotNull(lead_time)), toFloat64(ifNull(lead_time, 0))
FROM (
	SELECT repo_id, day, deploy_time, lead_time,
		sum(deployments_count) OVER (PARTITION BY repo_id) AS win_deployments,
		sum(failed_deployments_count) OVER (PARTITION BY repo_id) AS win_failed,
		count() OVER (PARTITION BY repo_id) AS win_days,
		row_number() OVER (PARTITION BY repo_id ORDER BY day DESC) AS latest
	FROM (
		SELECT repo_id, day, deployments_count, failed_deployments_count, deploy_time_p50_hours AS deploy_time, lead_time_p50_hours AS lead_time,
			row_number() OVER (PARTITION BY repo_id, day ORDER BY computed_at DESC, cityHash64(tuple(deployments_count, failed_deployments_count, ifNull(deploy_time_p50_hours, -1), ifNull(lead_time_p50_hours, -1))) DESC) AS rn
		FROM deploy_metrics_daily
		WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)} AND ` + window.dayExpr("day") + `
	)
	WHERE rn = 1
)
WHERE latest = 1
ORDER BY repo_id`
		if scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadTeamDeploymentRollup", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
			var repoID string
			var r deploymentRollupRepo
			var hasDeploy, hasLead uint8
			if err := row.Scan(&repoID, &r.deployments, &r.failed, &r.days, &r.latestDay, &hasDeploy, &r.deployTime, &hasLead, &r.leadTime); err != nil {
				return err
			}
			r.hasDeployTime, r.hasLead = hasDeploy != 0, hasLead != 0
			byRepo[repoID] = r
			return nil
		}, window.bindings()...); scanErr != nil {
			return outcome, scanErr
		}
	}
	for _, teamID := range teamIDs {
		subject := bySubject[teamID]
		repos := owned[teamID]
		if len(repos) == 0 {
			outcome.teamsWithoutRepos++
			continue
		}
		var total, failed, withData int64
		breakdown := make([]contextfabric.FactValueRow, 0, len(repos))
		for _, repo := range repos {
			r, ok := byRepo[repo.key]
			if !ok {
				continue
			}
			outcome.contributed++
			withData++
			total += r.deployments
			failed += r.failed
			cells := map[string]contextfabric.FactValue{
				"repository_id":                   contextfabric.StringFactValue(repo.key),
				"deployments_count_window":        contextfabric.IntegerFactValue(r.deployments),
				"failed_deployments_count_window": contextfabric.IntegerFactValue(r.failed),
				"days_with_data_window":           contextfabric.IntegerFactValue(r.days),
				"latest_day":                      contextfabric.StringFactValue(r.latestDay),
			}
			if repo.name != "" {
				cells["repository_name"] = contextfabric.StringFactValue(repo.name)
			}
			if r.hasDeployTime {
				cells["deploy_time_p50_hours_latest_day"] = contextfabric.NumberFactValue(r.deployTime)
			}
			if r.hasLead {
				cells["lead_time_p50_hours_latest_day"] = contextfabric.NumberFactValue(r.leadTime)
			}
			breakdown = append(breakdown, contextfabric.FactValueRow{Fields: cells})
		}
		fields := map[string]contextfabric.FactValue{
			"rollup_basis":                    contextfabric.StringFactValue(teamRollupBasis),
			"window_basis":                    contextfabric.StringFactValue(window.basis),
			"owned_repository_count":          contextfabric.IntegerFactValue(int64(len(repos))),
			"repositories_with_data_count":    contextfabric.IntegerFactValue(withData),
			"repositories_without_data_count": contextfabric.IntegerFactValue(int64(len(repos)) - withData),
		}
		if value, ok := window.startValue(); ok {
			fields["window_start"] = value
		}
		if value, ok := window.endValue(); ok {
			fields["window_end"] = value
		}
		if table, omitted, ok := ownedRepositoriesFactValue(repos); ok {
			fields["owned_repositories"] = table
			if omitted > 0 {
				fields["owned_repositories_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
			}
		}
		if withData > 0 {
			fields["deployments_count_window"] = contextfabric.IntegerFactValue(total)
			fields["failed_deployments_count_window"] = contextfabric.IntegerFactValue(failed)
			if table, omitted, ok := repositoryBreakdownFactValue(breakdown, grainDaily,
				[]string{"deployments_count_window", "failed_deployments_count_window", "days_with_data_window", "deploy_time_p50_hours_latest_day", "lead_time_p50_hours_latest_day"},
				[]string{"repository_name", "latest_day"}); ok {
				fields["repository_breakdown"] = table
				if omitted > 0 {
					outcome.truncated = true
					fields["repository_breakdown_omitted_count"] = contextfabric.IntegerFactValue(int64(omitted))
				}
			}
		}
		*facts = append(*facts, contextfabric.CanonicalFact{
			Kind: contextfabric.FactDeployments, Subject: subject, Fields: fields,
			EvidenceRefIDs: []string{evidenceRefID(contractsv1.ContextFabricEvidenceEntityTeam, teamID)},
		})
	}
	return outcome, nil
}
