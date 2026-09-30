package sourcerow_test

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/chfixture"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/dependencyrelation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The source-row statements against a real ClickHouse holding only the
// tables devhealthschema declares, seeded with one row for each of the 7
// source kinds the way the producers read them, and rows of the excluded
// kinds. It proves the SQL (the repository lookup, the incident discovery
// and the seven catalog statements the plans name, each filtered to one
// evidence id) and, on the real engine:
//   - the excluded kinds (non-injective grammar or non-key id; r2 P1) are
//     never read as a source row, codex r2's collision seed included;
//   - organization isolation: another organization's rows under a
//     colliding repository UUID are never served (codex r1 P1);
//   - an expired incident mapping never crowds out a current one (r1 P2);
//   - equal refusal: a restricted caller's refused ref and the same ref with
//     its rows deleted run the same statements with the same bindings.

const (
	integrationOrg = "10000000-0000-4000-8000-000000000001"
	// foreignOrg holds rows under grantedID too: repository UUIDs are keyed
	// (org_id, id), so two organizations may carry the same one.
	foreignOrg = "10000000-0000-4000-8000-00000000000b"
)

// recordingClient records every statement and its bindings on the way to
// the real client.
type recordingClient struct {
	inner contextpacket.ClickHouseQueryClient
	log   []string
}

func (c *recordingClient) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	c.log = append(c.log, fmt.Sprintf("%s %v", statement, bindings))
	return c.inner.Query(ctx, statement, bindings)
}

