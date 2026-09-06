package devhealthfacts_test

// CHAOS-5270: this package's real-ClickHouse tests used to each start their
// own container -- measured 2026-09-06 at ~160s of this package's ~380s
// wall, almost entirely container boot (the 8.5-11.5s cluster several of
// these tests share is consistent with a near-constant startup cost, not
// query time). This file gives every one of them ONE package-wide
// container instead, mirroring the pattern devhealthsource's
// TestOwnershipProducerAgainstRealClickHouse already uses (its own comment:
// replacing six per-test containers there fixed Postgres-backed packages
// starving under -race).
//
// Isolation: every fact provider this package tests is org_id scoped by
// design (several of these tests exist specifically to prove that), so
// tests sharing one schema stay independent as long as each uses its own
// organization id. sharedTestOrgID derives that id from t.Name(), which the
// testing package already guarantees is unique within a run -- nobody has
// to remember to pick a fresh literal. See
// TestChaos5270SharedContainerOrgIsolation for the regression this is
// pinned against.

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// devhealthschema:not-a-production-replica the table names below select what devhealthschema.DDL renders below; the schema itself
// is the declaration's, not this file's -- the same exemption chaos3780/chaos4099's own DDL calls already carried before CHAOS-5270
// folded their table lists into this one shared union.
//
// sharedClickHouseTables is the UNION of every table this package's
// real-ClickHouse tests need, rendered once at first use rather than once
// per test -- devhealthschema.DDL's CREATE TABLE is not idempotent, so a
// second call for an already-created table errors.
var sharedClickHouseTables = []string{
	"repo_metrics_daily", "compounding_risk_daily", "capacity_forecasts",
	"investment_metrics_daily", "estimate_coverage_metrics_daily", "recommendations_daily",
	"work_unit_investments", "work_item_team_attributions", "repos",
	"projects", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"work_item_metrics_daily",
}

var (
	sharedClickHouseOnce      sync.Once
	sharedClickHouseQuery     *runtimeclickhouse.Client
	sharedClickHouseConn      clickhousedriver.Conn
	sharedClickHouseAddr      string
	sharedClickHouseErr       error
	sharedClickHouseTerminate func()
)

// TestMain exists solely to tear the shared container down once, after
// every test in the package has run, rather than leaving it to whichever
// test happens to touch it first (t.Cleanup only fires when THAT test
// ends). It costs nothing when no real-ClickHouse test runs:
// sharedClickHouseTerminate stays nil unless sharedClickHouseFixture was
// actually called.
func TestMain(m *testing.M) {
	code := m.Run()
	if sharedClickHouseTerminate != nil {
		sharedClickHouseTerminate()
	}
	os.Exit(code)
}

// sharedClickHouseFixture returns this package's one shared ClickHouse
// container's clients, starting it (and creating every table
// sharedClickHouseTables lists) on first use. A failed start fails the
// calling test only -- other tests that never call this are unaffected,
// same blast radius as today's one-container-per-test tests.
func sharedClickHouseFixture(t *testing.T) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	sharedClickHouseOnce.Do(func() {
		sharedClickHouseQuery, sharedClickHouseConn, sharedClickHouseAddr, sharedClickHouseTerminate, sharedClickHouseErr = startSharedClickHouseContainer()
	})
	if sharedClickHouseErr != nil {
		t.Fatalf("start shared ClickHouse container: %v", sharedClickHouseErr)
	}
	return sharedClickHouseQuery, sharedClickHouseConn
}

// sharedClickHouseDSN returns the shared container's connection string, for
// a test that needs to open its OWN client against the same server with
// non-default options (e.g. a custom MaxResultRows) rather than the shared
// query client sharedClickHouseFixture returns.
func sharedClickHouseDSN(t *testing.T) string {
	t.Helper()
	sharedClickHouseFixture(t)
	return "clickhouse://acr:acr@" + sharedClickHouseAddr + "/default"
}

// sharedClickHouseAddrFor returns the shared container's bare host:port, for
// a test asserting an error message never leaks the raw connection address.
func sharedClickHouseAddrFor(t *testing.T) string {
	t.Helper()
	sharedClickHouseFixture(t)
	return sharedClickHouseAddr
}

// logCleanupErr surfaces a cleanup-path error instead of silently discarding
// it (CHAOS-5270 codex round-1 finding F-1). These closures run from
// TestMain's post-m.Run() teardown or mid-setup error unwinding, neither of
// which has a *testing.T in scope, so a logged line -- not t.Logf -- is what
// makes a real leak (a container that failed to terminate, a connection that
// failed to close) diagnosable instead of invisible.
func logCleanupErr(step string, err error) {
	if err != nil {
		log.Printf("chaos5270 shared ClickHouse fixture: %s: %v", step, err)
	}
}

