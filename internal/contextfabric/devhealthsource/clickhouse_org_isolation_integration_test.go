package devhealthsource_test

import (
	"context"
	"net"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	runtimeclickhouse "github.com/full-chaos/dev-health-acr/internal/runtime/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// collidingRepoID is deliberately reused across two different
// organizations' repos rows below -- the exact shape CHAOS-3753 codex
// finding W1 warns about: any two independently-synced repositories that
// happen to share an id (a non-UUID source system, a replayed sync, two
// tenants onboarded from the same seed data) must never let one tenant's
// row join to the other tenant's repos row.
const collidingRepoID = "11111111-1111-1111-1111-111111111111"

// newDevHealthClickHouseIntegrationClient starts a real (non-TLS)
// ClickHouse container, exposes both the production query client
// (contextpacket.ClickHouseQueryClient, what ClickHouseProjectionSource
// actually reads through) and a raw native connection for seeding fixture
// rows, matching the pattern cmd/acr-api's hosted_clickhouse_fixture_test.go
// established for the TLS-hosted-connection case. This package's own fake
// (fakeClient) cannot execute a real SQL JOIN -- see fakeTable.cursorOf's
// doc comment -- so proving the org_id-scoped join (W1) requires an actual
// ClickHouse server.
func newDevHealthClickHouseIntegrationClient(t *testing.T, ctx context.Context) (query *runtimeclickhouse.Client, direct clickhousedriver.Conn) {
	t.Helper()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "clickhouse/clickhouse-server:24.8", ExposedPorts: []string{"9000/tcp"},
			Env:        map[string]string{"CLICKHOUSE_USER": "acr", "CLICKHOUSE_PASSWORD": "acr", "CLICKHOUSE_DB": "default"},
			WaitingFor: wait.ForListeningPort("9000/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start ClickHouse container: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Errorf("terminate ClickHouse container: %v", err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "9000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort(host, port.Port())

	direct, err = clickhousedriver.Open(&clickhousedriver.Options{
		Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() {
		if err := direct.Close(); err != nil {
			t.Errorf("close native ClickHouse connection: %v", err)
		}
	})
	pingDeadline := time.Now().Add(30 * time.Second)
	for {
		if pingErr := direct.Ping(ctx); pingErr == nil {
			break
		} else if time.Now().After(pingDeadline) {
			t.Fatalf("clickhouse not ready for connections: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	query, err = runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{
		DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open production ClickHouse query client: %v", err)
	}
	t.Cleanup(func() {
		if err := query.Close(); err != nil {
			t.Errorf("close production ClickHouse query client: %v", err)
		}
	})
	return query, direct
}

// seedTwoTenantRepoIDCollision creates every table entityTables (tables.go)
// reads from -- all but repos/work_items stay empty, since this test's
// focus is that one join, not full coverage -- and inserts one colliding
// repos row per organization plus one work item that belongs to org-a.
func seedTwoTenantRepoIDCollision(t *testing.T, ctx context.Context, connection clickhousedriver.Conn, at time.Time) {
	t.Helper()
	statements := []string{
		// ORDER BY (org_id, id), not id alone: this test deliberately inserts
		// two repos rows sharing one id across two organizations (the exact
		// collision W1 guards against), and ReplacingMergeTree treats rows
		// with an identical sorting key as versions of the same logical row --
		// ORDER BY id alone would let ClickHouse collapse the two tenants'
		// rows into one under FINAL, before the query under test even runs.
		`CREATE TABLE repos (id String, org_id String, repo String, provider Nullable(String), last_synced DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (org_id, id)`,
		`CREATE TABLE work_items (work_item_id String, repo_id String, org_id String, title Nullable(String), status Nullable(String), url Nullable(String), updated_at DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY work_item_id`,
		`CREATE TABLE git_pull_requests (repo_id String, org_id String, number Int64, title Nullable(String), state Nullable(String), last_synced DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (repo_id, number)`,
		`CREATE TABLE deployments (repo_id String, org_id String, deployment_id String, status Nullable(String), environment Nullable(String), deployed_at Nullable(DateTime64(6, 'UTC')), started_at Nullable(DateTime64(6, 'UTC')), last_synced DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY deployment_id`,
		`CREATE TABLE operational_incidents (id String, org_id String, service_id String, title Nullable(String), normalized_status Nullable(String), raw_status Nullable(String), normalized_severity Nullable(String), raw_severity Nullable(String), started_at Nullable(DateTime64(6, 'UTC')), source_event_at Nullable(DateTime64(6, 'UTC')), observed_at DateTime64(6, 'UTC'), is_deleted UInt8) ENGINE = ReplacingMergeTree ORDER BY id`,
		`CREATE TABLE operational_service_repository_mappings (org_id String, service_id String, repo_id String, is_active UInt8) ENGINE = ReplacingMergeTree ORDER BY (org_id, service_id, repo_id)`,
		`CREATE TABLE work_item_dependencies (source_work_item_id String, target_work_item_id String, relationship_type Nullable(String), org_id String, last_synced DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY (source_work_item_id, target_work_item_id)`,
		`CREATE TABLE work_graph_deployment_incident_edges (edge_id String, deployment_id String, incident_id String, repo_id String, org_id UUID, observed_at DateTime64(6, 'UTC')) ENGINE = ReplacingMergeTree ORDER BY edge_id`,
	}
	for _, statement := range statements {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if err := connection.Exec(ctx, `INSERT INTO repos VALUES (?, ?, ?, ?, ?)`, collidingRepoID, "org-a", "org-a/service", "github", at); err != nil {
		t.Fatalf("seed org-a repo: %v", err)
	}
	if err := connection.Exec(ctx, `INSERT INTO repos VALUES (?, ?, ?, ?, ?)`, collidingRepoID, "org-b", "org-b/other-service", "github", at); err != nil {
		t.Fatalf("seed org-b repo: %v", err)
	}
	if err := connection.Exec(ctx, `INSERT INTO work_items VALUES (?, ?, ?, ?, ?, ?, ?)`, "WI-1", collidingRepoID, "org-a", "Org A task", "open", "", at); err != nil {
		t.Fatalf("seed org-a work item: %v", err)
	}
}

// TestClickHouseProjectionSourceScopesTheRepositoryJoinByOrganization is
// CHAOS-3753 codex finding W1's regression test: "org_id in EVERY join".
// Two organizations' repos rows share the exact same id; org-a's work item
// must join to org-a's repos row (and pick up org-a's slug/authorization),
// never org-b's, regardless of which row a plain "ON r.id = w.repo_id" join
// would nondeterministically prefer.
func TestClickHouseProjectionSourceScopesTheRepositoryJoinByOrganization(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedTwoTenantRepoIDCollision(t, ctx, direct, at)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-a", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	foundEntity := false
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID != "work_item:WI-1" {
			continue
		}
		foundEntity = true
		if len(entity.Authorization.RepositorySlugs) != 1 || entity.Authorization.RepositorySlugs[0] != "org-a/service" {
			t.Fatalf("org-a's work item authorization = %+v, want exactly [\"org-a/service\"] -- it must not see org-b's repository", entity.Authorization.RepositorySlugs)
		}
	}
	if !foundEntity {
		t.Fatalf("expected org-a's work item to be projected: %+v", batch.Entities)
	}

	foundRelationship := false
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID != "work_item:WI-1" || relationship.Type != "BELONGS_TO_REPOSITORY" {
			continue
		}
		foundRelationship = true
		if relationship.To.CanonicalID != "repository:"+collidingRepoID || relationship.To.Label != "org-a/service" {
			t.Fatalf("BELONGS_TO_REPOSITORY target = %+v, want the org-a repository, not org-b's", relationship.To)
		}
		if len(relationship.Authorization.RepositorySlugs) != 1 || relationship.Authorization.RepositorySlugs[0] != "org-a/service" {
			t.Fatalf("BELONGS_TO_REPOSITORY authorization = %+v, want exactly [\"org-a/service\"]", relationship.Authorization.RepositorySlugs)
		}
	}
	if !foundRelationship {
		t.Fatalf("expected org-a's work item to carry a BELONGS_TO_REPOSITORY relationship: %+v", batch.Relationships)
	}
}
