package main

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
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

// TestGrantRuntimeACL_theWholePurgeRunsOnlyAsThePurgeRole: on a database
// migrated and then given both declarations by grant-runtime-acl, Purger.Run is
// refused for the runtime role (it lacks DELETE on many tables and SELECT on
// audit_events, listed in the failure log) and succeeds for the purge role, which
// then holds the marker it wrote. The runtime role is not widened for the purge.
func TestGrantRuntimeACL_theWholePurgeRunsOnlyAsThePurgeRole(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_purge_runtime_test", PurgeRole: "acr_purge_test"})

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

	// The same purge, run as the purge role, succeeds and writes the marker.
	purgeLocker, err := projectionrun.NewPostgresOrgLocker(database.Purge)
	require.NoError(t, err)
	_, err = database.Owner.ExecContext(ctx, `INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, updated_at) VALUES ($1, 'github', now())`, org)
	require.NoError(t, err)
	purgePurger := &orgpurge.Purger{DB: database.Purge, Graph: purgeTestGraph{}, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: purgeLocker}
	dry, err := purgePurger.Run(ctx, org, orgpurge.Options{DryRun: true})
	require.NoError(t, err, "dry run as the purge role")
	require.EqualValues(t, 1, dry.Rows["context_fabric_projection_checkpoints"])
	report, err := purgePurger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusPurged, report.Status)
	require.True(t, report.AuditRowWritten)
	var rows int
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_projection_checkpoints WHERE org_id = $1`, org).Scan(&rows))
	require.Zero(t, rows, "the purge role deleted the organization's rows")
	var marked bool
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = $1)`, org).Scan(&marked))
	require.True(t, marked, "the purge writes the marker in its transaction")

	// What the purge command does around the purge, as the purge role: it
	// verifies the schema when it opens its runtime and clears markers on
	// discovery.
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	require.NoError(t, runner.VerifyCurrent(ctx, database.Purge), "schema verification as the purge role")
	store, err := pgprojection.NewOrgPurgeMarkerStore(database.Purge)
	require.NoError(t, err)
	clear, release, err := store.BeginDiscovery(ctx)
	require.NoError(t, err)
	defer release()
	_, err = clear(ctx, []string{org})
	require.NoError(t, err, "discovery's clear as the purge role")
}
