package devhealthfacts_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/full-chaos/dev-health-go/readers"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// TestChaos5751WorkItemReaderPhysicalAndMemoryBudgetLimitsAgainstRealClickHouse
// executes the released reader's production work-item statements with each
// independent server-side budget lowered to a tiny, controlled value. The
// existing CHAOS-5751 integration test checks that the shipped settings text
// contains the fixed 10,000-row and 512 MiB ceilings and behaviorally checks
// max_result_rows. This test supplies deliberately lower values only to make
// the physical-row and memory failure paths observable on a two-row fixture;
// it does not claim that the shipped ceilings themselves can be exceeded by
// this small corpus.
func TestChaos5751WorkItemReaderPhysicalAndMemoryBudgetLimitsAgainstRealClickHouse(t *testing.T) {
	ctx := context.Background()
	orgID := sharedTestOrgID(t)
	query, direct := newWorkItemReadLimitFixture(t, ctx)
	at := time.Now().UTC().Truncate(time.Millisecond)
	repoID := "ea198fbc-1945-3717-05d8-eb78866b4e90"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, orgID, "limits/physical-memory", "github", at); err != nil {
		t.Fatalf("seed repository: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, provider, title, status, created_at, updated_at, completed_at, last_synced) VALUES
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?),
		(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"LIMIT-A", repoID, orgID, "github", "limit A", "open", at, at, at, at,
		"LIMIT-B", repoID, orgID, "github", "limit B", "open", at, at, at, at); err != nil {
		t.Fatalf("seed two work items: %v", err)
	}

	ids := []string{repoID + ":LIMIT-A", repoID + ":LIMIT-B"}
	scope := readers.AuthorizationScope{
		RepositorySelectors: &readers.RepositorySelectorScope{
			Granted: readers.RepositorySelectorSet{All: true},
		},
	}
	reads := []struct {
		name string
		call func(readers.Settings) (int, error)
	}{
		{
			name: "status",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemStatusWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
		{
			name: "title",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemTitleWithScopeAndRowLimit(ctx, query, orgID, ids, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
		{
			name: "completion",
			call: func(settings readers.Settings) (int, error) {
				rows, err := readers.ReadWorkItemCompletionWithScopeAndRowLimit(ctx, query, orgID, ids, readers.TimeBound{}, scope, settings, len(ids)+1)
				return len(rows), err
			},
		},
	}

	for _, read := range reads {
		read := read
		t.Run("two-row control completes under independent ceilings/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead:  uint64(len(ids) + 1),
				MaxMemoryUsage: 512 << 20,
				MaxResultRows:  uint64(len(ids) + 1),
			})
			if err != nil {
				t.Fatalf("bounded two-row control error = %v", err)
			}
			if rows != len(ids) {
				t.Fatalf("bounded two-row control rows = %d, want %d", rows, len(ids))
			}
		})

		t.Run("physical-row ceiling throws before result ceiling/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead:  1,
				MaxMemoryUsage: 512 << 20,
				MaxResultRows:  100,
			})
			if err == nil {
				t.Fatalf("physical-row ceiling returned %d rows without an error", rows)
			}
			if rows != 0 {
				t.Fatalf("physical-row ceiling returned %d rows with an error, want 0", rows)
			}
			var serverErr *clickhousedriver.Exception
			if !errors.As(err, &serverErr) {
				t.Fatalf("physical-row ceiling error = %v, want a native ClickHouse exception", err)
			}
			if serverErr.Code != 158 {
				t.Fatalf("physical-row ceiling native error code = %d, want 158 (TOO_MANY_ROWS)", serverErr.Code)
			}
			t.Logf("physical-row budget failure (%s): native_error_code=%d error=%v", read.name, serverErr.Code, err)
		})

		t.Run("memory ceiling throws before result ceiling/"+read.name, func(t *testing.T) {
			rows, err := read.call(readers.Settings{
				MaxRowsToRead: 100,
				// One byte is a principled lower boundary. It proves that this
				// production-shaped reader carries the memory setting to
				// ClickHouse and propagates native enforcement without large
				// fixture data. It does not claim exhaustion of the shipped
				// 512 MiB ceiling.
				MaxMemoryUsage: 1,
				MaxResultRows:  100,
			})
			if err == nil {
				t.Fatalf("memory ceiling returned %d rows without an error", rows)
			}
			if rows != 0 {
				t.Fatalf("memory ceiling returned %d rows with an error, want 0", rows)
			}
			var serverErr *clickhousedriver.Exception
			if !errors.As(err, &serverErr) {
				t.Fatalf("memory ceiling error = %v, want a native ClickHouse exception", err)
			}
			if serverErr.Code != 241 {
				t.Fatalf("memory ceiling native error code = %d, want 241 (MEMORY_LIMIT_EXCEEDED)", serverErr.Code)
			}
			t.Logf("memory budget failure (%s): native_error_code=%d error=%v", read.name, serverErr.Code, err)
		})
	}
}

// newWorkItemReadLimitFixture gives this test its own ClickHouse server and
// production tables. The package-wide fixture is intentionally not used here:
// its default database is shared by many tests, so rows and MergeTree parts
// from earlier tests can consume this test's deliberately tiny physical-read
// budget before the organization and requested IDs filter the result.
func newWorkItemReadLimitFixture(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: chfixture.Image, ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start work-item read-limit ClickHouse container: %v", err)
	}
	containerID := container.GetContainerID()
	t.Logf("work-item read-limit fixture started: container_id=%q", containerID)

	var query *runtimeclickhouse.Client
	var direct clickhousedriver.Conn
	t.Cleanup(func() {
		if query != nil {
			if err := query.Close(); err != nil {
				t.Errorf("close work-item read-limit query client (container_id=%q): %v", containerID, err)
			}
		}
		if direct != nil {
			if err := direct.Close(); err != nil {
				t.Errorf("close work-item read-limit native client (container_id=%q): %v", containerID, err)
			}
		}
		runningBeforeTerminate := container.IsRunning()
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate work-item read-limit ClickHouse container (container_id=%q): %v", containerID, err)
		}
		t.Logf("work-item read-limit fixture terminated: container_id=%q running_before_terminate=%t running_after_terminate=%t", containerID, runningBeforeTerminate, container.IsRunning())
		if container.IsRunning() {
			t.Errorf("work-item read-limit ClickHouse container still running after cleanup (container_id=%q)", containerID)
		}
	})
	if !container.IsRunning() {
		t.Fatalf("work-item read-limit ClickHouse container is not running (container_id=%q)", containerID)
	}

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("resolve work-item read-limit ClickHouse host (container_id=%q): %v", containerID, err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve work-item read-limit ClickHouse port (container_id=%q): %v", containerID, err)
	}
	addr := net.JoinHostPort(host, port.Port())
	direct, err = clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open work-item read-limit native ClickHouse client (container_id=%q): %v", containerID, err)
	}

	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("work-item read-limit ClickHouse did not accept a ping (container_id=%q): %v", containerID, pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Logf("work-item read-limit fixture live: container_id=%q addr=%q", containerID, addr)

	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open work-item read-limit production query client (container_id=%q): %v", containerID, err)
	}
	for _, statement := range devhealthschema.DDL("repos", "work_items") {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create work-item read-limit production table (container_id=%q): %v\n%s", containerID, err, statement)
		}
	}
	return query, direct
}