func startClickHouse(t *testing.T, ctx context.Context) (*runtimeclickhouse.Client, clickhousedriver.Conn) {
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

func exec(t *testing.T, ctx context.Context, direct clickhousedriver.Conn, statement string) {
	t.Helper()
	if err := direct.Exec(ctx, statement); err != nil {
		t.Fatalf("exec: %v\n%s", err, statement)
	}
}

func seedIntegration(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL() {
		exec(t, ctx, direct, statement)
	}
	// devhealthschema does not declare the commit tables; these are the ops
	// definitions (migrations/clickhouse/000_raw_tables.sql) with the org_id
	// column and sorting key of migration 027.
	exec(t, ctx, direct, `CREATE TABLE git_commits (org_id String, repo_id UUID, hash String, message Nullable(String), author_name Nullable(String), author_email Nullable(String), author_when DateTime64(3, 'UTC'), committer_name Nullable(String), committer_email Nullable(String), committer_when DateTime64(3, 'UTC'), parents UInt32, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, hash)`)
	exec(t, ctx, direct, `CREATE TABLE git_commit_stats (org_id String, repo_id UUID, commit_hash String, file_path String, additions Int32, deletions Int32, old_file_mode String, new_file_mode String, last_synced DateTime64(3, 'UTC')) ENGINE = ReplacingMergeTree(last_synced) ORDER BY (org_id, repo_id, commit_hash, file_path)`)
	now := "'2026-09-01 12:00:00.000'"
	past := "'2026-01-01 00:00:00.000000'"
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github'), ('%s', '%s', %s, %s, '%s', 'github')`,
			grantedID, grantedRep, now, now, integrationOrg, secretID, secretRep, now, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES
			('%[1]s', 'jira:ABC-1', 'jira', 'Fix login', 'open', %[3]s, %[3]s, 'jira:ABC-0', %[3]s, '%[4]s'),
			('%[1]s', 'jira:ABC-0', 'jira', 'Login epic', 'open', %[3]s, %[3]s, '', %[3]s, '%[4]s'),
			('%[2]s', 'jira:SEC-1', 'jira', 'Secret', 'open', %[3]s, %[3]s, '', %[3]s, '%[4]s')`, grantedID, secretID, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('jira:ABC-1', 'jira:ABC-0', 'blocks', 'Blocks', %s, '%s')`, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO teams (id, name, updated_at, org_id, provider, is_active) VALUES ('team-a', 'Team A', %s, '%s', 'jira', 1)`, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES ('%s', '00000000-0000-0000-0000-000000000000', 'jira:ABC-1', 'team-a', 'native_team', 1, 'high', %s)`, integrationOrg, now),
		fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%s', 532, 'Fix the widget', 'merged', %s, %s, '%s')`, grantedID, now, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) VALUES ('%s', 532, 'rev-9', 'APPROVED', %s, %s, '%s')`, grantedID, now, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'run-1', 'success', %s, %s, '%s')`, grantedID, now, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, last_synced, org_id) VALUES ('%s', 'dep-1', 'success', 'production', %s, %s, %s, '%s')`, grantedID, now, now, now, integrationOrg),
		// The foreign organization: the SAME repository UUID and slug, rows of
		// every one of the 11 source kinds that only it owns, and rows whose
		// ids collide with this organization's own (work items ABC-0/ABC-1,
		// their dependency, PR 532).
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github')`, grantedID, grantedRep, now, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES
			('%[1]s', 'jira:ABC-1', 'jira', 'FOREIGN login', 'open', %[2]s, %[2]s, 'jira:ABC-0', %[2]s, '%[3]s'),
			('%[1]s', 'jira:ABC-0', 'jira', 'FOREIGN epic', 'open', %[2]s, %[2]s, '', %[2]s, '%[3]s'),
			('%[1]s', 'jira:FOR-1', 'jira', 'FOREIGN item', 'open', %[2]s, %[2]s, 'jira:FOR-0', %[2]s, '%[3]s'),
			('%[1]s', 'jira:FOR-0', 'jira', 'FOREIGN parent', 'open', %[2]s, %[2]s, '', %[2]s, '%[3]s')`, grantedID, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('jira:ABC-1', 'jira:ABC-0', 'blocks', 'Blocks', %[1]s, '%[2]s'), ('jira:FOR-1', 'jira:FOR-0', 'blocks', 'Blocks', %[1]s, '%[2]s')`, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO teams (id, name, updated_at, org_id, provider, is_active) VALUES ('team-b', 'Team B', %[1]s, '%[2]s', 'jira', 1), ('team-a', 'FOREIGN Team A', %[1]s, '%[2]s', 'jira', 1)`, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO projects (id, org_id, provider, name, is_active, state, url, updated_at) VALUES ('PROJ-1', '%[2]s', 'jira', 'FOREIGN project', 1, 'active', '', %[1]s), ('PROJ-F', '%[2]s', 'jira', 'FOREIGN only project', 1, 'active', '', %[1]s), ('PROJ-1', '%[3]s', 'jira', 'Own project', 1, 'active', '', %[1]s)`, now, foreignOrg, integrationOrg),
		fmt.Sprintf(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES ('%s', '00000000-0000-0000-0000-000000000000', 'jira:FOR-1', 'team-b', 'native_team', 1, 'high', %s)`, foreignOrg, now),
		fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%s', %s, 'INC-F', %s, %s, 'svc-f', 'FOREIGN outage', %s, 0)`, foreignOrg, past, past, past, past),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%s', %s, 'map-f', 'native', 0.9, 'svc-f', '%s', %s, 1)`, foreignOrg, past, grantedID, past),
		fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES ('edge-f', '%s', 'FOREIGN-DEP-990', 'INC-F', '%s', 0.9, 'native', 'foreign', %s, %s)`, foreignOrg, grantedID, now, now),
		// Its PR 532 collides with this organization's PR 532 on (repo, number).
		fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%[1]s', 990, 'FOREIGN PR 990', 'open', %[2]s, %[2]s, '%[3]s'), ('%[1]s', 532, 'FOREIGN PR 532', 'open', %[2]s, %[2]s, '%[3]s')`, grantedID, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) VALUES ('%s', 990, 'FOREIGN-REVIEW-990', 'APPROVED', %s, %s, '%s')`, grantedID, now, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'FOREIGN-CI-990', 'failed', %s, %s, '%s')`, grantedID, now, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, last_synced, org_id) VALUES ('%s', 'FOREIGN-DEP-990', 'success', 'private-env', %s, %s, %s, '%s')`, grantedID, now, now, now, foreignOrg),
		// Commits: this organization's c0ffee, and the foreign organization's
		// c0ffee (the same hash under the same repository UUID) and f0e1gn.
		fmt.Sprintf(`INSERT INTO git_commits (org_id, repo_id, hash, message, author_when, committer_when, parents, last_synced) VALUES ('%[1]s', '%[3]s', 'c0ffee', 'own commit', %[4]s, %[4]s, 1, %[4]s), ('%[2]s', '%[3]s', 'c0ffee', 'FOREIGN commit', %[4]s, %[4]s, 1, %[4]s), ('%[2]s', '%[3]s', 'f0e1gn', 'FOREIGN only', %[4]s, %[4]s, 1, %[4]s)`, integrationOrg, foreignOrg, grantedID, now),
		fmt.Sprintf(`INSERT INTO git_commit_stats (org_id, repo_id, commit_hash, file_path, additions, deletions, last_synced) VALUES ('%[1]s', '%[3]s', 'c0ffee', 'main.go', 1, 2, %[4]s), ('%[2]s', '%[3]s', 'c0ffee', 'main.go', 7, 8, %[4]s), ('%[2]s', '%[3]s', 'f0e1gn', 'secret.go', 9, 9, %[4]s)`, integrationOrg, foreignOrg, grantedID, now),
		// The collider-left case: the hierarchy ref <repo>:jira:P:Q:jira:R was
		// minted for child jira:P:Q under parent jira:R, which is gone; the
		// only row left is child jira:P under parent Q:jira:R, whose
		// serialization is the same string.
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES ('%[1]s', 'jira:P', 'jira', 'Collider child', 'open', %[2]s, %[2]s, 'Q:jira:R', %[2]s, '%[3]s'), ('%[1]s', 'Q:jira:R', 'jira', 'Collider parent', 'open', %[2]s, %[2]s, '', %[2]s, '%[3]s')`, grantedID, now, integrationOrg),
		// codex r2's collision seed: two dependencies, one ref string.
		fmt.Sprintf(`INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES ('%[1]s', 'jira:A:B', 'jira', 'Grant source', 'open', %[3]s, %[3]s, '', %[3]s, '%[4]s'), ('%[2]s', 'jira:A', 'jira', 'Secret source', 'open', %[3]s, %[3]s, '', %[3]s, '%[4]s')`, grantedID, secretID, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES ('jira:A:B', 'jira:C', 'blocks', 'Grant dependency', %[1]s, '%[2]s'), ('jira:A', 'B:jira:C', 'blocks', 'Secret dependency', %[1]s, '%[2]s')`, now, integrationOrg),
		// INC-2's service maps to 65 repositories through mappings that
		// expired, and to grantedID through a current one.
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) SELECT toUUID(concat('50000000-0000-4000-8000-', leftPad(toString(number), 12, '0'))), concat('acme/expired-', leftPad(toString(number), 3, '0')), %s, %s, '%s', 'github' FROM numbers(65)`, now, now, integrationOrg),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, valid_to, is_active) SELECT '%s', %s, concat('map-expired-', toString(number)), 'native', 0.9, 'svc-2', toUUID(concat('50000000-0000-4000-8000-', leftPad(toString(number), 12, '0'))), %s, '2026-02-01 00:00:00.000000', 1 FROM numbers(65)`, integrationOrg, past, past),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%s', %s, 'map-current', 'native', 0.9, 'svc-2', '%s', %s, 1)`, integrationOrg, past, grantedID, past),
		fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%s', %s, 'INC-2', %s, %s, 'svc-2', 'Second outage', %s, 0)`, integrationOrg, past, past, past, past),
		fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%s', %s, 'INC-1', %s, %s, 'svc-1', 'Outage', %s, 0)`, integrationOrg, past, past, past, past),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%s', %s, 'map-1', 'native', 0.9, 'svc-1', '%s', %s, 1)`, integrationOrg, past, grantedID, past),
		fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES ('edge-1', '%s', 'dep-1', 'INC-1', '%s', 0.9, 'native', 'deployed before the outage', %s, %s)`, integrationOrg, grantedID, now, now),
	} {
		exec(t, ctx, direct, statement)
	}
}

