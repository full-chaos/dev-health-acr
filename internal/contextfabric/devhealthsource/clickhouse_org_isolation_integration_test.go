package devhealthsource_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
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
	// Rendered from the SHARED production declaration rather than
	// hand-written (CHAOS-3781 round-2 F4). These fixtures previously
	// declared their own types -- DateTime64(6,'UTC') where production is
	// mostly DateTime64(3), repo_id as String where production is UUID --
	// so they proved organization isolation against a schema production
	// does not have, and could not have caught the UInt32 drift that
	// prompted the parity guard in the first place.
	statements := devhealthschema.DDL(sourceSchemaTables...)
	for _, statement := range statements {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	if err := connection.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, collidingRepoID, "org-a", "org-a/service", "github", at); err != nil {
		t.Fatalf("seed org-a repo: %v", err)
	}
	if err := connection.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, collidingRepoID, "org-b", "org-b/other-service", "github", at); err != nil {
		t.Fatalf("seed org-b repo: %v", err)
	}
	if err := connection.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "WI-1", collidingRepoID, "org-a", "Org A task", "open", "", at, ""); err != nil {
		t.Fatalf("seed org-a work item: %v", err)
	}

	// git_pull_requests: both organizations get a PR row sharing the exact
	// same (repo_id, number) key -- the collision codex round-2 finding K1
	// warns about for the reviews->PR join, distinct from the repos-join
	// collision above.
	if err := connection.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, collidingRepoID, "org-a", uint32(1042), "Typed session tokens", "open", at); err != nil {
		t.Fatalf("seed org-a pull request: %v", err)
	}
	if err := connection.Exec(ctx, `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`, collidingRepoID, "org-b", uint32(1042), "Other org's PR", "open", at); err != nil {
		t.Fatalf("seed org-b pull request: %v", err)
	}
	// A single org-a review: if either the reviews->PR join or the PR/review
	// ->repos join is missing its org_id predicate, this row can fan out
	// across org-b's colliding PR/repo rows too (a duplicate-subject
	// contract violation) or silently pick up org-b's slug.
	if err := connection.Exec(ctx, `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`, "review-1", collidingRepoID, "org-a", uint32(1042), "approved", at); err != nil {
		t.Fatalf("seed org-a pull request review: %v", err)
	}

	// ci_pipeline_runs: a single org-a run against the colliding repo_id,
	// proving its repos join stays scoped to org-a even though org-b has a
	// repos row with the same id.
	if err := connection.Exec(ctx, `INSERT INTO ci_pipeline_runs (run_id, repo_id, org_id, branch, status, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, "run-1", collidingRepoID, "org-a", "main", "success", at, at); err != nil {
		t.Fatalf("seed org-a ci run: %v", err)
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
		if entity.Subject.CanonicalID != "work_item.v2:"+collidingRepoID+":WI-1" {
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
		if relationship.From.CanonicalID != "work_item.v2:"+collidingRepoID+":WI-1" || relationship.Type != "BELONGS_TO_REPOSITORY" {
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

	// codex round-2 finding K1: the review->PR->repos join chain and the
	// CI-run->repos join must stay scoped to org-a despite org-b's
	// colliding repo_id (and, for the review, org-b's colliding PR
	// repo_id+number too).
	foundReview := false
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID != "pull_request_review.v2:"+collidingRepoID+":1042:review-1" {
			continue
		}
		foundReview = true
		if len(entity.Authorization.RepositorySlugs) != 1 || entity.Authorization.RepositorySlugs[0] != "org-a/service" {
			t.Fatalf("org-a's review authorization = %+v, want exactly [\"org-a/service\"] -- it must not see org-b's repository", entity.Authorization.RepositorySlugs)
		}
	}
	if !foundReview {
		t.Fatalf("expected org-a's pull request review to be projected: %+v", batch.Entities)
	}

	foundRun := false
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID != "ci_pipeline_run.v2:"+collidingRepoID+":run-1" {
			continue
		}
		foundRun = true
		if len(entity.Authorization.RepositorySlugs) != 1 || entity.Authorization.RepositorySlugs[0] != "org-a/service" {
			t.Fatalf("org-a's CI run authorization = %+v, want exactly [\"org-a/service\"] -- it must not see org-b's repository", entity.Authorization.RepositorySlugs)
		}
	}
	if !foundRun {
		t.Fatalf("expected org-a's CI run to be projected: %+v", batch.Entities)
	}
}

// zeroRepoID is the placeholder Linear-sourced work items carry in
// work_items.repo_id at ingest (CHAOS-3785) -- Linear issues are not tied to
// a single git repo. It never matches a real repos row for any
// organization, by construction.
const zeroRepoID = "00000000-0000-0000-0000-000000000000"

// seedTwoTenantLinearWorkItems seeds two organizations that each mix a
// Linear-shaped work item (zeroRepoID) with a repo-backed one sharing
// collidingRepoID -- the same repos-row collision
// seedTwoTenantRepoIDCollision proves is scoped correctly, but exercised
// through the LEFT JOIN this issue introduces rather than the old INNER
// JOIN. Both organizations reuse the exact same work_item_id strings
// ("LINEAR-1", "LINEAR-2", "REPO-1") -- the natural-key collision CHAOS-3785
// acceptance criterion 2 requires: an org-scoped query must never let one
// organization's row (Linear-shaped or repo-backed) answer for another's,
// regardless of which producer or which JOIN kind reads it.
func seedTwoTenantLinearWorkItems(t *testing.T, ctx context.Context, connection clickhousedriver.Conn, at time.Time) {
	t.Helper()
	// Rendered from the SHARED production declaration rather than
	// hand-written (CHAOS-3781 round-2 F4). These fixtures previously
	// declared their own types -- DateTime64(6,'UTC') where production is
	// mostly DateTime64(3), repo_id as String where production is UUID --
	// so they proved organization isolation against a schema production
	// does not have, and could not have caught the UInt32 drift that
	// prompted the parity guard in the first place.
	statements := devhealthschema.DDL(sourceSchemaTables...)
	for _, statement := range statements {
		if err := connection.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}

	for _, org := range []struct{ id, repoSlug, title string }{
		{"org-a", "org-a/service", "org-a task"},
		{"org-b", "org-b/other-service", "org-b task"},
	} {
		if err := connection.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, collidingRepoID, org.id, org.repoSlug, "github", at); err != nil {
			t.Fatalf("seed %s repo: %v", org.id, err)
		}
		// REPO-1: repo-backed, using the colliding repos id -- proves the
		// LEFT JOIN still resolves to THIS org's repos row, never the other
		// org's, exactly like seedTwoTenantRepoIDCollision's INNER JOIN case.
		if err := connection.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "REPO-1", collidingRepoID, org.id, org.title+" (repo-backed)", "open", "", at, ""); err != nil {
			t.Fatalf("seed %s repo-backed work item: %v", org.id, err)
		}
		// LINEAR-1/LINEAR-2: Linear-shaped, repo_id = zeroRepoID. LINEAR-2 is
		// LINEAR-1's parent (PART_OF); a work_item_dependencies row also
		// BLOCKS LINEAR-1 on LINEAR-2, so the same pair exercises both
		// relaxed-join producers.
		if err := connection.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "LINEAR-2", zeroRepoID, org.id, org.title+" (parent)", "open", "", at, ""); err != nil {
			t.Fatalf("seed %s LINEAR-2: %v", org.id, err)
		}
		if err := connection.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "LINEAR-1", zeroRepoID, org.id, org.title+" (child)", "open", "", at, "LINEAR-2"); err != nil {
			t.Fatalf("seed %s LINEAR-1: %v", org.id, err)
		}
		if err := connection.Exec(ctx, `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, org_id, last_synced) VALUES (?, ?, ?, ?, ?)`, "LINEAR-1", "LINEAR-2", "blocks", org.id, at); err != nil {
			t.Fatalf("seed %s work item dependency: %v", org.id, err)
		}
	}
}

// TestClickHouseProjectionSourceRelaxedRepoJoinStaysOrganizationScoped is
// CHAOS-3785 acceptance criterion 2: queryWorkItems, queryWorkItemDependencies,
// and queryWorkItemHierarchy all relax their repos join from INNER to LEFT
// so Linear-sourced (zero-repo-id) work items stop being silently dropped.
// Relaxing a join is exactly the kind of change that could accidentally
// widen what a query returns; this proves it did not widen ACROSS
// organizations. Two organizations share every work_item_id used here
// (Linear-shaped and repo-backed alike) plus one colliding repos id -- if
// any of the three producers' org_id guard or the repos join's org_id
// equality were missing, org-a's batch would pick up org-b's title, repo
// slug, or an edge that does not belong to it.
func TestClickHouseProjectionSourceRelaxedRepoJoinStaysOrganizationScoped(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	seedTwoTenantLinearWorkItems(t, ctx, direct, at)

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
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation -- a repo-less work item's authorization scope must still be non-empty: %v", err)
	}

	entitiesByID := map[string]contractsv1.ContextFabricEntityProjection{}
	for _, entity := range batch.Entities {
		entitiesByID[entity.Subject.CanonicalID] = entity
		if strings.Contains(entity.Subject.Label, "org-b") {
			t.Fatalf("org-a's batch contains an org-b-labeled entity: %+v", entity)
		}
	}

	linear1, ok := entitiesByID["work_item.v2:"+zeroRepositoryUUID+":LINEAR-1"]
	if !ok {
		t.Fatalf("expected org-a's Linear-shaped work item LINEAR-1 to be projected (this is the CHAOS-3785 zero-row defect): entities=%+v", batch.Entities)
	}
	if linear1.Subject.Label != "org-a task (child)" {
		t.Fatalf("LINEAR-1 label = %q, want org-a's title, not org-b's", linear1.Subject.Label)
	}
	// Pinned to the exact literal value (codex round-1 finding F3), not
	// merely "non-empty and not org-b's slug": a drift in the sentinel
	// devhealthsource actually writes would otherwise pass this assertion
	// silently while breaking anything (this file's own
	// TestLiveRelationshipProjectionNeverDowngradesAnEndpointsOwnAuthorization
	// included) that pins the same literal on the read side.
	if len(linear1.Authorization.RepositorySlugs) != 1 || linear1.Authorization.RepositorySlugs[0] != "acr-context-fabric:no-repository" {
		t.Fatalf("LINEAR-1 authorization = %+v, want exactly [\"acr-context-fabric:no-repository\"]", linear1.Authorization)
	}

	repo1, ok := entitiesByID["work_item.v2:"+collidingRepoID+":REPO-1"]
	if !ok {
		t.Fatalf("expected org-a's repo-backed work item REPO-1 to be projected: entities=%+v", batch.Entities)
	}
	if len(repo1.Authorization.RepositorySlugs) != 1 || repo1.Authorization.RepositorySlugs[0] != "org-a/service" {
		t.Fatalf("REPO-1 authorization = %+v, want exactly [\"org-a/service\"] -- the relaxed LEFT JOIN must still resolve to org-a's repos row, not org-b's colliding one", repo1.Authorization)
	}

	foundBlocks, foundPartOf := false, false
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID != "work_item.v2:"+zeroRepositoryUUID+":LINEAR-1" || relationship.To.CanonicalID != "work_item.v2:"+zeroRepositoryUUID+":LINEAR-2" {
			continue
		}
		switch relationship.Type {
		case contractsv1.ContextFabricRelationshipBlocks:
			foundBlocks = true
		case contractsv1.ContextFabricRelationshipPartOf:
			foundPartOf = true
		}
		if len(relationship.Authorization.RepositorySlugs) == 0 {
			t.Fatalf("relationship %s has an empty authorization scope: %+v", relationship.RelationshipID, relationship)
		}
	}
	if !foundBlocks {
		t.Fatalf("expected a BLOCKS edge LINEAR-1 -> LINEAR-2 (queryWorkItemDependencies through the relaxed join): %+v", batch.Relationships)
	}
	if !foundPartOf {
		t.Fatalf("expected a PART_OF edge LINEAR-1 -> LINEAR-2 (queryWorkItemHierarchy through the relaxed join): %+v", batch.Relationships)
	}

	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "work_item.v2:"+collidingRepoID+":REPO-1" && relationship.Type == "BELONGS_TO_REPOSITORY" {
			if relationship.To.Label != "org-a/service" {
				t.Fatalf("REPO-1's BELONGS_TO_REPOSITORY target = %+v, want org-a's repository, not org-b's", relationship.To)
			}
		}
		if relationship.From.CanonicalID == "work_item.v2:"+zeroRepositoryUUID+":LINEAR-1" && relationship.Type == "BELONGS_TO_REPOSITORY" {
			t.Fatalf("LINEAR-1 has no real repository, so it must never carry a BELONGS_TO_REPOSITORY edge: %+v", relationship)
		}
	}
}

// TestClickHouseProjectionSourceFiltersSelfReferentialParentID is CHAOS-3779
// codex round-1 finding M3's regression test. A work_items row whose
// parent_id equals its own work_item_id is never a legitimate hierarchy
// edge (a work item cannot be its own parent), and
// ContextFabricRelationshipProjection.Validate() unconditionally rejects
// From == To. Because ContextFabricProjectionBatch.Validate() is
// all-or-nothing, one such row would otherwise poison the ENTIRE batch and
// wedge this organization's projection forever -- queryWorkItemHierarchy
// filters it in SQL (parent_id != work_item_id) before it ever becomes a
// candidate. This proves that filter against a real ClickHouse server (a
// fake client does not execute real SQL, so it cannot exercise a
// WHERE-clause fix): a self-referencing row and a legitimately-parented
// row are seeded side by side, and the resulting batch must (a) validate
// cleanly, (b) carry the legitimate PART_OF edge, and (c) carry no edge at
// all for the self-referencing row.
func TestClickHouseProjectionSourceFiltersSelfReferentialParentID(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)

	// Rendered from the SHARED production declaration rather than
	// hand-written (CHAOS-3781 round-2 F4). These fixtures previously
	// declared their own types -- DateTime64(6,'UTC') where production is
	// mostly DateTime64(3), repo_id as String where production is UUID --
	// so they proved organization isolation against a schema production
	// does not have, and could not have caught the UInt32 drift that
	// prompted the parity guard in the first place.
	statements := devhealthschema.DDL(sourceSchemaTables...)
	for _, statement := range statements {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v", err)
		}
	}
	repoID := "22222222-2222-2222-2222-222222222222"
	if err := direct.Exec(ctx, `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`, repoID, "org-m3", "org-m3/service", "github", at); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	// PARENT-1: no parent (root). CHILD-1: legitimately parented by
	// PARENT-1. POISON-1: parent_id equals its own work_item_id -- the
	// self-reference this test exists to prove gets filtered.
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "PARENT-1", repoID, "org-m3", "Parent", "open", "", at, ""); err != nil {
		t.Fatalf("seed parent work item: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "CHILD-1", repoID, "org-m3", "Child", "open", "", at, "PARENT-1"); err != nil {
		t.Fatalf("seed legitimately-parented work item: %v", err)
	}
	if err := direct.Exec(ctx, `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, updated_at, parent_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, "POISON-1", repoID, "org-m3", "Poison", "open", "", at, "POISON-1"); err != nil {
		t.Fatalf("seed self-referencing work item: %v", err)
	}

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-m3", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation -- a self-referencing parent_id row must never reach the batch: %v", err)
	}

	foundLegitimate, foundPoison := false, false
	for _, relationship := range batch.Relationships {
		if relationship.Type != "PART_OF" {
			continue
		}
		switch relationship.From.CanonicalID {
		case "work_item.v2:" + repoID + ":CHILD-1":
			foundLegitimate = true
			if relationship.To.CanonicalID != "work_item.v2:"+repoID+":PARENT-1" {
				t.Fatalf("PART_OF target for CHILD-1 = %q, want work_item.v2:%s:PARENT-1", relationship.To.CanonicalID, repoID)
			}
		case "work_item.v2:" + repoID + ":POISON-1":
			foundPoison = true
		}
	}
	if !foundLegitimate {
		t.Fatalf("expected a PART_OF edge from CHILD-1 to PARENT-1: %+v", batch.Relationships)
	}
	if foundPoison {
		t.Fatalf("a self-referencing PART_OF edge (POISON-1 -> POISON-1) reached the batch -- the SQL filter did not exclude it: %+v", batch.Relationships)
	}
}
