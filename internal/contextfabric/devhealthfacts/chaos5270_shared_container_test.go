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
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	dockercontainer "github.com/moby/moby/api/types/container"
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

// CHAOS-5653: a shared testcontainer's own death is otherwise invisible --
// a caller only ever sees the query-side symptom (ClickHouse EOF /
// connection refused), never the container-side cause (OOM kill, a resource
// limit, or something else). This is a DIAGNOSTIC ONLY: no retry, no change
// to what any test asserts or how many times it runs. It buffers the
// container's own stdout/stderr and, the first time the shared container is
// observed no longer running, dumps that buffered tail plus the container's
// inspected State (OOMKilled/ExitCode/FinishedAt), the host's free memory,
// and the container's own memory stats -- so a death names its cause
// instead of surfacing as an unexplained EOF.
const clickHouseDeathMarker = "clickhouse_container_death"

var (
	sharedClickHouseDeathBuf  = newDeathLogBuffer(200)
	sharedClickHouseDeathOnce sync.Once
	sharedClickHouseStartedAt time.Time
	sharedClickHouseTestCount int64
	sharedClickHouseWatchStop chan struct{}
)

// deathLogBuffer is a testcontainers.LogConsumer that keeps only the last
// max lines it has seen, so a dump taken at death time carries a bounded
// tail rather than the whole run's output.
type deathLogBuffer struct {
	mu    sync.Mutex
	max   int
	lines []string
}

func newDeathLogBuffer(max int) *deathLogBuffer { return &deathLogBuffer{max: max} }

func (b *deathLogBuffer) Accept(l testcontainers.Log) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, line := range strings.Split(strings.TrimRight(string(l.Content), "\n"), "\n") {
		if line == "" {
			continue
		}
		b.lines = append(b.lines, line)
	}
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

func (b *deathLogBuffer) tail() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// containerDeathReport is the plain-data shape of one death dump, kept
// separate from how it's produced so the guard test can drive it with fake
// data instead of a real dying container.
type containerDeathReport struct {
	Trigger        string
	LogTail        []string
	OOMKilled      bool
	ExitCode       int
	FinishedAt     string
	HostFreeM      string
	ContainerStats string
	FixtureUptime  time.Duration
	TestCount      int64
}

// formatContainerDeathReport renders a report under the clickHouseDeathMarker
// line so it's grep-able out of a hosted CI log.
func formatContainerDeathReport(r containerDeathReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s trigger=%q oom_killed=%t exit_code=%d finished_at=%q fixture_uptime=%s test_count=%d\n",
		clickHouseDeathMarker, r.Trigger, r.OOMKilled, r.ExitCode, r.FinishedAt, r.FixtureUptime, r.TestCount)
	markerBlock(&b, "host_free_m", r.HostFreeM)
	markerBlock(&b, "container_stats", r.ContainerStats)
	fmt.Fprintf(&b, "%s log_tail lines=%d\n", clickHouseDeathMarker, len(r.LogTail))
	for _, line := range r.LogTail {
		fmt.Fprintf(&b, "%s log> %s\n", clickHouseDeathMarker, line)
	}
	return b.String()
}

// markerBlock writes a labelled, possibly multi-line block with every line
// -- including each line of the block's own body -- prefixed by
// clickHouseDeathMarker, so the whole report stays grep-able by that one
// marker out of a hosted CI log with no unmarked lines in between.
func markerBlock(b *strings.Builder, label, body string) {
	fmt.Fprintf(b, "%s %s:\n", clickHouseDeathMarker, label)
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(b, "%s %s> %s\n", clickHouseDeathMarker, label, line)
	}
}

// hostFreeM and containerMemStats shell out for the two pieces of diagnostic
// state Go's stdlib has no portable API for; a failure to collect either is
// itself reported rather than aborting the dump.
func hostFreeM() string {
	out, err := exec.Command("free", "-m").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("free -m failed: %v", err)
	}
	return strings.TrimRight(string(out), "\n")
}

func containerMemStats(containerID string) string {
	if containerID == "" {
		return "no container id available"
	}
	out, err := exec.Command("docker", "stats", "--no-stream", "--no-trunc", containerID).CombinedOutput()
	if err != nil {
		return fmt.Sprintf("docker stats failed: %v", err)
	}
	return strings.TrimRight(string(out), "\n")
}

// reportClickHouseContainerDeath assembles and emits one death report. It
// takes its state as parameters rather than reaching for globals so the
// guard test can call it directly with a fabricated death.
func reportClickHouseContainerDeath(logf func(string, ...any), buf *deathLogBuffer, state *dockercontainer.State, containerID string, startedAt time.Time, testCount int64, trigger string) {
	var oomKilled bool
	var exitCode int
	var finishedAt string
	if state != nil {
		oomKilled = state.OOMKilled
		exitCode = state.ExitCode
		finishedAt = state.FinishedAt
	}
	logf("%s", formatContainerDeathReport(containerDeathReport{
		Trigger:        trigger,
		LogTail:        buf.tail(),
		OOMKilled:      oomKilled,
		ExitCode:       exitCode,
		FinishedAt:     finishedAt,
		HostFreeM:      hostFreeM(),
		ContainerStats: containerMemStats(containerID),
		FixtureUptime:  time.Since(startedAt),
		TestCount:      testCount,
	}))
}

