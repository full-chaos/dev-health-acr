package main

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/stretchr/testify/require"
)

type purgeTestGraph struct{}

func (purgeTestGraph) CountOrganizationGraphs(context.Context, string) (int, error)  { return 1, nil }
func (purgeTestGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) { return 1, nil }

type purgeTestSet struct{}

func (purgeTestSet) RefreshOrgs(context.Context) error { return nil }
func (purgeTestSet) EffectiveOrgs() []string           { return nil }

type purgeTestEntitlements struct{}

func (purgeTestEntitlements) OrganizationUnknown(context.Context, string) (bool, error) {
	return true, nil
}

// TestGrantRuntimeACL_theWholePurgeCannotRunAsTheRuntimeRole records, by
// execution, that Purger.Run is refused on the runtime DSN: on a database
// migrated and then given the runtime declaration by grant-runtime-acl, the
// runtime role lacks DELETE on many tables and SELECT on audit_events (listed in
// the failure log). The runtime role must not be widened to fix that; the purge
// gets its own role. This test flips to requiring success on that role when the
// purge takes its own DSN.
func TestGrantRuntimeACL_theWholePurgeCannotRunAsTheRuntimeRole(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_purge_runtime_test"})

	need := map[string][]string{"audit_events": {"SELECT", "INSERT"}, "agent_episodes": {"SELECT"}, "client_credentials": {"SELECT"}, "oauth_authorization_requests": {"SELECT"}, "context_fabric_org_purge_markers": {"SELECT", "INSERT", "UPDATE"}}
	for _, table := range orgpurge.PurgedTables() {
		need[table] = append(need[table], "DELETE")
	}
	var missing []string
	for table, privileges := range need {
		for _, privilege := range privileges {
			var has bool
			require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, database.Role, "acr."+table, privilege).Scan(&has))
			if !has {
				missing = append(missing, privilege+" on "+table)
			}
		}
	}
	sort.Strings(missing)

	locker, err := projectionrun.NewPostgresOrgLocker(database.Runtime)
	require.NoError(t, err)
	const org = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	purger := &orgpurge.Purger{DB: database.Runtime, Graph: purgeTestGraph{}, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: locker}
	_, err = purger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	t.Logf("privileges the purge needs and the runtime role lacks (%d): %s", len(missing), strings.Join(missing, "; "))
	require.ErrorIs(t, err, orgpurge.ErrIncomplete)
	require.ErrorContains(t, err, "42501", "the first refused statement is a permission error")
	require.NotEmpty(t, missing)

	// The same purge, run as the table owner (standing in for a purge-only
	// role that holds the grants), succeeds and writes the marker.
	ownerPurger := &orgpurge.Purger{DB: database.Owner, Graph: purgeTestGraph{}, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: locker}
	report, err := ownerPurger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusPurged, report.Status)
	var marked bool
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = $1)`, org).Scan(&marked))
	require.True(t, marked, "the purge writes the marker in its transaction")
}
