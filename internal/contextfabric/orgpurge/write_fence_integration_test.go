package orgpurge_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pglifecycle"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgprojection"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pgstructurepriors"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/orgfence"
	storagepostgres "github.com/full-chaos/dev-health-acr/internal/storage/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

const (
	fenceInsertRebuildMarker = `INSERT INTO acr.context_fabric_projection_rebuild_markers (org_id, started_at) VALUES ($1, now()) ON CONFLICT (org_id) DO NOTHING`
	fenceSharedLockLine      = `PERFORM pg_advisory_xact_lock_shared(20261014, hashtext(canonical));`
	fenceMarkerReadLine      = `IF EXISTS (SELECT 1 FROM acr.context_fabric_org_purge_markers WHERE org_id = canonical) THEN`
)

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func purgeOrg(t *testing.T, r *rig, org string) {
	t.Helper()
	_, err := r.run(t, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
	require.NoError(t, err)
}

// fencedTables is every table that must carry the write fence: all the purge
// deletes, and the OAuth request table that goes with its device parent but
// has an organization column of its own.
func fencedTables() []string {
	tables := append(orgpurge.PurgedTables(), "oauth_authorization_requests")
	sort.Strings(tables)
	return tables
}

func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("never observed: %s", what)
}

// fenceLocks counts sessions on the write-fence advisory lock class in the
// given lock mode, waiting (granted false) or holding (granted true).
func fenceLocks(t *testing.T, db *sql.DB, mode string, granted bool) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid::bigint = $1 AND mode = $2 AND granted = $3`,
		orgpurge.WriteFenceLockClass, mode, granted).Scan(&n))
	return n
}

func markerCount(t *testing.T, db *sql.DB, org string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.context_fabric_org_purge_markers WHERE org_id = $1`, org).Scan(&n))
	return n
}

func fenceEpisodeCreate(clientEpisodeID string) contractsv1.AgentEpisodeCreate {
	return contractsv1.AgentEpisodeCreate{
		SchemaVersion: contractsv1.AgentEpisodeCreateSchema, ClientEpisodeID: clientEpisodeID, IdempotencyKey: "idem-" + clientEpisodeID, ContextPacketID: "packet_01",
		Goal: "fix the checkout flake", Summary: "fixed it", Repository: contractsv1.RepositoryRef{Slug: "owner/repo", RepoID: "repo_01"},
		Client:    contractsv1.EpisodeClient{Name: "test", Version: "1", SidecarVersion: "1"},
		StartedAt: time.Now().UTC().Add(-time.Hour), EndedAt: time.Now().UTC(), Outcome: "succeeded", RetentionClass: "default_90d",
		Artifacts: contractsv1.EpisodeArtifacts{FilesTouched: []string{}, ArtifactURIs: []string{}, TestsRun: []string{}}, Transcript: contractsv1.TranscriptRef{Mode: "none"},
	}
}

func fencePrincipal(org string) storage.Principal {
	return storage.Principal{OrgID: org, RepositoryScopes: []string{"owner/repo"}}
}

// requireNothingLeft asserts the organization has no row in any purged table,
// except the legal-hold episode the purge keeps by design.
func requireNothingLeft(t *testing.T, db *sql.DB, org string) {
	t.Helper()
	for table, n := range rowCounts(t, db, org) {
		want := int64(0)
		if table == "agent_episodes" {
			want = 1
		}
		require.Equal(t, want, n, "%s rows left for the purged organization", table)
	}
}

