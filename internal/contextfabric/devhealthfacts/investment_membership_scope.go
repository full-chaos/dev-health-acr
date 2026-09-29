package devhealthfacts

import "fmt"

// CHAOS-7073 (ruling K16, option a): the repository/team theme mix reads the
// same latest work-unit set ops' query-api reads. The fragments below are
// ports of dev-health-ops internal/queryapi/analytics (origin/main):
// investmentsupersessions.go supersededWorkUnitIDsFilter and
// investmentmembershipscope.go legacyRunID, investmentScopeRunIDSQL,
// legacyNodeMaxJoinSQL, membershipScopedWorkUnitIDsSource and
// investmentMembershipScopeFilter. Their SQL bodies are copied verbatim; ops
// is the authority for investment semantics, so a change here that is not
// first a change there makes acr answer a different question under the same
// name. ops appends both fragments, in this order, after
// LatestWorkUnitInvestmentsSource's `WHERE org_id = {org_id:String}`.
//
// None of them reads work_unit_investments: they read work_unit_supersessions,
// work_unit_membership_runs and work_unit_membership only, so splicing them
// into repoMixStatement keeps its single pass over work_unit_investments
// (CHAOS-6594, max_bytes_to_read error 307).

// supersededWorkUnitIDsFilter excludes every work unit a later run retired
// (work_unit_supersessions, ops migration 085). It applies whatever the
// membership scope below decides: an organisation with no complete membership
// run reads every work unit, which is exactly when a superseded id would
// otherwise come back.
func supersededWorkUnitIDsFilter() string {
	return `
              AND work_unit_id NOT IN (
                  SELECT superseded_work_unit_id
                  FROM work_unit_supersessions
                  WHERE org_id = {org_id:String}
              )`
}

// legacyRunID is ops' reserved run id for the synthetic marker migration 048
// seeds over membership rows written before run ids existed.
const legacyRunID = "__legacy__"

// investmentScopeRunIDSQL is the run the scope reads: the run_id of the
// organisation's latest complete membership marker, or the empty string when it has none
// (argMax over an empty set yields the String default).
func investmentScopeRunIDSQL() string {
	return `(SELECT argMax(run_id, completed_at) FROM work_unit_membership_runs WHERE org_id = {org_id:String})`
}

// legacyNodeMaxJoinSQL is each node's latest pre-run-id membership instant,
// joined onto work_unit_membership AS m.
func legacyNodeMaxJoinSQL() string {
	return `
            LEFT JOIN (
                SELECT
                    org_id,
                    node_type,
                    node_id,
                    max(computed_at) AS legacy_max_computed_at
                FROM work_unit_membership
                WHERE org_id = {org_id:String} AND run_id = ''
                GROUP BY org_id, node_type, node_id
            ) AS lnm
                ON lnm.org_id = m.org_id
                AND lnm.node_type = m.node_type
                AND lnm.node_id = m.node_id`
}

// membershipScopedWorkUnitIDsSource is the work units of the scope's run: a
// real run's own membership rows, or -- when the latest marker is the legacy
// one -- each node's latest row whose run_id is empty. The two branches are disjoint on
// the run id.
func membershipScopedWorkUnitIDsSource() string {
	runID := investmentScopeRunIDSQL()
	return fmt.Sprintf(`(
        SELECT DISTINCT m.work_unit_id AS work_unit_id
        FROM work_unit_membership AS m
        WHERE m.org_id = {org_id:String}
          AND %[1]s != ''
          AND %[1]s != '%[2]s'
          AND m.run_id = %[1]s
        UNION ALL
        SELECT DISTINCT m.work_unit_id AS work_unit_id
        FROM work_unit_membership AS m
        %[3]s
        WHERE m.org_id = {org_id:String}
          AND %[1]s = '%[2]s'
          AND m.run_id = ''
          AND m.computed_at = lnm.legacy_max_computed_at
    )`, runID, legacyRunID, legacyNodeMaxJoinSQL())
}

// investmentMembershipScopeFilter keeps the work units of the latest complete
// membership run, or every work unit when the organisation has no complete run
// recorded. Returned with its leading "AND (".
func investmentMembershipScopeFilter() string {
	return fmt.Sprintf(`
              AND (
                  %s = ''
                  OR work_unit_id IN (
                      SELECT work_unit_id FROM %s
                  )
              )`, investmentScopeRunIDSQL(), membershipScopedWorkUnitIDsSource())
}
