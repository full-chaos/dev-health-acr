package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// The production column snapshot this file used to hold inline now lives
// in devhealthschema, shared with devhealthfacts' own parity guard and
// with this package's org-isolation fixtures (CHAOS-3781 round-2 F1/F4).
//
// Two hand-maintained copies is what let the UInt32 drift survive: each
// package's fixtures agreed with its own reader's mistake. Rendering every
// fixture and every guard from one declaration means a fixture can no
// longer disagree with the schema the guard asserts.

// productionSchemaDDL renders the shared declaration into CREATE TABLE
// statements for the tables THIS package's producers read.
func productionSchemaDDL() []string {
	return devhealthschema.DDL(sourceSchemaTables...)
}

// devhealthschema:not-a-production-replica this list names WHICH declared tables to render;
// every column type, engine and sort key still comes from
// devhealthschema.DDL. Naming a subset is the point of the guard, not a rival
// source of schema truth.
// sourceSchemaTables are the tables devhealthsource's producers read. Named
// explicitly rather than taking every declared table, so this guard keeps
// asserting exactly its own package's surface as devhealthschema grows to
// cover other readers.
var sourceSchemaTables = []string{
	"repos", "work_items", "git_pull_requests", "git_pull_request_reviews",
	"ci_pipeline_runs", "deployments", "operational_incidents",
	"operational_service_repository_mappings", "work_item_dependencies",
	"work_graph_deployment_incident_edges",
	// CHAOS-3802's producers read these four; assertTeamsProjectsSchemaParity
	// runs against the same fixture rather than a second testcontainer.
	// CHAOS-4193: project_membership_transitions is the TABLE the
	// project_membership_presence VIEW reads (createProjectMembershipPresenceView
	// creates the view itself -- DDL() only ever renders CREATE TABLE).
	"teams", "projects", "work_item_team_attributions", "team_project_ownership",
	"project_membership_transitions",
	// CHAOS-4390: queryTeams now LEFT JOINs team_repo_ownership
	// (ownedRepositoriesJoinSQL) to derive a team's real, current
	// repository-ownership authorization scope.
	"team_repo_ownership",
}

// createProjectMembershipPresenceView creates project_membership_presence
// (devhealthschema.ProjectMembershipPresenceViewDDL, verbatim ops migration
// 077) against direct. productionSchemaDDL()/DDL() cannot render it --
// DDL() only ever renders a plain CREATE TABLE from a column list, and this
// object is a CREATE OR REPLACE VIEW with CTEs/arrayJoin/UNION ALL -- so
// every integration fixture that queries through the view creates it via
// this helper, AFTER project_membership_transitions and work_items exist
// (the view's FROM clauses reference both).
func createProjectMembershipPresenceView(t *testing.T, ctx context.Context, direct interface {
	Exec(ctx context.Context, query string, args ...any) error
}) {
	t.Helper()
	if err := direct.Exec(ctx, devhealthschema.ProjectMembershipPresenceViewDDL); err != nil {
		t.Fatalf("create project_membership_presence view: %v", err)
	}
}