// blockedPurge starts a real purge of org and holds it inside its delete
// transaction: the last table it deletes from has a row locked by the returned
// transaction. The purge has taken the write fence and deleted every earlier
// table when this returns. Ending the returned transaction lets it finish.
func blockedPurge(t *testing.T, r *rig, ctx context.Context, org string) (*sql.Tx, <-chan error) {
	t.Helper()
	holder, err := r.db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = holder.Rollback() })
	var one int
	require.NoError(t, holder.QueryRowContext(context.Background(), `SELECT 1 FROM acr.context_fabric_graph_lifecycle WHERE org_id = $1 FOR UPDATE`, org).Scan(&one))
	done := make(chan error, 1)
	go func() {
		_, err := r.purger.Run(ctx, org, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(org)})
		done <- err
	}()
	eventually(t, "the purge holding the write fence and waiting inside its delete transaction", func() bool {
		var waiting int
		require.NoError(t, r.db.QueryRowContext(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE 'DELETE FROM acr.context_fabric_graph_lifecycle%'`).Scan(&waiting))
		return waiting == 1 && fenceLocks(t, r.db, "ExclusiveLock", true) == 1
	})
	return holder, done
}

func mutateFenceFunction(t *testing.T, db *sql.DB, old, replacement string) {
	t.Helper()
	var definition string
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT pg_get_functiondef('acr.context_fabric_refuse_purged_org()'::regprocedure)`).Scan(&definition))
	require.Equal(t, 1, strings.Count(definition, old), "the clause to cut must be in the deployed function exactly once")
	_, err := db.ExecContext(context.Background(), strings.Replace(definition, old, replacement, 1))
	require.NoError(t, err)
}

func TestWriteFenceTriggersCoverEveryOrgKeyedTableThePurgeDoesNotKeep(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()

	rows, err := r.db.QueryContext(ctx, `
SELECT c.relname, t.tgtype, t.tgenabled
FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_proc p ON p.oid = t.tgfoid
WHERE n.nspname = 'acr' AND NOT t.tgisinternal AND p.proname = 'context_fabric_refuse_purged_org'`)
	require.NoError(t, err)
	defer rows.Close()
	const beforeInsertRow, beforeUpdateRow = 1 | 2 | 4, 1 | 2 | 16
	onInsert, onUpdate := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var table, enabled string
		var kind int
		require.NoError(t, rows.Scan(&table, &kind, &enabled))
		require.Equal(t, "O", enabled, "write fence trigger on %s must be enabled", table)
		switch kind {
		case beforeInsertRow:
			onInsert[table] = true
		case beforeUpdateRow:
			onUpdate[table] = true
		default:
			t.Fatalf("write fence trigger on %s has an unexpected shape %d", table, kind)
		}
	}
	require.NoError(t, rows.Err())
	keys := func(set map[string]bool) []string {
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}
	require.Equal(t, fencedTables(), keys(onInsert), "tables fenced on insert")
	require.Equal(t, fencedTables(), keys(onUpdate), "tables fenced on an update that sets the organization")

	kept := map[string]bool{}
	for _, name := range orgpurge.KeptTables() {
		kept[name] = true
	}
	orgKeyed, err := r.db.QueryContext(ctx, `
SELECT DISTINCT table_name FROM information_schema.columns
WHERE table_schema = 'acr' AND column_name IN ('org_id', 'organization_id', 'authorized_org_id', 'bound_org_id', 'organization_id_hint')`)
	require.NoError(t, err)
	defer orgKeyed.Close()
	var unfenced []string
	for orgKeyed.Next() {
		var name string
		require.NoError(t, orgKeyed.Scan(&name))
		if !kept[name] && !onInsert[name] {
			unfenced = append(unfenced, name)
		}
	}
	require.NoError(t, orgKeyed.Err())
	require.Empty(t, unfenced, "organization-keyed tables with no write fence and no kept entry")

	var definer, ownedByMigrationRole, publicMayExecute bool
	var config string
	require.NoError(t, r.db.QueryRowContext(ctx, `
SELECT p.prosecdef, coalesce(array_to_string(p.proconfig, ';'), ''), pg_get_userbyid(p.proowner) = current_user, has_function_privilege('public', p.oid, 'EXECUTE')
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'acr' AND p.proname = 'context_fabric_refuse_purged_org'`).Scan(&definer, &config, &ownedByMigrationRole, &publicMayExecute))
	require.True(t, definer, "the fence function reads the marker with its owner's privileges, so no grant to the writer's role can be missing")
	require.Equal(t, "search_path=pg_catalog, pg_temp", config)
	require.True(t, ownedByMigrationRole, "the fence function is owned by the role that ran the migrations")
	require.False(t, publicMayExecute, "no role may call the fence function directly")
}