func startSharedClickHouseContainer() (*runtimeclickhouse.Client, clickhousedriver.Conn, string, func(), error) {
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("start ClickHouse container: %w", err)
	}
	terminate := func() { logCleanupErr("terminate container", container.Terminate(context.Background())) }
	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		return nil, nil, "", nil, err
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		terminate()
		return nil, nil, "", nil, err
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		terminate()
		return nil, nil, "", nil, fmt.Errorf("open native ClickHouse connection: %w", err)
	}
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			logCleanupErr("close native connection", direct.Close())
			terminate()
			return nil, nil, "", nil, fmt.Errorf("clickhouse not ready for connections: %w", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		logCleanupErr("close native connection", direct.Close())
		terminate()
		return nil, nil, "", nil, fmt.Errorf("open production ClickHouse query client: %w", err)
	}
	for _, statement := range devhealthschema.DDL(sharedClickHouseTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			logCleanupErr("close query client", query.Close())
			logCleanupErr("close native connection", direct.Close())
			terminate()
			return nil, nil, "", nil, fmt.Errorf("create table: %w\n%s", err, statement)
		}
	}
	fullTerminate := func() {
		logCleanupErr("close query client", query.Close())
		logCleanupErr("close native connection", direct.Close())
		terminate()
	}
	return query, direct, addr, fullTerminate, nil
}

// sharedTestOrgID gives each test its own organization id, derived from the
// test's own name -- unique by construction, since testing.T.Name() already
// is. CHAOS-5270 codex round-1 found that a hand-picked literal org id,
// reused by copy-paste across tests sharing one container, silently
// corrupts an earlier test's data; see
// TestChaos5270SharedContainerOrgIsolation.
func sharedTestOrgID(t *testing.T) string {
	t.Helper()
	return "t5270-" + sanitizeOrgSuffix(t.Name())
}

func sanitizeOrgSuffix(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// TestChaos5270SharedContainerOrgIsolation is CHAOS-5270's isolation pin.
// Two callers sharing the ONE package-wide container, seeding the SAME
// entity keys with different content, must never see each other's rows --
// each must read back only what it itself wrote. This is exactly the shape
// several of chaos4099's nine tests would have hit had they kept sharing
// the literal chaos4099OrgID after moving onto one container. See the PR
// body's TEST-EVIDENCE for the red run this pinned against a naive
// sharedTestOrgID that returned a constant instead of deriving from
// t.Name().
func TestChaos5270SharedContainerOrgIsolation(t *testing.T) {
	ctx := context.Background()
	query, direct := sharedClickHouseFixture(t)

	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const repoID = "99999999-9999-9999-9999-999999999999"
	orgs := map[string]string{}
	titles := map[string]string{}

	seed := func(t *testing.T, title string) {
		t.Helper()
		org := sharedTestOrgID(t)
		orgs[t.Name()] = org
		titles[t.Name()] = title
		if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, org, "isolation-pin/repo", "github", at); err != nil {
			t.Fatalf("seed repo: %v", err)
		}
		if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "WI-PIN", repoID, org, title, "open", "", at, ""); err != nil {
			t.Fatalf("seed work item: %v", err)
		}
	}

	t.Run("caller-a", func(t *testing.T) { seed(t, "caller A's title") })
	t.Run("caller-b", func(t *testing.T) { seed(t, "caller B's title") })

	// A direct, org-scoped read of exactly the row each caller wrote --
	// deliberately not routed through devhealthsource's full projection
	// source, which reads a broader table set unrelated to this isolation
	// property. FINAL matches this codebase's own convention for reading a
	// ReplacingMergeTree table.
	readTitle := func(t *testing.T, org string) string {
		t.Helper()
		rows, err := query.Query(ctx,
			`SELECT title FROM work_items FINAL WHERE org_id = {org:String} AND repo_id = {repo:String} AND work_item_id = {wid:String}`,
			[]contextpacket.ClickHouseBinding{{Name: "org", Value: org}, {Name: "repo", Value: repoID}, {Name: "wid", Value: "WI-PIN"}})
		if err != nil {
			t.Fatalf("query work_items for org %s: %v", org, err)
		}
		defer rows.Close()
		if !rows.Next() {
			t.Fatalf("expected a work_items row for org %s, found none -- its own seed did not persist", org)
		}
		var title string
		if err := rows.Scan(&title); err != nil {
			t.Fatalf("scan title for org %s: %v", org, err)
		}
		if rows.Next() {
			t.Fatalf("expected exactly one work_items row for org %s, found more than one", org)
		}
		return title
	}

	for _, sub := range []string{t.Name() + "/caller-a", t.Name() + "/caller-b"} {
		org, wantTitle := orgs[sub], titles[sub]
		if org == "" {
			t.Fatalf("no recorded org for subtest %s -- it did not run", sub)
		}
		if got := readTitle(t, org); got != wantTitle {
			t.Fatalf("%s (org %s) read back title %q, want its own %q -- another caller's row leaked into this organization's scope", sub, org, got, wantTitle)
		}
	}
}