func TestSourceRowsAgainstClickHouse(t *testing.T) {
	ctx := context.Background()
	query, direct := startClickHouse(t, ctx)
	seedIntegration(t, ctx, direct)
	recorder := &recordingClient{inner: query}
	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(recorder), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), admitAllSubjects{})
	if err != nil {
		t.Fatal(err)
	}
	granted := storage.Principal{OrgID: integrationOrg, RepositoryScopes: []string{grantedRep}}
	orgWide := storage.Principal{OrgID: integrationOrg}
	for _, tc := range []struct {
		kind  contractsv1.ContextFabricEvidenceEntityType
		id    string
		label string
	}{
		{contractsv1.ContextFabricEvidenceEntityRepository, grantedID, grantedRep},
		{contractsv1.ContextFabricEvidenceEntityWorkItem, grantedID + ":jira:ABC-1", "Fix login"},
		{contractsv1.ContextFabricEvidenceEntityPullRequest, grantedID + ":532", "Fix the widget"},
		{contractsv1.ContextFabricEvidenceEntityReview, grantedID + ":rev-9", "PR #532 review"},
		{contractsv1.ContextFabricEvidenceEntityCI, grantedID + ":run-1", "CI run-1"},
		{contractsv1.ContextFabricEvidenceEntityDeployment, grantedID + ":dep-1", "production deployment"},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-1", "Outage"},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-2", "Second outage"},
		// CHAOS-7227: organization-level rows (the gate here admits every
		// subject; the gate itself is tested with the route).
		{contractsv1.ContextFabricEvidenceEntityTeam, "team-a", "Team A"},
		{contractsv1.ContextFabricEvidenceEntityProject, "jira:PROJ-1", "Own project"},
	} {
		orgLevel := tc.kind == contractsv1.ContextFabricEvidenceEntityTeam || tc.kind == contractsv1.ContextFabricEvidenceEntityProject
		for _, principal := range []storage.Principal{granted, orgWide} {
			expanded, decision := resolve.ResolveSourceRow(ctx, principal, string(tc.kind), tc.id)
			if decision.Reason != contextfabric.SourceRowServed {
				t.Fatalf("%s %s (%v): %+v", tc.kind, tc.id, principal.RepositoryScopes, decision)
			}
			if err := expanded.Validate(); err != nil {
				t.Fatalf("%s: invalid expansion: %v", tc.kind, err)
			}
			if expanded.Evidence.EvidenceRefID != contractsv1.EvidenceRefID(tc.kind, tc.id) || (!orgLevel && expanded.Structured["repository"] != grantedRep) {
				t.Fatalf("%s: %+v %v", tc.kind, expanded.Evidence, expanded.Structured)
			}
			if tc.label != "" && expanded.Evidence.Source.DisplayLabel != tc.label {
				t.Fatalf("%s: label %q, want %q", tc.kind, expanded.Evidence.Source.DisplayLabel, tc.label)
			}
			// Exactly this organization's row: a foreign row sharing the
			// repository UUID (and, for work items, the id) would make it 2.
			if decision.Rows != 1 || decision.Repositories != 1 {
				t.Fatalf("%s %s: %+v", tc.kind, tc.id, decision)
			}
		}
	}

	// Organization isolation (r1 P1): the foreign organization's rows under
	// the colliding repository UUID are never served to this organization,
	// org-wide caller included.
	for _, foreign := range []struct {
		kind contractsv1.ContextFabricEvidenceEntityType
		id   string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItem, grantedID + ":jira:FOR-1"},
		{contractsv1.ContextFabricEvidenceEntityPullRequest, grantedID + ":990"},
		{contractsv1.ContextFabricEvidenceEntityReview, grantedID + ":FOREIGN-REVIEW-990"},
		{contractsv1.ContextFabricEvidenceEntityCI, grantedID + ":FOREIGN-CI-990"},
		{contractsv1.ContextFabricEvidenceEntityDeployment, grantedID + ":FOREIGN-DEP-990"},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-F"},
		{contractsv1.ContextFabricEvidenceEntityTeam, "team-b"},
		{contractsv1.ContextFabricEvidenceEntityProject, "jira:PROJ-F"},
	} {
		for _, principal := range []storage.Principal{granted, orgWide} {
			if _, decision := resolve.ResolveSourceRow(ctx, principal, string(foreign.kind), foreign.id); decision.Reason != contextfabric.SourceRowNoRow {
				t.Fatalf("%s %s: another organization's row: %+v", foreign.kind, foreign.id, decision)
			}
		}
		// The rows are real: the foreign organization reads its own.
		if _, decision := resolve.ResolveSourceRow(ctx, storage.Principal{OrgID: foreignOrg}, string(foreign.kind), foreign.id); decision.Reason != contextfabric.SourceRowServed {
			t.Fatalf("%s %s: the foreign organization's own row is not served: %+v", foreign.kind, foreign.id, decision)
		}
	}
	// The repository kind: the foreign repository row shares the UUID and
	// the slug, and this organization still reads exactly its own (the
	// served-kinds loop above asserts Rows == 1 and Repositories == 1 for
	// every kind, with those colliding rows present).

	// The commit statements are on no source-row plan (commit kinds stay on
	// the record) but context_for_task reads them through the same catalog:
	// each reads exactly this organization's row under the shared UUID.
	catalog := contextpacket.NewCatalogClickHouseRows(query)
	for _, read := range []struct {
		query, locator string
		want           int
		citation       string
	}{
		{"git_commits.v1", "acr:v1:commit:c0ffee", 1, "own commit"},
		{"git_commits.v1", "acr:v1:commit:f0e1gn", 0, ""},
		{"git_commit_files.v1", "acr:v1:commit-file:c0ffee:main.go", 1, "1 additions, 2 deletions"},
		{"git_commit_files.v1", "acr:v1:commit-file:f0e1gn:secret.go", 0, ""},
	} {
		references, err := catalog.ResolveSourceRow(ctx, integrationOrg, contractsv1.ResolvedScope{RepoID: grantedID, RepoSlug: grantedRep}, contextpacket.SourceRowRead{QueryID: read.query, Locator: read.locator})
		if err != nil {
			t.Fatalf("%s %s: %v", read.query, read.locator, err)
		}
		if len(references) != read.want || (read.want == 1 && references[0].Evidence.Citation != read.citation) {
			t.Fatalf("%s %s: %d rows %+v, want %d (%q)", read.query, read.locator, len(references), references, read.want, read.citation)
		}
		foreign, err := catalog.ResolveSourceRow(ctx, foreignOrg, contractsv1.ResolvedScope{RepoID: grantedID, RepoSlug: grantedRep}, contextpacket.SourceRowRead{QueryID: read.query, Locator: read.locator})
		if err != nil || len(foreign) != 1 {
			t.Fatalf("%s %s: the foreign organization reads %d of its own rows (%v)", read.query, read.locator, len(foreign), err)
		}
	}

	// Expired incident mappings (r1 P2) are not discovered: INC-2 has 65 of
	// them and one current mapping, and only the current one is a candidate.
	if _, decision := resolve.ResolveSourceRow(ctx, granted, "incident", "INC-2"); decision.Reason != contextfabric.SourceRowServed || decision.Repositories != 1 {
		t.Fatalf("INC-2 behind expired mappings: %+v", decision)
	}

	// CHAOS-7226 r2 P1 (codex's seed, a real-ClickHouse regression): the
	// dependency pair ref <source>:<target>:<relation key> is not injective.
	// jira:A:B -> jira:C (acme/api) and jira:A -> B:jira:C (other-org/secret)
	// both serialize to the same ref; before this change the grant filter
	// turned the two candidates into one and served acme/api's row for a ref
	// that may have come from the other. A kind whose grammar is not
	// injective, or whose id is not its table key, is never read as a
	// source row: the persisted record answers it. Rows of every excluded
	// kind exist here, and none is served: not even when the ref's own row is
	// gone and a collider is the ONLY row its string matches.
	collision := "jira:A:B:jira:C:" + dependencyrelation.Key("blocks")
	for _, excluded := range []struct {
		kind contractsv1.ContextFabricEvidenceEntityType
		id   string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, collision},
		// Its own row is gone and the collider is the only match.
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, grantedID + ":jira:P:Q:jira:R"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "jira:ABC-1:jira:ABC-0:" + dependencyrelation.Key("blocks")},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, grantedID + ":jira:ABC-1:jira:ABC-0:blocks"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, grantedID + ":jira:ABC-1:jira:ABC-0"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeam, grantedID + ":jira:ABC-1:team-a"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncident, "edge-1"},
	} {
		for _, principal := range []storage.Principal{granted, orgWide} {
			if _, decision := resolve.ResolveSourceRow(ctx, principal, string(excluded.kind), excluded.id); decision.Reason != contextfabric.SourceRowKindOnRecord {
				t.Fatalf("%s %s: a non-injective or non-key id was read as a source row: %+v", excluded.kind, excluded.id, decision)
			}
		}
	}

	// Equal refusal on the real engine: the out-of-grant work item, then the
	// same ref once its rows are gone.
	ref := secretID + ":jira:SEC-1"
	if _, decision := resolve.ResolveSourceRow(ctx, orgWide, "work-item", ref); decision.Reason != contextfabric.SourceRowServed {
		t.Fatalf("secret row not seeded: %+v", decision)
	}
	recorder.log = nil
	_, refused := resolve.ResolveSourceRow(ctx, granted, "work-item", ref)
	refusedLog := recorder.log
	exec(t, ctx, direct, fmt.Sprintf(`ALTER TABLE repos DELETE WHERE id = '%s' SETTINGS mutations_sync = 2`, secretID))
	exec(t, ctx, direct, `ALTER TABLE work_items DELETE WHERE work_item_id = 'jira:SEC-1' SETTINGS mutations_sync = 2`)
	recorder.log = nil
	_, absent := resolve.ResolveSourceRow(ctx, granted, "work-item", ref)
	if refused.Reason != contextfabric.SourceRowNoRow || absent.Reason != contextfabric.SourceRowNoRow || refused.Repositories != 1 || absent.Repositories != 0 {
		t.Fatalf("refused %+v, absent %+v", refused, absent)
	}
	if !reflect.DeepEqual(refusedLog, recorder.log) {
		t.Fatalf("statements differ:\n refused %v\n absent  %v", refusedLog, recorder.log)
	}
}

// admitAllSubjects is a subject gate that admits every subject: the
// ClickHouse test proves the organization-level row statements, not the gate.
type admitAllSubjects struct{}

func (admitAllSubjects) Authorize(_ context.Context, _ storage.Principal, requested []contextfabric.SubjectRef) (directread.AuthorizedSubjects, directread.Authorization) {
	authorization := directread.Authorization{Decision: directread.DecisionAdmitted}
	for _, subject := range requested {
		authorization.Outcomes = append(authorization.Outcomes, directread.GatedSubject{Subject: subject, Outcome: directread.SubjectAdmitted})
	}
	return directread.AuthorizedSubjects{}, authorization
}