func TestWriteFenceRefusesEveryFencedTableAfterAPurge(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	seedPostgres(t, r.db, orgKept)
	purgeOrg(t, r, orgGone)
	require.Equal(t, 1, markerCount(t, r.db, orgGone))

	insertInto := regexp.MustCompile(`INSERT INTO acr\.([a-z_]+)`)
	suffix := strings.ReplaceAll(orgGone[:8], "-", "")
	refused := map[string]bool{}
	for _, statement := range seedStatements() {
		table := insertInto.FindStringSubmatch(statement)[1]
		args := []any{orgGone}
		if strings.Contains(statement, "$2") {
			args = append(args, suffix)
		}
		switch {
		case table == "oauth_device_grants", strings.Contains(statement, "approving_subject, created_at"):
			continue // no organization column in the row
		case table == "audit_events":
			_, err := r.db.ExecContext(ctx, statement, args...)
			require.NoError(t, err, "the audit trail is kept and stays writable")
		default:
			_, err := r.db.ExecContext(ctx, statement, args...)
			require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "%s: %v", table, err)
			require.True(t, orgfence.Refused(err))
			refused[table] = true
		}
	}
	tables := make([]string, 0, len(refused))
	for table := range refused {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	require.Equal(t, fencedTables(), tables, "every fenced table has a refused write in this test")
	requireNothingLeft(t, r.db, orgGone)

	for _, spelling := range []string{
		orgGone, strings.ToUpper(orgGone), "{" + orgGone + "}", "urn:uuid:" + orgGone, strings.ReplaceAll(orgGone, "-", ""), "  " + strings.ToUpper(orgGone) + " ",
	} {
		_, err := r.db.ExecContext(ctx, fenceInsertRebuildMarker, spelling)
		require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "text organization column spelled %q: %v", spelling, err)
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO acr.context_packet_snapshots (context_packet_id, org_id, repo_id, repo_slug, request_id, schema_version, query_version, ranking_version, scope_resolution, status, payload, generated_at, expires_at, created_at)
VALUES ('pkt-upper', $1::uuid, $1::uuid, 'a/b', 'req', 'context_packet.v1', 'q', 'r', 'exact_commit', 'complete', '{}', now(), now() + interval '1 day', now())`, strings.ToUpper(orgGone))
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "uuid organization column given in upper case: %v", err)
	requireNothingLeft(t, r.db, orgGone)

	seedPostgres(t, r.db, orgBroken)
	_, err = r.db.ExecContext(ctx, fenceInsertRebuildMarker, "not-a-uuid")
	require.NoError(t, err, "an id that is not a UUID cannot have been purged")

	_, err = r.db.ExecContext(ctx, `INSERT INTO acr.device_authorizations (device_code_hash, user_code_hash, state, created_at, expires_at, poll_interval_seconds, issuance_provenance)
VALUES (repeat(md5('fence-d'), 2), repeat(md5('fence-u'), 2), 'pending', now(), now() + interval '10 minutes', 5, 'device_authorization')`)
	require.NoError(t, err)
	_, err = r.db.ExecContext(ctx, `UPDATE acr.device_authorizations SET authorized_org_id = $1::uuid WHERE device_code_hash = repeat(md5('fence-d'), 2)`, strings.ToUpper(orgGone))
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "an update that gives a device authorization the purged organization: %v", err)
	_, err = r.db.ExecContext(ctx, `UPDATE acr.device_authorizations SET organization_id_hint = $1 WHERE device_code_hash = repeat(md5('fence-d'), 2)`, "{"+orgGone+"}")
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "an update that sets the purged organization as the hint: %v", err)
	_, err = r.db.ExecContext(ctx, `INSERT INTO acr.oauth_authorization_requests (handle_hash, device_code_hash, client_id, client_kind, redirect_uri, code_challenge, resource, created_at, expires_at)
VALUES (repeat(md5('fence-h'), 2), repeat(md5('fence-d'), 2), 'client', 'dynamic', 'http://localhost/cb', repeat('a', 43), 'res', now(), now() + interval '10 minutes')`)
	require.NoError(t, err)
	_, err = r.db.ExecContext(ctx, `UPDATE acr.oauth_authorization_requests SET bound_org_id = $1, bound_subject = 'subject' WHERE handle_hash = repeat(md5('fence-h'), 2)`, strings.ReplaceAll(strings.ToUpper(orgGone), "-", ""))
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "an update that binds an OAuth request to the purged organization: %v", err)
	_, err = r.db.ExecContext(ctx, `UPDATE acr.device_authorizations SET authorized_org_id = $1::uuid WHERE device_code_hash = repeat(md5('fence-d'), 2)`, orgKept)
	require.NoError(t, err, "the same update for another organization")
	result, err := r.db.ExecContext(ctx, `UPDATE acr.agent_episodes SET updated_at = now() WHERE org_id = $1::uuid`, orgGone)
	require.NoError(t, err, "an update that does not change the organization of a kept row is not a new write")
	touched, err := result.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, touched)
	requireNothingLeft(t, r.db, orgGone)

	require.False(t, graphExists(t, r.adapter, orgGone), "a graph read for the purged organization")
	graphs, err := r.adapter.CountOrganizationGraphs(ctx, orgGone)
	require.NoError(t, err)
	require.Zero(t, graphs, "a read must not create a graph for the purged organization")

	markers, err := pgprojection.NewOrgPurgeMarkerStore(r.db)
	require.NoError(t, err)
	clear, release, err := markers.BeginDiscovery(ctx)
	require.NoError(t, err)
	defer release()
	cleared, err := clear(ctx, []string{orgGone})
	require.NoError(t, err)
	require.Equal(t, []string{orgGone}, cleared)
	_, err = r.db.ExecContext(ctx, fenceInsertRebuildMarker, strings.ToUpper(orgGone))
	require.NoError(t, err, "the organization returned to discovery: the fence is lifted")
}

// A purge of an organization that already carries a marker must pass its own
// fence: its statements are deletes, the audit insert and the marker upsert,
// none of which the fence checks, and the foreign-key actions of its deletes
// do not change an organization column.
func TestWriteFenceARepeatedPurgeOfAMarkedOrganizationRemovesRowsThatPredateTheFence(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	purgeOrg(t, r, orgGone)
	require.Equal(t, 1, markerCount(t, r.db, orgGone))

	conn, err := r.db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET session_replication_role = replica`)
	require.NoError(t, err)
	suffix := strings.ReplaceAll(orgGone[:8], "-", "")
	for _, statement := range seedStatements() {
		if strings.Contains(statement, "'ep2-'") {
			continue // the legal-hold episode was kept by the first purge
		}
		args := []any{orgGone}
		if strings.Contains(statement, "$2") {
			args = append(args, suffix)
		}
		_, err := conn.ExecContext(ctx, statement, args...)
		require.NoError(t, err, statement)
	}
	_, err = conn.ExecContext(ctx, `SET session_replication_role = DEFAULT`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	for _, table := range countedTables() {
		require.NotZero(t, rowCounts(t, r.db, orgGone)[table], "a row planted behind the fence in %s", table)
	}

	report, err := r.run(t, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusPurged, report.Status)
	require.NotZero(t, report.TotalRows)
	require.Equal(t, 1, markerCount(t, r.db, orgGone))
	requireNothingLeft(t, r.db, orgGone)
}

