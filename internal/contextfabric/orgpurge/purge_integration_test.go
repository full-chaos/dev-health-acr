package orgpurge_test

import (
	"context"
	"database/sql"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/orgpurge"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	runtimepostgres "github.com/full-chaos/dev-health-acr/internal/runtime/postgres"
	migrations "github.com/full-chaos/dev-health-acr/migrations/postgres"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	falkordbImage = "falkordb/falkordb@sha256:ad09d5051bbda1cfee8cef9d7f41ffe1bcb1c5327b82c442c989e84ab8cc33d3"
	orgGone       = "aaaaaaaa-0000-4000-8000-000000000001"
	orgKept       = "bbbbbbbb-0000-4000-8000-000000000002"
	orgBroken     = "cccccccc-0000-4000-8000-000000000003"
	graphSource   = "purge-test"
)

type fakeSet struct{ members []string }

func (*fakeSet) RefreshOrgs(context.Context) error { return nil }
func (f *fakeSet) EffectiveOrgs() []string         { return f.members }

type fakeEntitlements struct{ known map[string]bool }

func (f *fakeEntitlements) OrganizationUnknown(_ context.Context, org string) (bool, error) {
	return !f.known[org], nil
}

type rig struct {
	db      *sql.DB
	adapter *falkorgraph.Adapter
	set     *fakeSet
	ent     *fakeEntitlements
	purger  *orgpurge.Purger
}

func newRig(t *testing.T) *rig {
	ctx := context.Background()
	pg, err := tcpostgres.Run(ctx, "postgres:18-alpine@sha256:a1d02e4bd40c94d3bf2bdd3678c137388e76d9efcd23c285e9429d336a834b44",
		tcpostgres.WithDatabase("acr"), tcpostgres.WithUsername("acr"), tcpostgres.WithPassword("acr"), tcpostgres.BasicWaitStrategies())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, pg.Terminate(ctx)) })
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := runtimepostgres.Open(ctx, runtimepostgres.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	runner, err := migrations.Embedded()
	require.NoError(t, err)
	_, err = runner.Apply(ctx, db)
	require.NoError(t, err)

	fk, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{Image: falkordbImage, ExposedPorts: []string{"6379/tcp"},
			WaitingFor: wait.ForListeningPort("6379/tcp").WithStartupTimeout(2 * time.Minute)},
		Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, fk.Terminate(context.Background())) })
	host, err := fk.Host(ctx)
	require.NoError(t, err)
	port, err := fk.MappedPort(ctx, "6379/tcp")
	require.NoError(t, err)
	t.Setenv(falkorgraph.EnvAddr, host+":"+port.Port())
	t.Setenv(falkorgraph.EnvTLS, "false")
	t.Setenv(falkorgraph.EnvAllowInsecure, "true")
	t.Setenv(falkorgraph.EnvGraphPrefix, "acr-purge-test")
	cfg, err := falkorgraph.ConfigFromEnv(os.LookupEnv)
	require.NoError(t, err)
	adapter, err := falkorgraph.New(cfg)
	require.NoError(t, err)

	locker, err := projectionrun.NewPostgresOrgLocker(db)
	require.NoError(t, err)
	r := &rig{db: db, adapter: adapter, set: &fakeSet{}, ent: &fakeEntitlements{known: map[string]bool{}}}
	r.purger = &orgpurge.Purger{DB: db, Graph: adapter, Set: r.set, Entitlements: r.ent, Locker: locker}
	return r
}

func (r *rig) run(t *testing.T, org string, opts orgpurge.Options) (orgpurge.Report, error) {
	return r.purger.Run(context.Background(), org, opts)
}

