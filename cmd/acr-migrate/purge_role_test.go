package main

import (
	"context"
	"strconv"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/stretchr/testify/require"
)

// The purge role holds exactly the purge declaration: for every table a
// migration creates (and the migration bookkeeping table) and every table
// privilege, what it holds equals what internal/dbacl declares for it, so it has
// no runtime privilege (no INSERT on credentials, no UPDATE on results) and no
// privilege beyond the purge's statements.
func TestPurgeRoleHoldsExactlyThePurgeDeclaration(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_runtime_for_purge_test", PurgeRole: "acr_purge_exact_test"})
	want := map[string]map[string]bool{}
	for _, statement := range dbacl.PurgeGrantStatements(dbacl.QuoteIdent("acr_purge_exact_test")) {
		if want[statement.Table] == nil {
			want[statement.Table] = map[string]bool{}
		}
		for _, privilege := range statement.Privileges {
			want[statement.Table][privilege] = true
		}
	}
	var mismatches []string
	for _, table := range append(migrationTables(t), "schema_migrations") {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
			var has bool
			require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, $2, $3)`, "acr_purge_exact_test", "acr."+table, privilege).Scan(&has))
			if has != want[table][privilege] {
				mismatches = append(mismatches, privilege+" on "+table+": holds "+strconv.FormatBool(has)+", declared "+strconv.FormatBool(want[table][privilege]))
			}
		}
	}
	require.Empty(t, mismatches)

	// The purge declaration is not given to the runtime role, and the runtime
	// declaration is not given to the purge role.
	var runtimeCanDelete, purgeCanInsert bool
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'acr.client_credentials', 'DELETE')`, "acr_runtime_for_purge_test").Scan(&runtimeCanDelete))
	require.False(t, runtimeCanDelete, "the runtime role cannot delete credentials")
	require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT has_table_privilege($1, 'acr.client_credentials', 'INSERT')`, "acr_purge_exact_test").Scan(&purgeCanInsert))
	require.False(t, purgeCanInsert, "the purge role cannot mint credentials")
}

// grant-runtime-acl refuses to give the purge privileges to the runtime role.
func TestGrantRuntimeACLRefusesAPurgeRoleThatIsTheRuntimeRole(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_same_role_test", SkipGrant: true})
	err := grantPurgeACL(ctx, database.Owner, "postgres://acr_same_role_test:x@h/acr", "postgres://acr_same_role_test:y@h/acr", discardWriter{})
	require.ErrorContains(t, err, "must not be the runtime role")
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// The purge takes its locks in one order: the organization's session lock, then
// a transaction, then the write fence's transaction lock, which dies with the
// transaction. The test watches pg_locks from the owner connection: at the first
// graph call (after the organization lock, before the transaction) the fence
// lock is not held; after the run neither lock remains (a session-level fence
// lock taken outside the transaction would remain on its pooled connection).
func TestPurgeLockOrderIsOrgLockThenTransactionThenFenceLock(t *testing.T) {
	ctx := context.Background()
	database := newRuntimeRoleDatabase(t, ctx, runtimeRoleOptions{Role: "acr_lock_order_runtime_test", PurgeRole: "acr_lock_order_purge_test"})
	locker, err := projectionrun.NewPostgresOrgLocker(database.Purge)
	require.NoError(t, err)
	held := func(class int) int {
		var n int
		require.NoError(t, database.Owner.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid::bigint = $1 AND granted`, class).Scan(&n))
		return n
	}
	var atGraph struct{ orgLock, fence int }
	graph := &lockObservingGraph{observe: func() { atGraph.orgLock, atGraph.fence = held(20260812), held(orgpurge.WriteFenceLockClass) }}
	const org = "0f7c2d4e-8a31-4b6d-9c15-2e7a6b9d1f03"
	purger := &orgpurge.Purger{DB: database.Purge, Graph: graph, Set: purgeTestSet{}, Entitlements: purgeTestEntitlements{}, Locker: locker}
	_, err = purger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	require.NoError(t, err)
	require.Equal(t, 1, atGraph.orgLock, "the organization lock is held before the transaction starts")
	require.Zero(t, atGraph.fence, "the write fence lock is not taken before the transaction")
	require.Zero(t, held(orgpurge.WriteFenceLockClass), "the write fence lock was a transaction lock: nothing remains after the commit")
	require.Zero(t, held(20260812), "the organization lock is released when the run ends")
}

type lockObservingGraph struct{ observe func() }

func (g *lockObservingGraph) CountOrganizationGraphs(context.Context, string) (int, error) {
	return 1, nil
}

func (g *lockObservingGraph) DeleteOrganizationGraphs(context.Context, string) (int, error) {
	g.observe()
	return 1, nil
}