// An upsert whose row already exists, and an update that moves a row into a
// purged organization, are both refused; each is accepted with its trigger off.
func TestWriteFenceRefusesAnUpsertOverASurvivingRowAndAMoveIntoAMarkedOrganization(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	seedPostgres(t, r.db, orgKept)
	purgeOrg(t, r, orgGone)
	results, err := pginvestigation.NewStore(r.db)
	require.NoError(t, err)

	conn, err := r.db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.ExecContext(ctx, `SET session_replication_role = replica`)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `INSERT INTO acr.context_fabric_reuse_invalidations (org_id, invalidated_at, epoch) VALUES ($1, now(), 1)`, orgGone)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, `SET session_replication_role = DEFAULT`)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	epoch := func() int64 {
		var value int64
		require.NoError(t, r.db.QueryRowContext(ctx, `SELECT epoch FROM acr.context_fabric_reuse_invalidations WHERE org_id = $1`, orgGone).Scan(&value))
		return value
	}

	require.ErrorIs(t, results.InvalidateOrganizationReuse(ctx, orgGone), storage.ErrOrgPurged, "the insert side of the upsert is checked before the conflict arm")
	require.EqualValues(t, 1, epoch(), "the surviving row is unchanged")
	_, err = r.db.ExecContext(ctx, `UPDATE acr.context_fabric_projection_rebuild_markers SET org_id = $1 WHERE org_id = $2`, strings.ToUpper(orgGone), orgKept)
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err), "an update that moves a row into the purged organization: %v", err)
	require.Zero(t, rowCounts(t, r.db, orgGone)["context_fabric_projection_rebuild_markers"])

	_, err = r.db.ExecContext(ctx, `ALTER TABLE acr.context_fabric_reuse_invalidations DISABLE TRIGGER refuse_purged_org_insert`)
	require.NoError(t, err)
	require.NoError(t, results.InvalidateOrganizationReuse(ctx, orgGone))
	require.EqualValues(t, 2, epoch(), "with the insert trigger off the upsert rewrites the purged organization's row")
	_, err = r.db.ExecContext(ctx, `ALTER TABLE acr.context_fabric_projection_rebuild_markers DISABLE TRIGGER refuse_purged_org_update`)
	require.NoError(t, err)
	_, err = r.db.ExecContext(ctx, `UPDATE acr.context_fabric_projection_rebuild_markers SET org_id = $1 WHERE org_id = $2`, strings.ToUpper(orgGone), orgKept)
	require.NoError(t, err)
	require.EqualValues(t, 1, rowCounts(t, r.db, orgGone)["context_fabric_projection_rebuild_markers"], "with the update trigger off the row moves into the purged organization")
}

