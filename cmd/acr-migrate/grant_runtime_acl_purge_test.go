package main

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
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

var composeGrantPattern = regexp.MustCompile(`(?m)^GRANT ([A-Z, ]+) ON TABLE (acr\.[a-z0-9_]+) TO :"runtime_user";`)

// TestGrantRuntimeACL_theWholePurgeCannotRunAsTheRuntimeRole records, by
// execution, that Purger.Run is refused on the runtime DSN: on a database
// migrated, given every runtime grant the compose db-init script applies
// (standing in for the operator's out-of-band bootstrap on Kubernetes) and
// then grant-runtime-acl, the runtime role lacks DELETE on seventeen tables'
// worth of privileges (listed in the failure log). The runtime role must not
// be widened to fix that; the purge gets its own role. This test flips to
// requiring success on that role when the purge takes its own DSN.
func TestGrantRuntimeACL_theWholePurgeCannotRunAsTheRuntimeRole(t *testing.T) {
	ctx := context.Background()
	migrationDSN := newTestPostgresDSN(t, ctx)
	require.NoError(t, run(ctx, []string{"up"}, testMigrationEnvironment(migrationDSN), &bytes.Buffer{}))

	adminConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	adminDB := stdlib.OpenDB(*adminConfig)
	t.Cleanup(func() { require.NoError(t, adminDB.Close()) })
	for _, statement := range []string{
		`CREATE ROLE acr_purge_runtime_test LOGIN PASSWORD 'runtime-test-password'`,
		`GRANT CONNECT ON DATABASE acr TO acr_purge_runtime_test`,
		`GRANT USAGE ON SCHEMA acr TO acr_purge_runtime_test`,
	} {
		_, err = adminDB.ExecContext(ctx, statement)
		require.NoError(t, err)
	}

	script, err := os.ReadFile("../../deploy/compose/acr-db-init.sh")
	require.NoError(t, err)
	applied := 0
	for _, match := range composeGrantPattern.FindAllStringSubmatch(string(script), -1) {
		if match[2] == "acr.schema_migrations" {
			continue
		}
		_, err = adminDB.ExecContext(ctx, `GRANT `+match[1]+` ON TABLE `+match[2]+` TO acr_purge_runtime_test`)
		require.NoError(t, err)
		applied++
	}
	require.Positive(t, applied, "no runtime grants parsed from acr-db-init.sh")
	require.NoError(t, run(ctx, []string{"grant-runtime-acl"}, environment(map[string]string{
		migrationDSNEnvironment: migrationDSN,
		runtimeDSNEnvironment:   "postgres://acr_purge_runtime_test:unused@runtime-dsn-is-never-dialed.invalid:5432/acr",
	}), &bytes.Buffer{}))

	var defaultPrivileges int
	require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT count(*) FROM pg_default_acl`).Scan(&defaultPrivileges))
	require.Zero(t, defaultPrivileges, "no default privileges: a missing grant must show up as permission denied")

	need := map[string][]string{"audit_events": {"SELECT", "INSERT"}, "agent_episodes": {"SELECT"}, "client_credentials": {"SELECT"}, "oauth_authorization_requests": {"SELECT"}, "context_fabric_org_purge_markers": {"SELECT", "INSERT", "UPDATE"}}
	for _, table := range orgpurge.PurgedTables() {
		need[table] = append(need[table], "DELETE")
	}
	var missing []string
	for table, privileges := range need {
		for _, privilege := range privileges {
			var has bool
			require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT has_table_privilege('acr_purge_runtime_test', $1, $2)`, "acr."+table, privilege).Scan(&has))
			if !has {
				missing = append(missing, privilege+" on "+table)
			}
		}
	}
	sort.Strings(missing)

	runtimeConfig, err := pgx.ParseConfig(migrationDSN)
	require.NoError(t, err)
	runtimeConfig.User, runtimeConfig.Password = "acr_purge_runtime_test", "runtime-test-password"
	runtimeDB := stdlib.OpenDB(*runtimeConfig)
	t.Cleanup(func() { require.NoError(t, runtimeDB.Close()) })
	locker, err := projectionrun.NewPostgresOrgLocker(runtimeDB)
	require.NoError(t, err)

	const org = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	purger := &orgpurge.Purger{DB: runtimeDB, Graph: purgeTestGraph{}, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: locker}
	_, err = purger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	t.Logf("privileges the purge needs and the runtime role lacks (%d): %s", len(missing), strings.Join(missing, "; "))
	require.ErrorIs(t, err, orgpurge.ErrIncomplete)
	require.ErrorContains(t, err, "42501", "the first refused statement is a permission error")
	require.NotEmpty(t, missing)

	// The same purge, run as the table owner (standing in for a purge-only
	// role that holds the grants), succeeds and writes the marker.
	ownerPurger := &orgpurge.Purger{DB: adminDB, Graph: purgeTestGraph{}, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: locker}
	report, err := ownerPurger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusPurged, report.Status)
	var marked bool
	require.NoError(t, adminDB.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = $1)`, org).Scan(&marked))
	require.True(t, marked, "the purge writes the marker in its transaction")
}
