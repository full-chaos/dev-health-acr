package devhealthfacts

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	"github.com/full-chaos/dev-health-go/readers"
)

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

// membershipScope is how one statement applies the membership scope.
//
// The scope subqueries read work_unit_membership whole on every statement
// (about half of a production-sized organization's read budget). A completed
// membership run is immutable, so the mix reads resolve the run id with a tiny
// read and pass that run's work unit ids as one bound array, remembered per
// (organization, run id) -- see membershipScopeCache. The resolved scope never
// changes which work units are kept, only how the statement learns them.
//
// The memo covers ONLY the organization-wide run (the marker read from
// work_unit_membership_runs); repository-scoped runs carry no such marker and
// are never seen here. "Current" is the run with the newest completed_at,
// re-read on every request, so a run that finishes late and becomes current is
// followed, and a run id is the cache key, never an assumption of order.
type membershipScope struct {
	mode membershipScopeMode
	ids  []string
}

type membershipScopeMode int

const (
	// membershipScopeSubquery keeps today's text: the scope subqueries.
	membershipScopeSubquery membershipScopeMode = iota
	// membershipScopeNone: the organization has no complete run, every work
	// unit is read.
	membershipScopeNone
	// membershipScopeIDs: the unit ids of the latest complete run, bound.
	membershipScopeIDs
)

var subqueryMembershipScope = membershipScope{mode: membershipScopeSubquery}

const membershipScopeIDsParam = "scope_ids"

// filter is the scope predicate spliced after supersededWorkUnitIDsFilter.
func (s membershipScope) filter() string {
	switch s.mode {
	case membershipScopeNone:
		return ""
	case membershipScopeIDs:
		return "\n              AND work_unit_id IN {" + membershipScopeIDsParam + ":Array(String)}"
	default:
		return investmentMembershipScopeFilter()
	}
}

func (s membershipScope) bindings() []readers.Binding {
	if s.mode != membershipScopeIDs {
		return nil
	}
	return []readers.Binding{{Name: membershipScopeIDsParam, Value: s.ids}}
}

const (
	// membershipScopeMaxUnits bounds one remembered scope. A run with more
	// units is read through the subqueries instead.
	membershipScopeMaxUnits = 100000
	// membershipScopeMaxOrgs bounds how many organizations are remembered.
	membershipScopeMaxOrgs = 64
	// membershipScopeTTL bounds how long a remembered scope is served. A
	// completed run is immutable, so a fresh entry is exact; the bound only
	// limits how long a set read while the run's rows were still becoming
	// visible (replica lag against the run marker) could stay wrong.
	membershipScopeTTL = 5 * time.Minute
	// membershipScopeLoadTimeout bounds the one shared read of a scope's ids.
	membershipScopeLoadTimeout = 30 * time.Second
)

const membershipScopeRunStatement = `SELECT argMax(run_id, completed_at) FROM work_unit_membership_runs WHERE org_id = {org_id:String}`

// membershipScopeUnitsStatement returns the run's unit ids as ONE row (an
// array), so the client's max_result_rows never bounds a scope's size; the
// array itself holds at most membershipScopeMaxUnits+1 ids, the extra one
// proving the bound was crossed.
const membershipScopeUnitsStatement = `SELECT arraySort(groupUniqArray(100001)(work_unit_id)) FROM work_unit_membership WHERE org_id = {org_id:String} AND run_id = {scope_run:String}`

type membershipScopeEntry struct {
	runID  string
	ids    []string
	loaded time.Time
}

// membershipScopeCache remembers the unit ids of the latest complete run per
// organization. An entry is replaced when the organization's run id changes;
// concurrent loads of one (organization, run) share one read.
type membershipScopeCache struct {
	mu      sync.Mutex
	entries map[string]membershipScopeEntry
	loads   singleflight.Group
}

func newMembershipScopeCache() *membershipScopeCache {
	return &membershipScopeCache{entries: map[string]membershipScopeEntry{}}
}

func (c *membershipScopeCache) get(orgID, runID string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || entry.runID != runID || now.Sub(entry.loaded) >= membershipScopeTTL {
		return nil, false
	}
	return entry.ids, true
}

func (c *membershipScopeCache) put(orgID, runID string, ids []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[orgID]; !ok && len(c.entries) >= membershipScopeMaxOrgs {
		for key := range c.entries {
			delete(c.entries, key)
			break
		}
	}
	c.entries[orgID] = membershipScopeEntry{runID: runID, ids: ids, loaded: now}
}

// resolveMembershipScope reads the organization's latest complete run id and
// returns the scope the statements apply. A legacy run, a run too large to
// remember and a missing scope cache keep the subquery text, so the answer is
// the same in every case.
func (p *InvestmentProvider) resolveMembershipScope(ctx context.Context, orgID string) (membershipScope, error) {
	if p.scopes == nil {
		return subqueryMembershipScope, nil
	}
	runID := ""
	runs := 0
	if err := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadMembershipScopeRun", membershipScopeRunStatement, orgID, nil, func(row contextpacket.ClickHouseRowScanner) error {
		runs++
		return row.Scan(&runID)
	}); err != nil {
		if ctx.Err() != nil {
			return membershipScope{}, err
		}
		// The run could not be resolved: never serve a remembered set for a run
		// that may have been replaced. The scope subqueries decide in the
		// statement itself, exactly as before.
		return subqueryMembershipScope, nil
	}
	if runs > 1 {
		return subqueryMembershipScope, nil
	}
	switch runID {
	case "":
		return membershipScope{mode: membershipScopeNone}, nil
	case legacyRunID:
		return subqueryMembershipScope, nil
	}
	// The shared read must not die with the first caller: it runs on a context
	// that keeps the caller's values but not its cancellation, under its own
	// timeout, and every waiter still honours its own context.
	result := p.scopes.loads.DoChan(orgID+"\x00"+runID, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), membershipScopeLoadTimeout)
		defer cancel()
		if ids, ok := p.scopes.get(orgID, runID, clock()); ok {
			return ids, nil
		}
		var ids []string
		rows := 0
		scanErr := readers.QueryOrgScopedNamed(loadCtx, p.facts.client, "ReadMembershipScopeUnits", membershipScopeUnitsStatement, orgID, nil, func(row contextpacket.ClickHouseRowScanner) error {
			rows++
			return row.Scan(&ids)
		}, readers.Binding{Name: "scope_run", Value: runID})
		if scanErr != nil {
			return nil, fmt.Errorf("read membership scope units: %w", scanErr)
		}
		overflow := rows != 1 || len(ids) > membershipScopeMaxUnits
		if overflow || len(ids) == 0 {
			// Too large to remember, or no row visible for the run yet (a run
			// marker can be seen before its rows, and an organization's rows
			// can be deleted): an empty set is never remembered.
			return []string(nil), nil
		}
		p.scopes.put(orgID, runID, ids, clock())
		return ids, nil
	})
	var loaded any
	select {
	case <-ctx.Done():
		return membershipScope{}, ctx.Err()
	case res := <-result:
		if res.Err != nil {
			return membershipScope{}, res.Err
		}
		loaded = res.Val
	}
	ids, _ := loaded.([]string)
	if ids == nil {
		return subqueryMembershipScope, nil
	}
	return membershipScope{mode: membershipScopeIDs, ids: ids}, nil
}
