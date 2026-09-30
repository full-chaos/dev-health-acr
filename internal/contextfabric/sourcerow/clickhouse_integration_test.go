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
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The source-row statements against a real ClickHouse holding only the
// tables devhealthschema declares, seeded with one row for each of the 11
// source kinds the way the producers read them (both dependency grammars
// included). It proves the SQL (the repository lookup, the three
// discoveries, the dependency locator, both source-row-only statements and
// the nine catalog statements the plans name, each filtered to one evidence
// id) and, on the real engine:
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
		fmt.Sprintf(`INSERT INTO teams (id, name, updated_at, org_id, provider, is_active) VALUES ('team-b', 'Team B', %s, '%s', 'jira', 1)`, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES ('%s', '00000000-0000-0000-0000-000000000000', 'jira:FOR-1', 'team-b', 'native_team', 1, 'high', %s)`, foreignOrg, now),
		fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%s', %s, 'INC-F', %s, %s, 'svc-f', 'FOREIGN outage', %s, 0)`, foreignOrg, past, past, past, past),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%s', %s, 'map-f', 'native', 0.9, 'svc-f', '%s', %s, 1)`, foreignOrg, past, grantedID, past),
		fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES ('edge-f', '%s', 'FOREIGN-DEP-990', 'INC-F', '%s', 0.9, 'native', 'foreign', %s, %s)`, foreignOrg, grantedID, now, now),
		// Its PR 532 collides with this organization's PR 532 on (repo, number).
		fmt.Sprintf(`INSERT INTO git_pull_requests (repo_id, number, title, state, created_at, last_synced, org_id) VALUES ('%[1]s', 990, 'FOREIGN PR 990', 'open', %[2]s, %[2]s, '%[3]s'), ('%[1]s', 532, 'FOREIGN PR 532', 'open', %[2]s, %[2]s, '%[3]s')`, grantedID, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO git_pull_request_reviews (repo_id, number, review_id, state, submitted_at, last_synced, org_id) VALUES ('%s', 990, 'FOREIGN-REVIEW-990', 'APPROVED', %s, %s, '%s')`, grantedID, now, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO ci_pipeline_runs (repo_id, run_id, status, started_at, last_synced, org_id) VALUES ('%s', 'FOREIGN-CI-990', 'failed', %s, %s, '%s')`, grantedID, now, now, foreignOrg),
		fmt.Sprintf(`INSERT INTO deployments (repo_id, deployment_id, status, environment, started_at, deployed_at, last_synced, org_id) VALUES ('%s', 'FOREIGN-DEP-990', 'success', 'private-env', %s, %s, %s, '%s')`, grantedID, now, now, now, foreignOrg),
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
	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(recorder), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}))
	if err != nil {
		t.Fatal(err)
	}
	pair := "jira:ABC-1:jira:ABC-0:" + dependencyrelation.Key("blocks")
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
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, grantedID + ":jira:ABC-1:jira:ABC-0", "jira:ABC-1 part of jira:ABC-0"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeam, grantedID + ":jira:ABC-1:team-a", "jira:ABC-1 owned by team team-a"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, grantedID + ":jira:ABC-1:jira:ABC-0:blocks", ""},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, pair, ""},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-1", "Outage"},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-2", "Second outage"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncident, "edge-1", "dep-1 linked to INC-1"},
	} {
		for _, principal := range []storage.Principal{granted, orgWide} {
			expanded, decision := resolve.ResolveSourceRow(ctx, principal, string(tc.kind), tc.id)
			if decision.Reason != contextfabric.SourceRowServed {
				t.Fatalf("%s %s (%v): %+v", tc.kind, tc.id, principal.RepositoryScopes, decision)
			}
			if err := expanded.Validate(); err != nil {
				t.Fatalf("%s: invalid expansion: %v", tc.kind, err)
			}
			if expanded.Evidence.EvidenceRefID != contractsv1.EvidenceRefID(tc.kind, tc.id) || expanded.Structured["repository"] != grantedRep {
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
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, grantedID + ":jira:FOR-1:jira:FOR-0"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeam, grantedID + ":jira:FOR-1:team-b"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, grantedID + ":jira:FOR-1:jira:FOR-0:blocks"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "jira:FOR-1:jira:FOR-0:" + dependencyrelation.Key("blocks")},
		{contractsv1.ContextFabricEvidenceEntityIncident, "INC-F"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncident, "edge-f"},
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

	// Expired incident mappings (r1 P2) are not discovered: INC-2 has 65 of
	// them and one current mapping, and only the current one is a candidate.
	if _, decision := resolve.ResolveSourceRow(ctx, granted, "incident", "INC-2"); decision.Reason != contextfabric.SourceRowServed || decision.Repositories != 1 {
		t.Fatalf("INC-2 behind expired mappings: %+v", decision)
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