func TestPurgeRemovesOneOrganizationAndNothingElse(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgGone)
	seedPostgres(t, r.db, orgKept)
	seedGraph(t, r.adapter, orgGone)
	seedGraph(t, r.adapter, strings.ToUpper(orgGone))
	seedGraph(t, r.adapter, orgKept)

	beforeGone := rowCounts(t, r.db, orgGone)
	beforeKept := rowCounts(t, r.db, orgKept)
	for _, table := range countedTables() {
		require.NotZero(t, beforeGone[table], "seed must put a row in %s", table)
	}
	require.True(t, graphExists(t, r.adapter, orgGone))
	require.True(t, graphExists(t, r.adapter, orgKept))

	opts := orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGone)}

	dry, err := r.run(t, orgGone, orgpurge.Options{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, beforeGone, rowCounts(t, r.db, orgGone), "dry run must change nothing")
	require.True(t, graphExists(t, r.adapter, orgGone), "dry run must keep the graph")
	require.Equal(t, 2, dry.Graphs, "dry run counts the org's graphs in both UUID spellings")
	require.Len(t, dry.Rows, len(countedTables()), "dry run counts the cascaded children too")
	require.EqualValues(t, 1, beforeGone["oauth_device_grants"])
	require.EqualValues(t, 1, beforeGone["oauth_authorization_requests"])
	for table, n := range dry.Rows {
		want := beforeGone[table]
		if table == "agent_episodes" {
			want-- // the legal_hold episode stays
		}
		require.EqualValues(t, want, n, "dry-run count for %s", table)
	}
	require.EqualValues(t, 1, dry.KeptLegalHold)

	// guards on the real rig
	r.set.members = []string{strings.ToUpper(orgGone)}
	_, err = r.run(t, orgGone, opts)
	require.ErrorIs(t, err, orgpurge.ErrRefused, "an org in the projector set")
	r.set.members = nil
	r.ent.known[orgGone] = true
	_, err = r.run(t, orgGone, opts)
	require.ErrorIs(t, err, orgpurge.ErrRefused, "an org Dev Health still answers for")
	delete(r.ent.known, orgGone)
	require.Equal(t, beforeGone, rowCounts(t, r.db, orgGone), "refusals must change nothing")
	require.True(t, graphExists(t, r.adapter, orgGone))

	report, err := r.run(t, strings.ToUpper(orgGone), opts)
	require.NoError(t, err)
	require.True(t, report.AuditRowWritten)
	require.EqualValues(t, dry.TotalRows, report.TotalRows, "reported total includes the cascaded rows")
	require.EqualValues(t, 1, report.Rows["oauth_device_grants"])
	require.Equal(t, 2, report.Graphs)
	left, err := r.adapter.CountOrganizationGraphs(ctx, orgGone)
	require.NoError(t, err)
	require.Zero(t, left)
	keptGraphs, err := r.adapter.CountOrganizationGraphs(ctx, orgKept)
	require.NoError(t, err)
	require.Equal(t, 1, keptGraphs)

	after := rowCounts(t, r.db, orgGone)
	for _, table := range countedTables() {
		want := int64(0)
		if table == "agent_episodes" {
			want = 1 // legal_hold is kept
		}
		require.Equal(t, want, after[table], "%s after purge", table)
	}
	require.False(t, graphExists(t, r.adapter, orgGone), "graph must be gone")
	require.Equal(t, beforeKept, rowCounts(t, r.db, orgKept), "other org rows untouched")
	require.True(t, graphExists(t, r.adapter, orgKept), "other org graph untouched")

	var audit int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid AND action = 'org_purged'`, orgGone).Scan(&audit))
	require.Equal(t, 1, audit)
	var seeded int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid AND action = 'seed'`, orgGone).Scan(&seeded))
	require.Equal(t, 1, seeded, "audit_events are kept")

	var auditRows int64
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT (metadata->'rows'->>'oauth_device_grants')::int FROM acr.audit_events WHERE org_id = $1::uuid AND action = 'org_purged'`, orgGone).Scan(&auditRows))
	require.EqualValues(t, 1, auditRows, "audit counts include cascaded rows")

	again, err := r.run(t, orgGone, opts)
	require.NoError(t, err)
	require.Zero(t, again.TotalRows, "second run removes nothing")
	require.False(t, again.AuditRowWritten, "second run writes no audit row")
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid AND action = 'org_purged'`, orgGone).Scan(&audit))
	require.Equal(t, 1, audit)
}

func TestEveryOrgKeyedTableIsPurgedOrKept(t *testing.T) {
	r := newRig(t)
	rows, err := r.db.QueryContext(context.Background(), `
SELECT DISTINCT table_name FROM information_schema.columns
WHERE table_schema = 'acr' AND column_name IN ('org_id', 'organization_id', 'authorized_org_id', 'bound_org_id', 'organization_id_hint')`)
	require.NoError(t, err)
	defer rows.Close()
	known := map[string]bool{}
	for _, name := range append(append(orgpurge.PurgedTables(), orgpurge.KeptTables()...), orgpurge.CascadedTables()...) {
		known[name] = true
	}
	var missing []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if !known[name] {
			missing = append(missing, name)
		}
	}
	require.NoError(t, rows.Err())
	sort.Strings(missing)
	require.Empty(t, missing, "org-keyed tables the purge neither deletes nor keeps: %s", strings.Join(missing, ", "))
}

var countWhere = map[string]string{
	"device_authorizations": `authorized_org_id::text = $1 OR ` + looseMatch("organization_id_hint") + `
		OR device_code_hash IN (SELECT device_code_hash FROM acr.oauth_authorization_requests WHERE ` + looseMatch("bound_org_id") + `)`,
	"oauth_authorization_requests": looseMatch("bound_org_id"),
	"oauth_device_grants":          `device_code_hash IN (SELECT device_code_hash FROM acr.device_authorizations WHERE authorized_org_id::text = $1)`,
	"client_credentials":           `org_id::text = $1`, "workload_bindings": `org_id::text = $1`,
	"context_packet_snapshots": `org_id::text = $1`, "agent_episodes": `org_id::text = $1`,
}

func looseMatch(column string) string {
	return "replace(lower(translate(replace(" + column + ", 'urn:uuid:', ''), '{}', '')), '-', '') = replace($1, '-', '')"
}

func countedTables() []string {
	return append(orgpurge.PurgedTables(), orgpurge.CascadedTables()...)
}

