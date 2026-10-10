package devhealthfacts

import (
	"context"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

// The window change failure rate is the share of deployments linked to an
// incident, over the window and subject (one repository, or the repositories a
// team owns). It is read from repo_change_failure_daily, never from the
// deprecated repo_metrics_daily.change_failure_rate and never as an average of
// daily rates: the counts of every stored day in the window are summed and one
// rule is applied (the rule of the ops writer, vendored as an oracle in
// internal/contextfabric/devhealthfacts/internal/opschangefailure):
//
//   - no stored row in the window            -> no rate and no state (the fields are absent)
//   - no deployments in the window           -> no rate, state not_applicable_no_deployments
//   - deployments, no incident tied          -> no rate, state unknown_no_incident_evidence
//   - else (failed native + heuristic) / deployments; 0 is a measured 0
//
// Missing is not healthy: a repository that deploys but has no incident
// evidence never reads 0.
const (
	changeFailureRateField        = "change_failure_rate"
	changeFailureStateField       = "change_failure_rate_state"
	changeFailureDeploymentsField = "change_failure_deployments_count"
	changeFailureFailedField      = "change_failure_failed_deployments_count"
	changeFailureLinkTierField    = "change_failure_link_tier"

	changeFailureStateMeasured      = "measured"
	changeFailureStateNotApplicable = "not_applicable_no_deployments"
	changeFailureStateUnknown       = "unknown_no_incident_evidence"
	changeFailureStateNoCounts      = ""

	changeFailureTierNative    = "native"
	changeFailureTierHeuristic = "heuristic"
)

// changeFailureCounts are the summed stored counts of a view and the number of
// stored rows behind them.
type changeFailureCounts struct {
	storedRows             int64
	deployments            int64
	failedNative           int64
	failedHeuristic        int64
	incidentsDirect        int64
	incidentsViaDeployment int64
}

type changeFailureOutcome struct {
	value    float64
	measured bool
	state    string
	linkTier string
}

// evaluateChangeFailure is the one rule every read here applies to a view.
func evaluateChangeFailure(c changeFailureCounts) changeFailureOutcome {
	switch {
	case c.storedRows == 0:
		return changeFailureOutcome{state: changeFailureStateNoCounts}
	case c.deployments == 0:
		return changeFailureOutcome{state: changeFailureStateNotApplicable}
	case c.incidentsDirect+c.incidentsViaDeployment == 0:
		return changeFailureOutcome{state: changeFailureStateUnknown}
	}
	out := changeFailureOutcome{
		value:    float64(c.failedNative+c.failedHeuristic) / float64(c.deployments),
		measured: true,
		state:    changeFailureStateMeasured,
	}
	switch {
	case c.failedHeuristic > 0:
		out.linkTier = changeFailureTierHeuristic
	case c.failedNative > 0:
		out.linkTier = changeFailureTierNative
	}
	return out
}

// readWindowChangeFailure runs one statement over repo_change_failure_daily.
// grouped returns one entry per repository id; otherwise a single entry under
// the empty key covers every repository in repoKeys together (a team's owned
// set), so the rate is over the union of their deployments. A repository or
// team with no stored row has no entry.
func readWindowChangeFailure(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, repoKeys []string, window rollupWindow, grouped bool) (map[string]changeFailureCounts, error) {
	out := map[string]changeFailureCounts{}
	if len(repoKeys) == 0 {
		return out, nil
	}
	keyExpr, groupBy := "''", ""
	if grouped {
		keyExpr, groupBy = "toString(repo_id)", "\nGROUP BY repo_id"
	}
	statement := `SELECT ` + keyExpr + `,
	toInt64(count()),
	toInt64(sum(deployments_count)),
	toInt64(sum(failed_deployments_native)),
	toInt64(sum(failed_deployments_heuristic)),
	toInt64(sum(incidents_direct)),
	toInt64(sum(incidents_via_deployment))
FROM repo_change_failure_daily FINAL
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
	AND ` + window.dayExpr("day") + groupBy
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadWindowChangeFailure", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
		var key string
		var c changeFailureCounts
		if err := row.Scan(&key, &c.storedRows, &c.deployments, &c.failedNative, &c.failedHeuristic, &c.incidentsDirect, &c.incidentsViaDeployment); err != nil {
			return err
		}
		if c.storedRows > 0 {
			out[key] = c
		}
		return nil
	}, window.bindings()...)
	return out, err
}

// setChangeFailure adds the window change failure fields to a metrics fact.
// counts is nil for a view with no stored row: nothing was counted, so no
// state, rate or count is served (the same answer the ops rule gives: no state).
func setChangeFailure(fields map[string]contextfabric.FactValue, counts *changeFailureCounts) {
	if counts == nil {
		return
	}
	outcome := evaluateChangeFailure(*counts)
	if outcome.state == changeFailureStateNoCounts {
		return
	}
	fields[changeFailureStateField] = contextfabric.StringFactValue(outcome.state)
	fields[changeFailureDeploymentsField] = contextfabric.IntegerFactValue(counts.deployments)
	fields[changeFailureFailedField] = contextfabric.IntegerFactValue(counts.failedNative + counts.failedHeuristic)
	if outcome.measured {
		fields[changeFailureRateField] = contextfabric.NumberFactValue(outcome.value)
		if outcome.linkTier != "" {
			fields[changeFailureLinkTierField] = contextfabric.StringFactValue(outcome.linkTier)
		}
	}
}
