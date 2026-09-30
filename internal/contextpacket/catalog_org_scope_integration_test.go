package contextpacket_test

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// CHAOS-7237 on a real ClickHouse, through the packet path context_for_task
// runs (CatalogClickHouseRows.EvidenceRows over the whole catalog): two
// organizations hold the SAME repository UUID and slug, as they do when both
// sync one repository (ops mints repos.id from the repository name). Each
// organization's packet read returns its own rows only, one per statement,
// and never a row of the other.

const (
	scopeRepoID  = "20000000-0000-4000-8000-000000000002"
	scopeRepo    = "acme/api"
	scopeOrgA    = "10000000-0000-4000-8000-00000000000a"
	scopeOrgB    = "10000000-0000-4000-8000-00000000000b"
	scopeForeign = "FOREIGN"
)

func startScopeClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
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
	direct, err := clickhousedriver.Open(&clickhousedriver.Options{Addr: []string{addr}, Auth: clickhousedriver.Auth{Database: "default", Username: "acr", Password: "acr"}, DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open native ClickHouse connection: %v", err)
	}
	t.Cleanup(func() { _ = direct.Close() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		pingErr := direct.Ping(ctx)
		if pingErr == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("clickhouse not ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	query, err := runtimeclickhouse.NewClickHouseQueryClientWithOptions(runtimeclickhouse.Options{DSN: "clickhouse://acr:acr@" + addr + "/default", DialTimeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("open query client: %v", err)
	}
	t.Cleanup(func() { _ = query.Close() })
	return query, direct
}

func scopeExec(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, statement string) {
	t.Helper()
	if err := direct.Exec(ctx, statement); err != nil {
		t.Fatalf("exec: %v\n%s", err, statement)
	}
}

// seedTwoOrganizations writes, for each organization, one row of every
// repository-joined catalog statement under the shared repository UUID;
// organization B's rows carry the FOREIGN marker in a column the packet
// shows, and some collide with A's keys (PR 532 and its review, commit
// c0ffee and its file).
func seedTwoOrganizations(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL() {
		scopeExec(t, ctx, direct, statement)
	}
	// devhealthschema does not declare the commit tables: the ops
	// definitions (migrations/clickhouse/000_raw_tables.sql) with the
	// org_id column and sorting key of ops migration 027.
	scopeExec(t, ctx, direct, `CREATE TABLE git_commits (org_id String, repo_id UUID, hash String, message Nullable(String), author_name Nullable(String), author_email Nullable(String), author_when DateTime64(3, 'UTC'), committer_name Nullable(String), committer_email Nullable(String), committer_when DateTime64(3, 'UTC'), parents UInt32, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, hash)`)
	scopeExec(t, ctx, direct, `CREATE TABLE git_commit_stats (org_id String, repo_id UUID, commit_hash String, file_path String, additions Int32, deletions Int32, old_file_mode String, new_file_mode String, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, commit_hash, file_path)`)
	now := "'2026-09-01 12:00:00.000'"
	for _, org := range []struct{ id, marker string }{{scopeOrgA, "own"}, {scopeOrgB, scopeForeign}} {
		m := org.marker
		for _, statement := range []string{
			fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, scopeRepoID, scopeRepo, now, now, org.id),
			fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%s', 532, '%s PR', 'merged', %s, %s, '%s')`, scopeRepoID, m, now, now, org.id),
			fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) VALUES ('%s', 532, 'rev-%s', '%s_APPROVED', %s, %s, '%s')`, scopeRepoID, m, m, now, now, org.id),
			fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'run-%s', 'success', %s, %s, '%s')`, scopeRepoID, m, now, now, org.id),
			fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, release_ref, release_ref_confidence, last_synced, org_id) VALUES ('%s', 'dep-%s', 'success', '%s-env', %s, %s, 'v1', 1.0, %s, '%s')`, scopeRepoID, m, m, now, now, now, org.id),
			fmt.Sprintf(`INSERT INTO git_commits (org_id, repo_id, hash, message, author_when, committer_when, parents, last_synced) VALUES ('%s', '%s', 'c0ffee', '%s commit', %s, %s, 1, %s)`, org.id, scopeRepoID, m, now, now, now),
			fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%s', '%s', 'c0ffee', '%s.go', 1, 2, %s)`, org.id, scopeRepoID, m, now),
		} {
			scopeExec(t, ctx, direct, statement)
		}
	}
}

// joinedStatements are the catalog statements that join a table to repos:
// each must read exactly one row per organization here.
var joinedStatements = []string{"pull_requests.v1", "pull_request_reviews.v1", "ci_pipeline_runs.v1", "deployments.v1", "git_commits.v1", "git_commit_files.v1"}

func TestPacketCatalogReadsOnlyTheCallersOrganizationUnderASharedRepositoryID(t *testing.T) {
	ctx := context.Background()
	query, direct := startScopeClickHouse(t, ctx)
	seedTwoOrganizations(t, ctx, direct)
	rows := contextpacket.NewCatalogClickHouseRows(query)
	for _, org := range []struct {
		id      string
		foreign bool
	}{{scopeOrgA, false}, {scopeOrgB, true}} {
		plan := contextpacket.ReadPlan{OrgID: org.id, RepoID: scopeRepoID, RepoSlug: scopeRepo}
		evidence, _, unavailable, err := rows.EvidenceRows(ctx, plan)
		if err != nil {
			t.Fatalf("org %s: %v", org.id, err)
		}
		joined := map[string]bool{}
		for _, id := range joinedStatements {
			joined[id] = true
		}
		counts := map[string]int{}
		for _, ref := range evidence {
			if !joined[ref.SourceVersion] {
				continue
			}
			counts[ref.SourceVersion]++
			text := ref.Source.DisplayLabel + " " + ref.Citation + " " + ref.Source.EntityID
			if strings.Contains(text, scopeForeign) != org.foreign {
				t.Errorf("org %s read the other organization's %s row: %q", org.id, ref.SourceVersion, text)
			}
		}
		for _, id := range joinedStatements {
			if counts[id] != 1 {
				t.Errorf("org %s: %s returned %d rows, want exactly its own one (unavailable: %v)", org.id, id, counts[id], unavailable)
			}
			// The evidence-expansion read (ResolveEvidenceReference, the
			// packet handle's own statement) sees every joined row without
			// the packet's id dedupe: one row, this organization's own.
			references, err := rows.ResolveEvidenceReference(ctx, org.id, contractsv1.ResolvedScope{RepoID: scopeRepoID, RepoSlug: scopeRepo}, id, "")
			if err != nil {
				t.Fatalf("org %s: %s expansion read: %v", org.id, id, err)
			}
			if len(references) != 1 {
				t.Errorf("org %s: %s expansion read returned %d rows, want exactly its own one", org.id, id, len(references))
			}
			for _, reference := range references {
				text := reference.Evidence.Source.DisplayLabel + " " + reference.Evidence.Citation + " " + reference.Evidence.Source.EntityID
				if strings.Contains(text, scopeForeign) != org.foreign {
					t.Errorf("org %s: %s expansion read returned the other organization's row: %q", org.id, id, text)
				}
			}
		}
	}
}