func rowCounts(t *testing.T, db *sql.DB, org string) map[string]int64 {
	counts := map[string]int64{}
	for _, table := range countedTables() {
		where := looseMatch("org_id")
		if w, ok := countWhere[table]; ok {
			where = w
		}
		var n int64
		require.NoError(t, db.QueryRowContext(context.Background(), "SELECT count(*) FROM acr."+table+" WHERE "+where, org).Scan(&n))
		counts[table] = n
	}
	return counts
}

func graphExists(t *testing.T, adapter *falkorgraph.Adapter, org string) bool {
	_, err := adapter.ProjectionWatermark(context.Background(), org, graphSource)
	if err == nil {
		return true
	}
	require.ErrorIs(t, err, contextfabric.ErrProjectionWatermarkNotFound)
	return false
}

func TestInterruptedPurgeIsIncompleteAndTheRetryCompletes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	seedPostgres(t, r.db, orgBroken)
	seedGraph(t, r.adapter, orgBroken)
	before := rowCounts(t, r.db, orgBroken)
	opts := orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgBroken)}

	_, err := r.db.ExecContext(ctx, `
CREATE FUNCTION acr.purge_test_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$;
CREATE TRIGGER purge_test_fail BEFORE DELETE ON acr.context_fabric_structure_priors FOR EACH ROW EXECUTE FUNCTION acr.purge_test_fail();`)
	require.NoError(t, err)

	report, err := r.run(t, orgBroken, opts)
	require.ErrorIs(t, err, orgpurge.ErrIncomplete, "a failure after the graph drop must not look like success")
	require.Equal(t, orgpurge.StatusIncomplete, report.Status)
	require.Equal(t, 1, report.Graphs)
	require.Equal(t, before, rowCounts(t, r.db, orgBroken), "the failed transaction must roll back every delete")
	require.False(t, graphExists(t, r.adapter, orgBroken), "the graph drop already happened")

	dry, err := r.run(t, orgBroken, orgpurge.Options{DryRun: true})
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusDryRun, dry.Status)
	require.Positive(t, dry.TotalRows, "the dry run shows the residue")
	require.Zero(t, dry.Graphs)

	_, err = r.db.ExecContext(ctx, `DROP TRIGGER purge_test_fail ON acr.context_fabric_structure_priors`)
	require.NoError(t, err)
	final, err := r.run(t, orgBroken, opts)
	require.NoError(t, err)
	require.Equal(t, orgpurge.StatusPurged, final.Status)
	after := rowCounts(t, r.db, orgBroken)
	for _, table := range countedTables() {
		want := int64(0)
		if table == "agent_episodes" {
			want = 1 // legal_hold stays
		}
		require.Equal(t, want, after[table], "%s after the retry", table)
	}

	for action, want := range map[string]int{"org_purge_incomplete": 1, "org_purged": 1} {
		var n int
		require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid AND action = $2`, orgBroken, action).Scan(&n))
		require.Equal(t, want, n, "audit rows for %s", action)
	}
}

func TestGraphOnlyPurgeIsAudited(t *testing.T) {
	r := newRig(t)
	const orgGraphOnly = "dddddddd-0000-4000-8000-000000000004"
	seedGraph(t, r.adapter, orgGraphOnly)
	report, err := r.run(t, orgGraphOnly, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(orgGraphOnly)})
	require.NoError(t, err)
	require.Equal(t, 1, report.Graphs)
	require.Zero(t, report.TotalRows)
	require.True(t, report.AuditRowWritten, "a purge that only removed a graph is still audited")
	var n int
	require.NoError(t, r.db.QueryRowContext(context.Background(), `SELECT count(*) FROM acr.audit_events WHERE org_id = $1::uuid AND action = 'org_purged'`, orgGraphOnly).Scan(&n))
	require.Equal(t, 1, n)
}

func TestPurgeDurationOnALargeTable(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	const big = "eeeeeeee-0000-4000-8000-000000000005"
	for _, org := range []string{big, orgKept} {
		_, err := r.db.ExecContext(ctx, `
INSERT INTO acr.context_fabric_investigation_results (result_id, org_id, payload, generated_at)
SELECT 'result-' || $1 || '-' || g, $1, '{}', now() FROM generate_series(1, 100000) g`, org)
		require.NoError(t, err)
	}
	started := time.Now()
	report, err := r.run(t, big, orgpurge.Options{ConfirmOrgKey: orgpurge.OrgKey(big)})
	elapsed := time.Since(started)
	require.NoError(t, err)
	require.EqualValues(t, 100000, report.Rows["context_fabric_investigation_results"])
	t.Logf("purge of 100000 rows beside 100000 rows of another organization (investigation_results, 200000 rows scanned by the normalised predicate): %s", elapsed)
	require.Less(t, elapsed, time.Minute)
	var kept int
	require.NoError(t, r.db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_investigation_results WHERE org_id = $1`, orgKept).Scan(&kept))
	require.Equal(t, 100000, kept)
}
