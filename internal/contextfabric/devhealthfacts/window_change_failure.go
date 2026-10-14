package devhealthfacts

import (
	"context"
	"time"

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
//   - no stored row in the window            -> no rate and no state, reason not_collected
//   - no deployments in the window           -> no rate, state not_applicable_no_deployments
//   - deployments, no incident tied and no deployment with a final status
//     -> no rate, state unknown_no_incident_evidence
//   - else (failed native + heuristic + status) / deployments; 0 is a measured 0
//
// Missing is not healthy: a repository that deploys but has no incident
// evidence never reads 0. Whenever no rate is served, change_failure_unknown_reason
// says why: the three reasons are distinct answers.
//
// The table gains two status-based columns (failed_deployments_status,
// deployments_status_known) when the ops migration rolls. acr rolls on its own
// schedule, so the reader names them only after a column probe finds both
// (change_failure_columns_probe.go); before that the statement is the one the
// older table answers.
const (
	changeFailureRateField         = "change_failure_rate"
	changeFailureStateField        = "change_failure_rate_state"
	changeFailureDeploymentsField  = "change_failure_deployments_count"
	changeFailureFailedField       = "change_failure_failed_deployments_count"
	changeFailureLinkTierField     = "change_failure_link_tier"
	changeFailureUnknownReason     = "change_failure_unknown_reason"
	changeFailureEvidenceBasis     = "change_failure_evidence_basis"
	changeFailureStatusFailedField = "change_failure_status_failed_deployments_count"
	changeFailureStatusKnownField  = "change_failure_status_known_deployments_count"
	changeFailureStatusCoverage    = "change_failure_status_coverage"

	changeFailureStateMeasured      = "measured"
	changeFailureStateNotApplicable = "not_applicable_no_deployments"
	changeFailureStateUnknown       = "unknown_no_incident_evidence"
	changeFailureStateNoCounts      = ""

	changeFailureTierNative    = "native"
	changeFailureTierHeuristic = "heuristic"

	changeFailureReasonNoDeployments  = "no_deployments_in_window"
	changeFailureReasonNotCollected   = "not_collected"
	changeFailureReasonWithoutOutcome = "deployments_without_outcome"
	changeFailureBasisIncident        = "incident"
	changeFailureBasisBoth            = "both"
	changeFailureBasisStatus          = "status"
)

// changeFailureCounts are the summed stored counts of a view and the number of
// stored rows behind them. failedStatus and statusKnown are read only when the
// table has the status columns (statusColumns).
type changeFailureCounts struct {
	storedRows             int64
	deployments            int64
	failedNative           int64
	failedHeuristic        int64
	incidentsDirect        int64
	incidentsViaDeployment int64
	failedStatus           int64
	statusKnown            int64
	statusColumns          bool
}

type changeFailureOutcome struct {
	value    float64
	measured bool
	state    string
	linkTier string
	reason   string
	basis    string
}

// evaluateChangeFailure is the one rule every read here applies to a view. It
// is acr's own copy of the ops rule (the vendored oracle executes the ops
// function against it over every count combination):
//
//   - no stored row                                -> no state, reason not_collected
//   - no deployments                               -> not applicable, reason no_deployments_in_window
//   - no incident evidence and no known status     -> unknown, reason deployments_without_outcome
//   - else (native + heuristic + status failed) / deployments, 0 a measured 0;
//     the basis names the evidence: incident, status or both.
//
// Failed by status counts the failed deployments no incident link names, so a
// failed deployment counts once. A table without the status columns has both
// status counts at 0 and so reads the incident rule alone.
func evaluateChangeFailure(c changeFailureCounts) changeFailureOutcome {
	switch {
	case c.storedRows == 0:
		return changeFailureOutcome{state: changeFailureStateNoCounts, reason: changeFailureReasonNotCollected}
	case c.deployments == 0:
		return changeFailureOutcome{state: changeFailureStateNotApplicable, reason: changeFailureReasonNoDeployments}
	}
	incidentEvidence := c.incidentsDirect+c.incidentsViaDeployment > 0
	statusEvidence := c.statusKnown > 0
	if !incidentEvidence && !statusEvidence {
		return changeFailureOutcome{state: changeFailureStateUnknown, reason: changeFailureReasonWithoutOutcome}
	}
	out := changeFailureOutcome{
		value:    float64(c.failedNative+c.failedHeuristic+c.failedStatus) / float64(c.deployments),
		measured: true,
		state:    changeFailureStateMeasured,
	}
	switch {
	case c.failedHeuristic > 0:
		out.linkTier = changeFailureTierHeuristic
	case c.failedNative > 0:
		out.linkTier = changeFailureTierNative
	}
	switch {
	case incidentEvidence && statusEvidence:
		out.basis = changeFailureBasisBoth
	case statusEvidence:
		out.basis = changeFailureBasisStatus
	default:
		out.basis = changeFailureBasisIncident
	}
	return out
}

// readWindowChangeFailure runs one statement over repo_change_failure_daily.
// grouped returns one entry per repository id; otherwise a single entry under
// the empty key covers every repository in repoKeys together (a team's owned
// set), so the rate is over the union of their deployments. A repository or
// team with no stored row has no entry. probe says which statement form the
// table answers; a nil probe reads the older form.
func readWindowChangeFailure(ctx context.Context, client contextpacket.ClickHouseQueryClient, probe *changeFailureStatusColumnsProbe, orgID string, repoKeys []string, window rollupWindow, grouped bool) (map[string]changeFailureCounts, error) {
	out := map[string]changeFailureCounts{}
	if len(repoKeys) == 0 {
		return out, nil
	}
	keyExpr, groupBy := "''", ""
	if grouped {
		keyExpr, groupBy = "toString(repo_id)", "\nGROUP BY repo_id"
	}
	statusColumns := probe.availableAt(ctx, client, time.Now())
	statusSelect := ""
	if statusColumns {
		statusSelect = `,
	toInt64(sum(failed_deployments_status)),
	toInt64(sum(deployments_status_known))`
	}
	statement := `SELECT ` + keyExpr + `,
	toInt64(count()),
	toInt64(sum(deployments_count)),
	toInt64(sum(failed_deployments_native)),
	toInt64(sum(failed_deployments_heuristic)),
	toInt64(sum(incidents_direct)),
	toInt64(sum(incidents_via_deployment))` + statusSelect + `
FROM repo_change_failure_daily FINAL
WHERE org_id = {org_id:String} AND toString(repo_id) IN {ids:Array(String)}
	AND ` + window.dayExpr("day") + groupBy
	err := readers.QueryOrgScopedNamed(ctx, client, "ReadWindowChangeFailure", statement, orgID, repoKeys, func(row contextpacket.ClickHouseRowScanner) error {
		var key string
		var c changeFailureCounts
		dest := []any{&key, &c.storedRows, &c.deployments, &c.failedNative, &c.failedHeuristic, &c.incidentsDirect, &c.incidentsViaDeployment}
		if statusColumns {
			dest = append(dest, &c.failedStatus, &c.statusKnown)
		}
		if err := row.Scan(dest...); err != nil {
			return err
		}
		c.statusColumns = statusColumns
		if c.storedRows > 0 {
			out[key] = c
		}
		return nil
	}, window.bindings()...)
	return out, err
}

// setChangeFailure adds the window change failure fields to a metrics fact.
// counts is nil for a view with no stored row: nothing was counted, so no
// state, rate or count is served; the reason says nothing was collected only
// when notCollected is set (a fact that exists for other reasons), so a
// repository with no row at all never gains a fact of its own for it.
func setChangeFailure(fields map[string]contextfabric.FactValue, counts *changeFailureCounts, notCollected bool) {
	if counts == nil {
		if notCollected {
			fields[changeFailureUnknownReason] = contextfabric.StringFactValue(changeFailureReasonNotCollected)
		}
		return
	}
	outcome := evaluateChangeFailure(*counts)
	if outcome.state == changeFailureStateNoCounts {
		return
	}
	fields[changeFailureStateField] = contextfabric.StringFactValue(outcome.state)
	fields[changeFailureDeploymentsField] = contextfabric.IntegerFactValue(counts.deployments)
	fields[changeFailureFailedField] = contextfabric.IntegerFactValue(counts.failedNative + counts.failedHeuristic + counts.failedStatus)
	if counts.statusColumns {
		fields[changeFailureStatusFailedField] = contextfabric.IntegerFactValue(counts.failedStatus)
		fields[changeFailureStatusKnownField] = contextfabric.IntegerFactValue(counts.statusKnown)
		if counts.deployments > 0 {
			fields[changeFailureStatusCoverage] = contextfabric.NumberFactValue(float64(counts.statusKnown) / float64(counts.deployments))
		}
	}
	if outcome.measured {
		fields[changeFailureRateField] = contextfabric.NumberFactValue(outcome.value)
		if outcome.linkTier != "" {
			fields[changeFailureLinkTierField] = contextfabric.StringFactValue(outcome.linkTier)
		}
		if outcome.basis != "" {
			fields[changeFailureEvidenceBasis] = contextfabric.StringFactValue(outcome.basis)
		}
		return
	}
	fields[changeFailureUnknownReason] = contextfabric.StringFactValue(outcome.reason)
}
