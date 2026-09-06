package devhealthsource_test

// CHAOS-5270: this file's three org-isolation tests each used to start their
// own container -- a small slice of this package's own wall (27.65s of
// 363.09s measured 2026-09-06), unlike devhealthfacts's much larger 41.8%.
// This mirrors the pattern TestOwnershipProducerAgainstRealClickHouse (in
// teams_projects_ownership_integration_test.go) already uses for its own
// nine cases -- that test's own comment: replacing six per-test containers
// fixed Postgres-backed packages starving under -race. It keeps its own
// dedicated container; these three get a second, smaller shared one rather
// than being folded into it, since they are otherwise unrelated to its
// fixture.
//
// Isolation: as with devhealthfacts, sharedTestOrgID derives an
// organization id from t.Name() so tests sharing this container's schema
// stay independent without anyone having to pick a fresh literal by hand.

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var (
	orgIsolationClickHouseOnce      sync.Once
	orgIsolationClickHouseQuery     *runtimeclickhouse.Client
	orgIsolationClickHouseConn      clickhousedriver.Conn
	orgIsolationClickHouseErr       error
	orgIsolationClickHouseTerminate func()
)

// TestMain tears the shared container down once, after every test in the
// package has run, rather than leaving it to whichever test happens to
// touch it first. It costs nothing when orgIsolationClickHouseFixture is
// never called: orgIsolationClickHouseTerminate stays nil.
func TestMain(m *testing.M) {
	code := m.Run()
	if orgIsolationClickHouseTerminate != nil {
		orgIsolationClickHouseTerminate()
	}
	os.Exit(code)
}

// orgIsolationClickHouseFixture returns the container these three
// org-isolation tests share, starting it (and creating sourceSchemaTables)
// on first use.
func orgIsolationClickHouseFixture(t *testing.T) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	orgIsolationClickHouseOnce.Do(func() {
		orgIsolationClickHouseQuery, orgIsolationClickHouseConn, orgIsolationClickHouseTerminate, orgIsolationClickHouseErr = startOrgIsolationClickHouseContainer()
	})
	if orgIsolationClickHouseErr != nil {
		t.Fatalf("start shared ClickHouse container: %v", orgIsolationClickHouseErr)
	}
	return orgIsolationClickHouseQuery, orgIsolationClickHouseConn
}

func startOrgIsolationClickHouseContainer() (*runtimeclickhouse.Client, clickhousedriver.Conn, func(), error) {
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
		return nil, nil, nil, fmt.Errorf("start ClickHouse container: %w", err)
	}
	terminate := func() { _ = container.Terminate(context.Background()) }
	host, err := container.Host(ctx)
	if err != nil {
		terminate()
		return nil, nil, nil, err
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		terminate()
		return nil, nil, nil, err
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err := clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		terminate()
		return nil, nil, nil, fmt.Errorf("open native ClickHouse connection: %w", err)
	}
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			_ = direct.Close()
			terminate()
			return nil, nil, nil, fmt.Errorf("clickhouse not ready for connections: %w", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		_ = direct.Close()
		terminate()
		return nil, nil, nil, fmt.Errorf("open production ClickHouse query client: %w", err)
	}
	for _, statement := range devhealthschema.DDL(sourceSchemaTables...) {
		if err := direct.Exec(ctx, statement); err != nil {
			_ = query.Close()
			_ = direct.Close()
			terminate()
			return nil, nil, nil, fmt.Errorf("create table: %w\n%s", err, statement)
		}
	}
	fullTerminate := func() {
		_ = query.Close()
		_ = direct.Close()
		terminate()
	}
	return query, direct, fullTerminate, nil
}

// sharedTestOrgID gives each test its own organization id, derived from the
// test's own name -- unique by construction, since testing.T.Name() already
// is. See devhealthfacts's own sharedTestOrgID and
// TestChaos5270SharedContainerOrgIsolation for the regression this pattern
// is pinned against (that pin lives in devhealthfacts; it is not repeated
// here since it would exercise the identical property this package's own
// org_id-scoping tests already assert).
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