func TestWriteFenceProductionWritersReturnTheTypedError(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	purgeOrg(t, r, orgGone)

	episodes, err := storagepostgres.NewEpisodeStore(r.db)
	require.NoError(t, err)
	rebuilds, err := pgprojection.NewRebuildMarkerStore(r.db)
	require.NoError(t, err)
	results, err := pginvestigation.NewStore(r.db)
	require.NoError(t, err)
	priors, err := pgstructurepriors.NewStore(r.db)
	require.NoError(t, err)
	lifecycle, err := pglifecycle.NewStore(r.db)
	require.NoError(t, err)

	writers := map[string]func(org string) error{
		"episode create": func(org string) error {
			_, _, err := episodes.CreateIdempotent(ctx, fencePrincipal(org), fenceEpisodeCreate("ep-"+org[:4]), nil)
			return err
		},
		"rebuild marker":     func(org string) error { return rebuilds.BeginRebuild(ctx, org) },
		"reuse invalidation": func(org string) error { return results.InvalidateOrganizationReuse(ctx, org) },
		"prior revocation":   func(org string) error { return priors.RevokeEntry(ctx, org, "entry-fence", "tester") },
		"graph lifecycle row": func(org string) error {
			_, err := lifecycle.BeginBuild(ctx, org, []string{"src"}, time.Now().UTC())
			return err
		},
	}
	for name, write := range writers {
		err := write(orgGone)
		require.ErrorIs(t, err, storage.ErrOrgPurged, name)
		require.ErrorIs(t, write(strings.ToUpper(orgGone)), storage.ErrOrgPurged, "%s, upper-case organization id", name)
		require.NoError(t, write(orgKept), "%s for an organization that was not purged", name)
	}
	requireNothingLeft(t, r.db, orgGone)
}

func TestWriteFenceAWriteThatStartsDuringThePurgeWaitsThenIsRefused(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	episodes, err := storagepostgres.NewEpisodeStore(r.db)
	require.NoError(t, err)
	rebuilds, err := pgprojection.NewRebuildMarkerStore(r.db)
	require.NoError(t, err)

	holder, purged := blockedPurge(t, r, ctx, orgGone)

	episodeDone, rebuildDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, err := episodes.CreateIdempotent(ctx, fencePrincipal(orgGone), fenceEpisodeCreate("ep-during-purge"), nil)
		episodeDone <- err
	}()
	go func() { rebuildDone <- rebuilds.BeginRebuild(ctx, "{"+strings.ToUpper(orgGone)+"}") }()
	eventually(t, "both writers waiting on the organization's write fence", func() bool {
		return fenceLocks(t, r.db, "ShareLock", false) == 2
	})
	select {
	case err := <-episodeDone:
		t.Fatalf("the episode write returned while the purge transaction was open: %v", err)
	case err := <-rebuildDone:
		t.Fatalf("the rebuild marker write returned while the purge transaction was open: %v", err)
	case err := <-purged:
		t.Fatalf("the purge finished while its row was locked: %v", err)
	default:
	}

	require.NoError(t, holder.Commit())
	require.NoError(t, <-purged)
	require.ErrorIs(t, <-episodeDone, storage.ErrOrgPurged)
	require.ErrorIs(t, <-rebuildDone, storage.ErrOrgPurged)
	require.Equal(t, 1, markerCount(t, r.db, orgGone))
	requireNothingLeft(t, r.db, orgGone)
}