// TestLiveSchemaParityAcrossEveryProducer seeds one real ClickHouse
// (production-typed, per productionSchemaDDL) row per table entityTables
// reads, runs the actual NextProjectionBatch path (not a fake), and asserts
// every table contributed at least one candidate. A Scan() type mismatch
// against any producer's column -- the CHAOS-3789 class of bug -- surfaces
// here as a query error instead of on a live organization.
func TestLiveSchemaParityAcrossEveryProducer(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	const orgID = "10000000-0000-4000-8000-000000000001"
	const repoID = "20000000-0000-4000-8000-000000000002"
	const repoSlug = "acme/parity-service"
	// CHAOS-4193: shaped as a real GitHub Projects V2 id
	// (ghprojv2:<org>#<n>, Context Fabric ruling 2026-08-24 09:50) because
	// project_membership_presence's work_item_column arm refuses any OTHER
	// shape for provider=github (`startsWith(w.project_id, 'ghprojv2:')`) --
	// unlike every other provider, which the arm accepts unprefixed.
	const projectID = "ghprojv2:acme#1"
	const teamID = "TEAM-PARITY"

	query, direct := newDevHealthClickHouseIntegrationClient(t, ctx)
	for _, statement := range productionSchemaDDL() {
		if err := direct.Exec(ctx, statement); err != nil {
			t.Fatalf("create table: %v\n%s", err, statement)
		}
	}
	createProjectMembershipPresenceView(t, ctx, direct)

	mustSeed := func(label, statement string, args ...any) {
		t.Helper()
		if err := direct.Exec(ctx, statement, args...); err != nil {
			t.Fatalf("seed %s: %v", label, err)
		}
	}

	// devhealthschema:not-a-production-replica these are INSERT statements seeding rows into tables
	// devhealthschema.DDL already created; the table name is an argument
	// selecting where the row goes. No schema is declared here.
	mustSeed("repos", `INSERT INTO repos (id, org_id, repo, provider, last_synced) VALUES (?, ?, ?, ?, ?)`,
		repoID, orgID, repoSlug, "github", at)
	// The parent carries an EMPTY project_id and the child a real one, so
	// project_membership_presence's work_item_column arm `project_id != ''`
	// filter is exercised in both directions by the same fixture
	// (CHAOS-3802). provider=github on the child is what the view's
	// github-specific ghprojv2: prefix carve-out (CHAOS-4193) actually
	// tests: an empty/other-provider row would skip that branch entirely.
	mustSeed("work_items parent", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-PARENT", repoID, orgID, "Parent work item", "open", "", "", "github", "", at)
	mustSeed("work_items child", `INSERT INTO work_items (work_item_id, repo_id, org_id, title, status, url, parent_id, provider, project_id, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"WI-CHILD", repoID, orgID, "Child work item", "open", "", "WI-PARENT", "github", projectID, at)
	mustSeed("git_pull_requests", `INSERT INTO git_pull_requests (repo_id, org_id, number, title, state, last_synced) VALUES (?, ?, ?, ?, ?, ?)`,
		repoID, orgID, uint32(4242), "Parity PR", "open", at)
	mustSeed("deployments", `INSERT INTO deployments (repo_id, org_id, deployment_id, status, environment, deployed_at, started_at, last_synced) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID, orgID, "deploy-parity-1", "success", "production", at, at, at)
	mustSeed("operational_service_repository_mappings", `INSERT INTO operational_service_repository_mappings (org_id, service_id, repo_id, is_active) VALUES (?, ?, ?, ?)`,
		orgID, "svc-parity", repoID, uint8(1))
	mustSeed("operational_incidents", `INSERT INTO operational_incidents (id, org_id, service_id, title, normalized_status, raw_status, normalized_severity, raw_severity, started_at, source_event_at, observed_at, is_deleted) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"incident-parity-1", orgID, "svc-parity", "Parity incident", "open", "open", "low", "low", at, at, at, uint8(0))
	mustSeed("work_item_dependencies", `INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, org_id, last_synced) VALUES (?, ?, ?, ?, ?)`,
		"WI-CHILD", "WI-PARENT", "blocks", orgID, at)
	mustSeed("work_graph_deployment_incident_edges", `INSERT INTO work_graph_deployment_incident_edges (edge_id, deployment_id, incident_id, repo_id, org_id, observed_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"edge-parity-1", "deploy-parity-1", "incident-parity-1", repoID, orgID, at)
	mustSeed("git_pull_request_reviews", `INSERT INTO git_pull_request_reviews (review_id, repo_id, org_id, number, state, submitted_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"review-parity-1", repoID, orgID, uint32(4242), "approved", at)
	mustSeed("ci_pipeline_runs", `INSERT INTO ci_pipeline_runs (run_id, repo_id, org_id, branch, status, started_at, finished_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"run-parity-1", repoID, orgID, "main", "success", at, at)
	// devhealthschema:not-a-production-replica the seeding block continues here, past the reach of
	// the marker above it. Same INSERT-into-an-already-created-table shape:
	// devhealthschema.DDL created every table named below, and here the name
	// only selects a destination for a row. No schema is declared.
	mustSeed("teams", `INSERT INTO teams (id, name, description, updated_at, org_id, provider, native_team_key, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		teamID, "Parity Team", "Parity team description", at, orgID, "github", "parity-team", uint8(1))
	mustSeed("projects", `INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, orgID, "github", "PARITY", "Parity Project", uint8(1), "started", "https://example.invalid/parity", at)
	mustSeed("work_item_team_attributions", `INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, repoID, "WI-CHILD", teamID, "native_team", uint8(1), "high", at)
	// TWO ownership rows for ONE edge, differing only in valid_from -- the
	// live shape queryProjectTeams exists to survive (616 rows, 3 edges).
	// FINAL cannot collapse them because valid_from is in the ORDER BY, so
	// this fixture fails the batch's relationship-uniqueness rule unless the
	// producer's GROUP BY is doing its job.
	mustSeed("team_project_ownership first window", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, "github", teamID, projectID, "PARITY", "native", at.Add(-48*time.Hour), nil, at)
	mustSeed("team_project_ownership second window", `INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, valid_to, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		orgID, "github", teamID, projectID, "PARITY", "native", at.Add(-24*time.Hour), nil, at)

	source, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch against production-typed schema: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	// One canonical ID per table this fixture seeded, keyed by the table
	// name entityTables (tables.go) uses -- proof that table's producer
	// actually scanned a row, not just that the batch as a whole is
	// non-empty.
	// CHAOS-3898 §1.5: work_item_dependencies' relationship id is the
	// digest-scheme relationship.v2 id (identity.DeriveRelationship), not
	// a hand-built string -- computed here from the same endpoint
	// canonical ids the production producer itself derives, so this
	// fixture can never drift from the actual scheme.
	workItemChildID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, "WI-CHILD"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(child) failed: omitted=%v err=%v", omitted, err)
	}
	workItemParentID, omitted, err := identity.Derive(identity.KindWorkItem, []string{repoID, "WI-PARENT"}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(parent) failed: omitted=%v err=%v", omitted, err)
	}
	workItemDependencyRelationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, workItemChildID, workItemParentID, "blocks")
	// CHAOS-3898 §1.5 P1-2 fix-forward: work_items_hierarchy's relationship
	// id is the SAME digest scheme, converted from the old
	// endpoint-independent string this fixture used to hardcode --
	// queryWorkItemHierarchy's own PART_OF type, same endpoint canonical
	// ids computed above.
	workItemHierarchyRelationshipID := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemHierarchy, workItemChildID, workItemParentID, string(contractsv1.ContextFabricRelationshipPartOf))

	wantCanonicalID := map[string]string{
		// devhealthschema:not-a-production-replica this maps each table to the canonical ID its row is
		// expected to project to. Keyed BY table on purpose, and it mirrors no
		// column type, engine or sort key -- it is an assertion about output.
		"repos":                                "repository:" + repoID,
		"work_items":                           "work_item.v2:" + repoID + ":WI-CHILD",
		"git_pull_requests":                    "pull_request:" + repoID + ":4242",
		"deployments":                          "deployment.v2:" + repoID + ":deploy-parity-1",
		"operational_incidents":                "incident:incident-parity-1",
		"work_item_dependencies":               workItemDependencyRelationshipID,
		"work_items_hierarchy":                 workItemHierarchyRelationshipID,
		"work_graph_deployment_incident_edges": "relationship:deployment_incident:edge-parity-1",
		"git_pull_request_reviews":             "pull_request_review.v2:" + repoID + ":4242:review-parity-1",
		"ci_pipeline_runs":                     "ci_pipeline_run.v2:" + repoID + ":run-parity-1",
	}

	seen := map[string]bool{}
	for _, entity := range batch.Entities {
		for table, canonicalID := range wantCanonicalID {
			if entity.Subject.CanonicalID == canonicalID {
				seen[table] = true
			}
		}
	}
	for _, relationship := range batch.Relationships {
		for table, canonicalID := range wantCanonicalID {
			if relationship.RelationshipID == canonicalID {
				seen[table] = true
			}
		}
	}

	// CHAOS-3789 codex round-1 F2: derive the table inventory from
	// entityTables (tables.go) itself, via export_test.go, instead of
	// trusting wantCanonicalID's key set to stay in sync by hand -- a
	// producer added to entityTables with no matching entry here now fails
	// this test instead of silently going unasserted, and a stale
	// wantCanonicalID entry for a since-removed producer fails too.
	tableNames := devhealthsource.EntityTableNamesForTest()
	matched := map[string]bool{}
	for _, table := range tableNames {
		canonicalID, ok := wantCanonicalID[table]
		if !ok {
			t.Errorf("entityTables (tables.go) lists table %q with no matching entry in this test's wantCanonicalID -- add a seed row above and an expected canonical/relationship ID here", table)
			continue
		}
		matched[table] = true
		if !seen[table] {
			t.Errorf("producer for table %q never contributed its expected candidate (%q) -- its Scan() may not have matched the production-typed schema, or the fixture row above didn't join; entities=%+v relationships=%+v", table, canonicalID, batch.Entities, batch.Relationships)
		}
	}
	for table := range wantCanonicalID {
		if !matched[table] {
			t.Errorf("wantCanonicalID has entry %q but entityTables (tables.go) no longer lists it -- remove the stale entry", table)
		}
	}

	assertTeamsProjectsSchemaParity(t, ctx, query, orgID, repoID, projectID, teamID)
}

