package devhealthsource

import (
	"context"
	"fmt"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// OrgDiscoveryLimit bounds one ListOrgs read. It is a CEILING on a set that
// is one row per tenant, not a page size -- there is no pagination here and
// deliberately so: an operator running more organizations than this has a
// capacity decision to make, not a projector that should silently serve an
// arbitrary prefix of them. Exported so the bound a test asserts is the
// bound production uses.
const OrgDiscoveryLimit = 10000

// OrgDiscoveryTimeout bounds one ListOrgs read independently of the caller's
// context. Discovery runs at the START of every projection tick, so a
// ClickHouse that has stopped answering must fail this read quickly and let
// the tick proceed against the last-known organization set, rather than
// holding the whole tick open for the caller's (much longer) deadline.
const OrgDiscoveryTimeout = 10 * time.Second

// DefaultOrgActivityWindow is how recently an organization's canonical data
// must have moved for discovery to admit it. 30 days is wide enough that a
// real customer between releases is never dropped, and narrow enough to
// exclude the throwaway tenants a shared environment accumulates -- the
// trial ClickHouse carries 88 organization ids, most of them test junk,
// and giving each one a graph is real projection work, real FalkorDB keys
// and real embedding cost for nothing.
//
// The window is REVERSIBLE and needs no operator action either way: one
// new row inside it makes a previously-skipped organization eligible on the
// very next tick.
const DefaultOrgActivityWindow = 720 * time.Hour

// ClickHouseOrgSource is the production projectionrun.OrgSource: the
// organizations of the canonical Dev Health catalog that are worth giving a
// graph (CHAOS-6182).
//
// Eligibility is two conditions, both evaluated in ONE grouped query
// covering every organization -- never a query per organization, which at a
// 15-second tick cadence would turn discovery into the most expensive thing
// the projector does:
//
//  1. the organization owns at least one `repos` row. Repository ownership
//     is how this platform scopes a graph, so an organization with work
//     items but no repository has nothing to scope one around yet;
//  2. its most recent canonical activity -- the newest of `repos.last_synced`,
//     `work_items.updated_at` and `git_pull_requests.last_synced` -- falls
//     inside ActivityWindow.
//
// Three tables rather than `repos` alone because `repos.last_synced` is a
// SYNC stamp: a scheduled sync touches it for a dormant tenant just as it
// does for a busy one, so on its own it would admit everything. The work
// item and pull request stamps move only when someone actually did
// something. Every column read is declared in
// internal/contextfabric/devhealthschema (`repos.org_id` is `String`; the
// three timestamps are `DateTime64(3)`, two of them UTC-qualified and one
// not, which is why each branch casts explicitly rather than relying on
// UNION ALL's supertype rules).
//
// It reuses internal/contextpacket's existing ClickHouse read boundary
// (contextpacket.ClickHouseQueryClient), the same one
// ClickHouseProjectionSource reads through, rather than opening a second
// connection convention.
type ClickHouseOrgSource struct {
	client  contextpacket.ClickHouseQueryClient
	limit   int
	timeout time.Duration
	now     func() time.Time

	// ActivityWindow is condition 2's bound. A non-positive value disables
	// the activity condition entirely (condition 1 still applies) -- an
	// explicit operator choice, not a fallback: see
	// ACR_CONTEXT_FABRIC_PROJECTOR_ORG_ACTIVITY_WINDOW's own validation,
	// which refuses a negative value and defaults an unset one.
	ActivityWindow time.Duration
}

// NewClickHouseOrgSource builds the production organization-discovery
// adapter over an existing ClickHouse query client, with the default
// activity window.
func NewClickHouseOrgSource(client contextpacket.ClickHouseQueryClient) (*ClickHouseOrgSource, error) {
	return NewClickHouseOrgSourceWithWindow(client, DefaultOrgActivityWindow)
}

// NewClickHouseOrgSourceWithWindow is NewClickHouseOrgSource with the
// activity window an operator configured. A zero window disables the
// activity condition; a negative one is treated the same way rather than
// silently inverting the comparison.
func NewClickHouseOrgSourceWithWindow(client contextpacket.ClickHouseQueryClient, window time.Duration) (*ClickHouseOrgSource, error) {
	if client == nil {
		return nil, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	return &ClickHouseOrgSource{
		client: client, limit: OrgDiscoveryLimit, timeout: OrgDiscoveryTimeout,
		now: time.Now, ActivityWindow: window,
	}, nil
}

// orgDiscoveryStatement is the single grouped eligibility read.
//
// The inner UNION ALL contributes one grouped row per (table,
// organization); the outer aggregate folds them into one row per
// organization carrying its repository count and its newest activity
// across all three tables. Every branch casts to the same
// `DateTime64(3, 'UTC')` and `UInt64`, because `work_items`' timestamps are
// declared without a timezone while the other two carry 'UTC', and
// `count()` is UInt64 while a bare `0` literal is UInt8 -- leaving either
// to UNION ALL's supertype inference is how a schema change becomes a
// silent type surprise at the driver.
//
// No FINAL anywhere: this reads only grouping keys and aggregate maxima,
// and a ReplacingMergeTree's duplicate parts cannot change either one --
// an unmerged duplicate row has the same org_id and can only contribute a
// timestamp that max() already dominates or equals. The one column FINAL
// would affect is the repository COUNT, which is why condition 1 asks
// whether the count is positive rather than what it is.
const orgDiscoveryStatement = `SELECT org_id, toUInt64(max(repo_rows)) AS repo_rows, max(last_activity) AS last_activity
FROM (
  SELECT org_id, toUInt64(count()) AS repo_rows, toDateTime64(max(last_synced), 3, 'UTC') AS last_activity
  FROM repos WHERE org_id != '' GROUP BY org_id
  UNION ALL
  SELECT org_id, toUInt64(0) AS repo_rows, toDateTime64(max(updated_at), 3, 'UTC') AS last_activity
  FROM work_items WHERE org_id != '' GROUP BY org_id
  UNION ALL
  SELECT org_id, toUInt64(0) AS repo_rows, toDateTime64(max(last_synced), 3, 'UTC') AS last_activity
  FROM git_pull_requests WHERE org_id != '' GROUP BY org_id
)
GROUP BY org_id ORDER BY org_id ASC LIMIT {row_limit:UInt32}`

// ListOrgs implements projectionrun.OrgSource.
//
// Read-only, parameterized, bounded and ordered: ORDER BY org_id makes the
// result deterministic for the caller's own union, and the LIMIT binding
// makes the ceiling a bound the server enforces rather than one the caller
// trims after the fact. Blank org ids are excluded in SQL -- a blank
// organization id is not a tenant, and it would reach
// ProjectionWorker.RunOnce as an unattributable pair failure.
//
// Every organization the read saw but did not admit comes back in
// Skipped with its reason. A skipped organization is not an error and not
// an absence: it is a decision, and the caller publishes it.
func (s *ClickHouseOrgSource) ListOrgs(ctx context.Context) (contextfabric.OrgDiscoveryResult, error) {
	if s == nil || s.client == nil {
		return contextfabric.OrgDiscoveryResult{}, fmt.Errorf("devhealthsource: clickhouse query client is required")
	}
	timeout := s.timeout
	if timeout <= 0 {
		timeout = OrgDiscoveryTimeout
	}
	limit := s.limit
	if limit <= 0 {
		limit = OrgDiscoveryLimit
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	var activeSince time.Time
	if s.ActivityWindow > 0 {
		activeSince = now().UTC().Add(-s.ActivityWindow)
	}

	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rows, err := s.client.Query(queryCtx, orgDiscoveryStatement, []contextpacket.ClickHouseBinding{
		{Name: "row_limit", Value: uint32(limit)},
	})
	if err != nil {
		return contextfabric.OrgDiscoveryResult{}, fmt.Errorf("devhealthsource: list organizations: %w", err)
	}
	defer rows.Close()

	result := contextfabric.OrgDiscoveryResult{}
	for rows.Next() {
		var orgID string
		var repoRows uint64
		var lastActivity time.Time
		if err := rows.Scan(&orgID, &repoRows, &lastActivity); err != nil {
			return contextfabric.OrgDiscoveryResult{}, fmt.Errorf("devhealthsource: scan organization: %w", err)
		}
		switch {
		case repoRows == 0:
			result.Skipped = append(result.Skipped, contextfabric.SkippedOrg{OrgID: orgID, Reason: contextfabric.OrgSkipReasonNoRepo})
		case !activeSince.IsZero() && lastActivity.UTC().Before(activeSince):
			result.Skipped = append(result.Skipped, contextfabric.SkippedOrg{OrgID: orgID, Reason: contextfabric.OrgSkipReasonInactive})
		default:
			result.OrgIDs = append(result.OrgIDs, orgID)
		}
	}
	if err := rows.Err(); err != nil {
		return contextfabric.OrgDiscoveryResult{}, fmt.Errorf("devhealthsource: list organizations: %w", err)
	}
	return result, nil
}