func TestWriteFenceAWriteThatWaitedProceedsWhenThePurgeRollsBack(t *testing.T) {
	r := newRig(t)
	seedPostgres(t, r.db, orgGone)
	episodes, err := storagepostgres.NewEpisodeStore(r.db)
	require.NoError(t, err)

	purgeCtx, cancelPurge := context.WithCancel(context.Background())
	defer cancelPurge()
	holder, purged := blockedPurge(t, r, purgeCtx, orgGone)
	written := make(chan error, 1)
	go func() {
		_, _, err := episodes.CreateIdempotent(context.Background(), fencePrincipal(orgGone), fenceEpisodeCreate("ep-purge-rolled-back"), nil)
		written <- err
	}()
	eventually(t, "the writer waiting on the organization's write fence", func() bool {
		return fenceLocks(t, r.db, "ShareLock", false) == 1
	})

	cancelPurge()
	require.ErrorIs(t, <-purged, orgpurge.ErrIncomplete)
	require.NoError(t, holder.Rollback())
	require.NoError(t, <-written, "nothing was purged, so the write is accepted")
	require.Zero(t, markerCount(t, r.db, orgGone))
	var stored int
	require.NoError(t, r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.agent_episodes WHERE org_id = $1::uuid AND client_episode_id = 'ep-purge-rolled-back'`, orgGone).Scan(&stored))
	require.Equal(t, 1, stored)
}

func TestWriteFenceThePurgeWaitsForAWriteInFlightAndThenRemovesIt(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	before := rowCounts(t, r.db, orgGone)

	writer, err := r.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = writer.Rollback() }()
	_, err = writer.ExecContext(ctx, fenceInsertRebuildMarker, strings.ToUpper(orgGone))
	require.NoError(t, err)
	require.Equal(t, 1, fenceLocks(t, r.db, "ShareLock", true), "the open write holds the fence in shared mode")

	impatient := *r.purger
	impatient.WriteFenceLockTimeout = 300 * time.Millisecond
	report, err := impatient.Run(ctx, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
	require.ErrorIs(t, err, orgpurge.ErrWriteFenceTimeout)
	require.ErrorIs(t, err, orgpurge.ErrIncomplete, "a purge that cannot take the fence fails; it never deletes without it")
	require.Equal(t, orgpurge.StatusIncomplete, report.Status)
	require.Equal(t, before, rowCounts(t, r.db, orgGone), "a timed-out purge deletes no row")
	require.Zero(t, markerCount(t, r.db, orgGone))

	purged := make(chan error, 1)
	go func() {
		_, err := r.purger.Run(ctx, orgGone, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)})
		purged <- err
	}()
	eventually(t, "the purge waiting for the write fence", func() bool {
		return fenceLocks(t, r.db, "ExclusiveLock", false) == 1
	})
	select {
	case err := <-purged:
		t.Fatalf("the purge finished while a write of the organization was open: %v", err)
	default:
	}
	require.NoError(t, writer.Commit())
	require.NoError(t, <-purged)
	requireNothingLeft(t, r.db, orgGone)
	require.Equal(t, 1, markerCount(t, r.db, orgGone))
}

func TestWriteFenceRefusesAFencedWriteOutsideReadCommitted(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for _, level := range []sql.IsolationLevel{sql.LevelRepeatableRead, sql.LevelSerializable} {
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: level})
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, fenceInsertRebuildMarker, orgKept)
		require.Equal(t, orgfence.IsolationSQLState, sqlState(err), "%s: %v", level, err)
		require.NoError(t, tx.Rollback())
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, fenceInsertRebuildMarker, orgKept)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	var level string
	require.NoError(t, r.db.QueryRowContext(ctx, `SHOW default_transaction_isolation`).Scan(&level))
	require.Equal(t, "read committed", level, "the pool's default isolation")
}

// The three tests below cut one clause of the fence in the test database and
// show the defect that clause exists to stop.

func TestWriteFenceWithoutTheSharedLockAWriteDuringThePurgeSurvivesIt(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	mutateFenceFunction(t, r.db, fenceSharedLockLine, "")
	rebuilds, err := pgprojection.NewRebuildMarkerStore(r.db)
	require.NoError(t, err)

	holder, purged := blockedPurge(t, r, ctx, orgGone)
	require.NoError(t, rebuilds.BeginRebuild(ctx, strings.ToUpper(orgGone)), "with the lock cut the write does not wait and the marker is not yet visible")
	require.NoError(t, holder.Commit())
	require.NoError(t, <-purged)

	require.Equal(t, 1, markerCount(t, r.db, orgGone))
	require.EqualValues(t, 1, rowCounts(t, r.db, orgGone)["context_fabric_projection_rebuild_markers"], "the row written during the purge outlives it")
}

func TestWriteFenceWithoutTheMarkerReadAWriteAfterThePurgeIsAccepted(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	purgeOrg(t, r, orgGone)
	_, err := r.db.ExecContext(ctx, fenceInsertRebuildMarker, orgGone)
	require.Equal(t, orgfence.PurgedSQLState, sqlState(err))

	mutateFenceFunction(t, r.db, fenceMarkerReadLine, "IF false THEN")
	_, err = r.db.ExecContext(ctx, fenceInsertRebuildMarker, orgGone)
	require.NoError(t, err)
	require.EqualValues(t, 1, rowCounts(t, r.db, orgGone)["context_fabric_projection_rebuild_markers"])
}

func TestWriteFenceWithoutTheTriggerAPurgedOrganizationIsWrittenAgain(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	purgeOrg(t, r, orgGone)
	rebuilds, err := pgprojection.NewRebuildMarkerStore(r.db)
	require.NoError(t, err)
	require.ErrorIs(t, rebuilds.BeginRebuild(ctx, orgGone), storage.ErrOrgPurged)

	_, err = r.db.ExecContext(ctx, `ALTER TABLE acr.context_fabric_projection_rebuild_markers DISABLE TRIGGER refuse_purged_org_insert`)
	require.NoError(t, err)
	require.NoError(t, rebuilds.BeginRebuild(ctx, orgGone))
	require.EqualValues(t, 1, rowCounts(t, r.db, orgGone)["context_fabric_projection_rebuild_markers"])
}

// TestWriteFenceCost measures what the fence adds to a write. Each figure is
// the slowest of three warm rounds.
func TestWriteFenceCost(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	const rowsPerRound = 500
	round := 0
	slowest := func(statement string, args func(i int) []any) time.Duration {
		var worst time.Duration
		for attempt := 0; attempt < 4; attempt++ {
			round++
			started := time.Now()
			for i := 0; i < rowsPerRound; i++ {
				_, err := r.db.ExecContext(ctx, statement, args(round*rowsPerRound+i)...)
				require.NoError(t, err)
			}
			if elapsed := time.Since(started); attempt > 0 && elapsed > worst {
				worst = elapsed // the first round warms the connection and the plan
			}
		}
		return worst / rowsPerRound
	}
	setTriggers := func(table, state string) {
		for _, trigger := range []string{"refuse_purged_org_insert", "refuse_purged_org_update"} {
			_, err := r.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE acr.%s %s TRIGGER %s`, table, state, trigger))
			require.NoError(t, err)
		}
	}
	measure := func(name, table, statement string, args func(i int) []any) {
		fenced := slowest(statement, args)
		setTriggers(table, "DISABLE")
		bare := slowest(statement, args)
		setTriggers(table, "ENABLE")
		t.Logf("write fence cost: %s: %s per row with the fence, %s without (slowest of 3 warm rounds of %d rows)", name, fenced, bare, rowsPerRound)
		require.Less(t, fenced, 20*time.Millisecond, "%s: one fenced write", name)
	}

	measure("request path insert (model execution receipt)", "context_fabric_model_execution_receipts",
		`INSERT INTO acr.context_fabric_model_execution_receipts (receipt_id, org_id, operation, provider, outcome, fallback_used, payload, started_at, completed_at)
VALUES ($1, $2, 'op', 'p', 'ok', false, '{}', now(), now())`,
		func(i int) []any { return []any{fmt.Sprintf("receipt-cost-%d", i), orgKept} })
	measure("projector checkpoint insert", "context_fabric_projection_checkpoints",
		`INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, updated_at) VALUES ($1, $2, now())`,
		func(i int) []any { return []any{orgKept, fmt.Sprintf("src-cost-%d", i)} })
	_, err := r.db.ExecContext(ctx, `INSERT INTO acr.context_fabric_projection_checkpoints (org_id, source, updated_at) VALUES ($1, 'src-steady', now())`, orgKept)
	require.NoError(t, err)
	measure("projector checkpoint update (every source, every tick)", "context_fabric_projection_checkpoints",
		`UPDATE acr.context_fabric_projection_checkpoints SET updated_at = now() WHERE org_id = $1 AND source = 'src-steady' AND $2::int IS NOT NULL`,
		func(i int) []any { return []any{orgKept, i} })
	t.Fatal("TEMPORARY COST PROBE: prints the figures above; removed in the next commit")
}
