package devhealthfacts

import (
	"context"
	"fmt"
	"sync"

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
)

const membershipScopeRunStatement = `SELECT argMax(run_id, completed_at) FROM work_unit_membership_runs WHERE org_id = {org_id:String}`

const membershipScopeUnitsStatement = `SELECT DISTINCT work_unit_id FROM work_unit_membership WHERE org_id = {org_id:String} AND run_id = {scope_run:String} ORDER BY work_unit_id`

type membershipScopeEntry struct {
	runID string
	ids   []string
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

func (c *membershipScopeCache) get(orgID, runID string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[orgID]
	if !ok || entry.runID != runID {
		return nil, false
	}
	return entry.ids, true
}

func (c *membershipScopeCache) put(orgID, runID string, ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[orgID]; !ok && len(c.entries) >= membershipScopeMaxOrgs {
		for key := range c.entries {
			delete(c.entries, key)
			break
		}
	}
	c.entries[orgID] = membershipScopeEntry{runID: runID, ids: ids}
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
		return membershipScope{}, err
	}
	if runs > 1 {
		return membershipScope{}, fmt.Errorf("membership scope run read returned %d rows, want at most 1", runs)
	}
	switch runID {
	case "":
		return membershipScope{mode: membershipScopeNone}, nil
	case legacyRunID:
		return subqueryMembershipScope, nil
	}
	if ids, ok := p.scopes.get(orgID, runID); ok {
		return membershipScope{mode: membershipScopeIDs, ids: ids}, nil
	}
	loaded, err, _ := p.scopes.loads.Do(orgID+"\x00"+runID, func() (any, error) {
		if ids, ok := p.scopes.get(orgID, runID); ok {
			return ids, nil
		}
		ids := make([]string, 0, 1024)
		overflow := false
		scanErr := readers.QueryOrgScopedNamed(ctx, p.facts.client, "ReadMembershipScopeUnits", membershipScopeUnitsStatement, orgID, nil, func(row contextpacket.ClickHouseRowScanner) error {
			if overflow {
				return nil
			}
			var id string
			if err := row.Scan(&id); err != nil {
				return err
			}
			if len(ids) >= membershipScopeMaxUnits {
				overflow = true
				return nil
			}
			ids = append(ids, id)
			return nil
		}, readers.Binding{Name: "scope_run", Value: runID})
		if scanErr != nil {
			return nil, scanErr
		}
		if overflow {
			return []string(nil), nil
		}
		p.scopes.put(orgID, runID, ids)
		return ids, nil
	})
	if err != nil {
		return membershipScope{}, err
	}
	ids, _ := loaded.([]string)
	if ids == nil {
		return subqueryMembershipScope, nil
	}
	return membershipScope{mode: membershipScopeIDs, ids: ids}, nil
}