// assertTeamsProjectsSchemaParity is CHAOS-3802's half of the same guarantee,
// run against the same production-typed fixture rather than a second
// testcontainer. It matters more here than for any producer above, because
// this source scans shapes the package's fake cannot vouch for at all: Enum8
// through toString(), Nullable(String) through ifNull, a
// Nullable(DateTime64) collapsed by max(valid_to IS NULL) into a UInt8, and
// teams.updated_at's DateTime64(6) with no timezone qualifier compared
// against a DateTime64(6,'UTC') bind parameter. A Scan mismatch in any of
// those is the CHAOS-3789 class of bug, invisible to every fake-backed test.
func assertTeamsProjectsSchemaParity(t *testing.T, ctx context.Context, query contextpacket.ClickHouseQueryClient, orgID, repoID, projectID, teamID string) {
	t.Helper()
	source, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatalf("new teams/projects source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: orgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("teams/projects batch against production-typed schema: %v", err)
	}
	if !available {
		t.Fatal("expected a teams/projects batch to be available")
	}

	// Built via identity.Derive itself, not a hand-typed literal: projectID
	// ("ghprojv2:acme#1", CHAOS-4193) contains a raw ':', which
	// EncodeSegment escapes to "%3A" -- a literal "project.v2:github:" +
	// projectID string would silently mismatch the real encoded id.
	projectCanonicalID, omitted, err := identity.Derive(identity.KindProject, []string{"github", projectID}, nil)
	if err != nil || omitted {
		t.Fatalf("identity.Derive(project): omitted=%v err=%v", omitted, err)
	}
	wantCanonicalID := map[string]string{
		// devhealthschema:not-a-production-replica this maps each table to the canonical ID its row is
		// expected to project to. Keyed BY table on purpose, and it mirrors no
		// column type, engine or sort key -- it is an assertion about output.
		"teams":                       "team:" + teamID,
		"projects":                    projectCanonicalID,
		"project_membership_presence": devhealthsource.ProjectMembershipRelationshipIDForTest(t, devhealthsource.WorkItemSubjectCanonicalIDForTest(t, repoID, "WI-CHILD"), "github", projectID, ""),
		"work_item_team_attributions": devhealthsource.WorkItemTeamRelationshipIDForTest(t, repoID, "WI-CHILD", teamID),
		"team_project_ownership":      devhealthsource.ProjectTeamRelationshipIDForTest(t, "github", projectID, teamID, "native"),
	}
	seen := map[string]bool{}
	for _, entity := range batch.Entities {
		for table, canonicalID := range wantCanonicalID {
			if entity.Subject.CanonicalID == canonicalID {
				seen[table] = true
			}
		}
	}
	for _, relationship := range batch.Relationships {
		for table, canonicalID := range wantCanonicalID {
			if relationship.RelationshipID == canonicalID {
				seen[table] = true
			}
		}
	}

	// Same F2 discipline as above: derive the inventory from
	// teamsProjectsTables itself so a producer added without a seed row and
	// expectation here fails loudly instead of going unasserted.
	matched := map[string]bool{}
	for _, table := range devhealthsource.TeamsProjectsTableNamesForTest() {
		canonicalID, ok := wantCanonicalID[table]
		if !ok {
			t.Errorf("teamsProjectsTables lists table %q with no matching entry here -- add a seed row and an expected canonical/relationship ID", table)
			continue
		}
		matched[table] = true
		if !seen[table] {
			t.Errorf("teams/projects producer for table %q never contributed its expected candidate (%q); entities=%+v relationships=%+v", table, canonicalID, batch.Entities, batch.Relationships)
		}
	}
	for table := range wantCanonicalID {
		if !matched[table] {
			t.Errorf("this test expects table %q but teamsProjectsTables no longer lists it -- remove the stale entry", table)
		}
	}
}