// deathProbeTimeout bounds each tick's State inspect and TCP dial so one
// slow probe can never stall the watcher past its next tick.
const deathProbeTimeout = 3 * time.Second

// containerStateProber is the narrow slice of testcontainers.Container the
// watcher actually calls -- real *testcontainers.DockerContainer satisfies
// it structurally, and a small fake satisfies it in tests instead of
// stubbing the whole 20-method Container interface.
type containerStateProber interface {
	State(ctx context.Context) (*dockercontainer.State, error)
	GetContainerID() string
}

// watchClickHouseContainerDeath probes the shared container's ACTUAL state
// every tick -- a real docker inspect (container.State, which calls docker
// inspect live, testcontainers-go@v0.43.0 docker.go:479-485) AND a raw TCP
// dial to the mapped ClickHouse port -- rather than
// testcontainers.Container.IsRunning(), which is a cached atomic bool
// (docker.go:119-121) cleared ONLY by this process's own Stop()/Terminate()
// calls (docker.go:308, :366): nothing updates it when the container dies
// on its own, so it can never observe an out-of-band death. Live inspect
// state and a TCP dial are both signals that DO reflect what actually
// happened, independent of anything this process called.
//
// Either signal going bad on TWO CONSECUTIVE ticks reports the death once;
// a single bad tick is a transient blip (host contention, a slow inspect
// under load), not evidence of death, so momentary pressure never
// false-fires. An intentional teardown closes stop first, so a normal
// shutdown never reports as a death.
func watchClickHouseContainerDeath(container containerStateProber, addr string, stop <-chan struct{}, buf *deathLogBuffer, startedAt time.Time, once *sync.Once, testCount func() int64, logf func(string, ...any)) {
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	consecutiveBad := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			select {
			case <-stop:
				return
			default:
			}

			ctx, cancel := context.WithTimeout(context.Background(), deathProbeTimeout)
			state, stateErr := container.State(ctx)
			cancel()
			dialErr := probeTCPDial(addr, deathProbeTimeout)

			if stateErr == nil && state.Running && !isConnectionRefused(dialErr) {
				consecutiveBad = 0
				continue
			}
			consecutiveBad++
			if consecutiveBad < 2 {
				continue
			}
			once.Do(func() {
				reportClickHouseContainerDeath(
					logf,
					buf,
					state,
					container.GetContainerID(),
					startedAt,
					testCount(),
					deathTrigger(stateErr, state, dialErr),
				)
			})
			return
		}
	}
}

// probeTCPDial reports whether addr refused (or otherwise failed) a live
// TCP connection attempt, closing the connection immediately on success --
// this is a liveness probe only, never a real client.
func probeTCPDial(addr string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	return conn.Close()
}

// isConnectionRefused reports whether err is shaped like nothing is
// listening on the port anymore -- the exact signature every observed
// hosted-CI death has carried ("connect: connection refused") -- rather
// than a generic dial error a merely slow, still-alive host could also
// produce (a timeout under load), which the 2-consecutive-ticks debounce
// above already guards against separately.
func isConnectionRefused(err error) bool {
	return err != nil && (errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused"))
}

// deathTrigger names which probe actually caught the death, so a reader of
// the dump knows whether the container itself stopped running or only its
// ClickHouse server/port went unreachable while docker still reports the
// container as running.
func deathTrigger(stateErr error, state *dockercontainer.State, dialErr error) string {
	switch {
	case stateErr != nil:
		return "container_state_inspect_failed"
	case state != nil && !state.Running:
		return "container_not_running"
	case isConnectionRefused(dialErr):
		return "clickhouse_port_refused"
	default:
		return "unknown"
	}
}

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
	atomic.AddInt64(&sharedClickHouseTestCount, 1)
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
	sharedClickHouseStartedAt = time.Now()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
			LogConsumerCfg: &testcontainers.LogConsumerConfig{
				Consumers: []testcontainers.LogConsumer{sharedClickHouseDeathBuf},
			},
		},
		Started: true,
	})
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("start ClickHouse container: %w", err)
	}
	sharedClickHouseWatchStop = make(chan struct{})
	terminate := func() {
		close(sharedClickHouseWatchStop)
		logCleanupErr("terminate container", container.Terminate(context.Background()))
	}
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
	// The watcher needs addr for its own TCP probe, so it starts only once
	// the mapped port is known -- nothing meaningful to watch before that,
	// and a Host/MappedPort failure above never leaves an orphaned watcher.
	go watchClickHouseContainerDeath(container, addr, sharedClickHouseWatchStop, sharedClickHouseDeathBuf, sharedClickHouseStartedAt,
		&sharedClickHouseDeathOnce, func() int64 { return atomic.LoadInt64(&sharedClickHouseTestCount) }, log.Printf)

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
