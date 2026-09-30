package sourcerow_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthfacts"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthschema"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/evidenceref"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/sourcerow"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-7252 on a real ClickHouse, from the REAL producers.
//
// The projection sources acr-projector runs (devhealthsource) and the fact
// providers (devhealthfacts) read seeded rows that carry every id shape the
// retired bare-':' grammars could not tell apart -- codex r2's dependency
// pair, the same shift for hierarchy and work-item-team (an Atlassian team
// ARI), a deployment/incident pair under two sources, ids holding '%' and a
// literal "%3A" -- plus the collider-left shapes, where a ref's own row is
// gone and only its old-grammar collider is left. Every ".v2" ref those
// producers mint must:
//
//   - parse back to exactly the components of the row it was minted from
//     (the differential: mint -> parse -> same components, over producer
//     output, never hand-written ids);
//   - expand through the resolver to that row and no other, the SQL-rendered
//     id of the served row equal to the Go-minted one (Go <-> SQL parity on
//     the engine);
//   - be refused to a caller who may not read every subject the row names,
//     and a collider-left ref must name nothing.

const (
	now7252  = "'2026-09-01 12:00:00.000'"
	past7252 = "'2026-01-01 00:00:00.000000'"
)

// seed7252 seeds its own ClickHouse (a fresh container: the #731 test's
// seed shares ids with this one on purpose -- same UUIDs and slugs -- and
// must not bleed into these expectations).
func seed7252(t *testing.T, ctx context.Context, direct clickhousedriver.Conn) {
	t.Helper()
	for _, statement := range devhealthschema.DDL() {
		exec(t, ctx, direct, statement)
	}
	exec(t, ctx, direct, devhealthschema.ProjectMembershipPresenceViewDDL)
	org, foreign := integrationOrg, foreignOrg
	workItems := func(org, repo string, ids ...[2]string) string {
		values := make([]string, 0, len(ids))
		for _, id := range ids {
			values = append(values, fmt.Sprintf("('%s', '%s', 'jira', 'item %s', 'open', %s, %s, '%s', %s, '%s')", repo, id[0], id[0], now7252, now7252, id[1], now7252, org))
		}
		return `INSERT INTO work_items (repo_id, work_item_id, provider, title, status, created_at, updated_at, parent_id, last_synced, org_id) VALUES ` + strings.Join(values, ", ")
	}
	for _, statement := range []string{
		fmt.Sprintf(`INSERT INTO repos (id, repo, created_at, last_synced, org_id, provider) VALUES ('%s', '%s', %s, %s, '%s', 'github'), ('%s', '%s', %s, %s, '%s', 'github'), ('%s', '%s', %s, %s, '%s', 'github')`,
			grantedID, grantedRep, now7252, now7252, org, secretID, secretRep, now7252, now7252, org, grantedID, grantedRep, now7252, now7252, foreign),
		workItems(org, grantedID,
			[2]string{"jira:A:B", ""}, [2]string{"jira:C", ""},
			[2]string{"jira:T1", ""}, [2]string{"jira:T2", ""},
			[2]string{"jira:L:M", ""}, [2]string{"jira:N", ""},
			// The collider-left subjects: work items that exist, whose
			// relations do not.
			[2]string{"jira:L", ""}, [2]string{"M:jira:N", ""},
			[2]string{"jira:W", ""}, [2]string{"jira:ABC-1:ari:cloud", ""},
			[2]string{"pct%3A:1", ""}, [2]string{"pct%25", ""},
			[2]string{"jira:P:Q", "jira:R"}, [2]string{"jira:R", ""},
			[2]string{"jira:P", "Q:jira:R"}, [2]string{"Q:jira:R", ""},
			[2]string{"jira:U", "V:jira:W"}, [2]string{"V:jira:W", ""},
			[2]string{"jira:K", "jira:PARENT2"}, [2]string{"jira:PARENT2", ""},
			[2]string{"jira:S1", "jira:SP"},
			[2]string{"a%b:c", "jira:R"},
			[2]string{"jira:ABC-1", ""}, [2]string{"jira:ABC-1:ari", ""},
		),
		workItems(org, secretID,
			[2]string{"jira:A", ""}, [2]string{"B:jira:C", ""},
			[2]string{"jira:PARENT2", ""}, [2]string{"jira:SP", ""},
		),
		// The foreign organization: the same repository UUID, the same ids.
		workItems(foreign, grantedID, [2]string{"jira:A:B", ""}, [2]string{"jira:C", ""}, [2]string{"jira:P:Q", "jira:R"}, [2]string{"jira:R", ""}),
		fmt.Sprintf(`INSERT INTO work_item_dependencies (source_work_item_id, target_work_item_id, relationship_type, relationship_type_raw, last_synced, org_id) VALUES
			('jira:A:B', 'jira:C', 'blocks', 'Grant dependency', %[1]s, '%[2]s'),
			('jira:A', 'B:jira:C', 'blocks', 'Secret dependency', %[1]s, '%[2]s'),
			('jira:T1', 'jira:T2', 'blocks', 'Older twin', '2026-08-01 00:00:00.000', '%[2]s'),
			('jira:T1', 'jira:T2', 'BLOCKS ', 'Newer twin', %[1]s, '%[2]s'),
			('jira:A:B', 'EXT-1', 'relates_to', 'Stub target', %[1]s, '%[2]s'),
			('jira:L:M', 'jira:N', 'blocks', 'Collider', %[1]s, '%[2]s'),
			('pct%%3A:1', 'pct%%25', 'relates_to', 'Percent ids', %[1]s, '%[2]s'),
			('jira:A:B', 'jira:C', 'blocks', 'FOREIGN dependency', %[1]s, '%[3]s')`, now7252, org, foreign),
		fmt.Sprintf(`INSERT INTO teams (id, name, updated_at, org_id, provider, is_active) VALUES ('ari:cloud:identity::team/1', 'Team ARI', %[1]s, '%[2]s', 'jira', 1), ('cloud:identity::team/1', 'Team shifted', %[1]s, '%[2]s', 'jira', 1), ('gl:full.chaos', 'Full Chaos', %[1]s, '%[2]s', 'gitlab', 1)`, now7252, org),
		fmt.Sprintf(`INSERT INTO work_item_team_attributions (org_id, repo_id, work_item_id, team_id, source, is_primary, confidence, computed_at) VALUES
			('%[1]s', '00000000-0000-0000-0000-000000000000', 'jira:ABC-1', 'ari:cloud:identity::team/1', 'native_team', 1, 'high', %[2]s),
			('%[1]s', '00000000-0000-0000-0000-000000000000', 'jira:ABC-1:ari', 'cloud:identity::team/1', 'issue_project', 1, 'medium', %[2]s)`, org, now7252),
		// Incidents: INC-1 maps to acme/api now, INC-S only to
		// other-org/secret, INC-U to nothing.
		fmt.Sprintf(`INSERT INTO operational_incidents (org_id, source_version_at, id, observed_at, last_synced, service_id, title, started_at, is_deleted) VALUES ('%[1]s', %[2]s, 'INC-1', %[2]s, %[2]s, 'svc-1', 'Outage', %[2]s, 0), ('%[1]s', %[2]s, 'INC-S', %[2]s, %[2]s, 'svc-s', 'Secret outage', %[2]s, 0), ('%[1]s', %[2]s, 'INC-U', %[2]s, %[2]s, 'svc-u', 'Unmapped outage', %[2]s, 0)`, org, past7252),
		fmt.Sprintf(`INSERT INTO operational_service_repository_mappings (org_id, source_version_at, id, relationship_provenance, relationship_confidence, service_id, repo_id, valid_from, is_active) VALUES ('%[1]s', %[2]s, 'map-1', 'native', 0.9, 'svc-1', '%[3]s', %[2]s, 1), ('%[1]s', %[2]s, 'map-s', 'native', 0.9, 'svc-s', '%[4]s', %[2]s, 1)`, org, past7252, grantedID, secretID),
		// One (deployment, incident) under TWO sources: one edge_id, two rows.
		fmt.Sprintf(`INSERT INTO work_graph_deployment_incident_edges (edge_id, org_id, deployment_id, incident_id, repo_id, confidence, source, evidence, observed_at, computed_at) VALUES
			('edge-1', '%[1]s', 'dep-1', 'INC-1', '%[2]s', 0.9, 'native', 'asserted link', %[3]s, %[3]s),
			('edge-1', '%[1]s', 'dep-1', 'INC-1', '%[2]s', 0.4, 'heuristic', 'inferred link', %[3]s, %[3]s),
			('edge-u', '%[1]s', 'dep:2', 'INC-U', '%[2]s', 0.9, 'native', 'unmapped incident', %[3]s, %[3]s),
			('edge-s', '%[1]s', 'dep-3', 'INC-S', '%[2]s', 0.9, 'native', 'secret incident', %[3]s, %[3]s)`, org, grantedID, now7252),
		// One project -> team ownership (project-team.v2 is minted; its
		// source-row read is a separate change).
		fmt.Sprintf(`INSERT INTO projects (id, org_id, provider, project_key, name, is_active, state, url, updated_at) VALUES ('%[1]s:gitlab:71133891', '%[1]s', 'gitlab', 'full.chaos/chaos-ops', 'chaos-ops', 1, 'active', 'https://gitlab.com/full.chaos/chaos-ops', %[2]s)`, org, now7252),
		fmt.Sprintf(`INSERT INTO team_project_ownership (org_id, provider, team_id, project_id, project_key, source, valid_from, updated_at) VALUES ('%[1]s', 'gitlab', 'gl:full.chaos', 'full.chaos/chaos-ops', 'full.chaos/chaos-ops', 'native', %[2]s, %[2]s)`, org, now7252),
	} {
		exec(t, ctx, direct, statement)
	}
}

// drain runs one real projection source to the end and returns every
// ".v2" evidence ref its entities and relationships cite.
func drain(t *testing.T, ctx context.Context, source contextfabric.ProjectionSource, name string) map[string]bool {
	t.Helper()
	refs := map[string]bool{}
	cursor := ""
	for page := 0; page < 100; page++ {
		batch, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: integrationOrg, Source: name, Cursor: cursor})
		if err != nil {
			t.Fatalf("%s page %d: %v", name, page, err)
		}
		if !available {
			return refs
		}
		var cited []string
		for _, entity := range batch.Entities {
			cited = append(cited, entity.EvidenceRefIDs...)
		}
		for _, relationship := range batch.Relationships {
			cited = append(cited, relationship.EvidenceRefIDs...)
		}
		for _, ref := range cited {
			if kind, _ := splitRef(ref); contractsv1.EncodedEvidenceEntityType(kind) {
				refs[ref] = true
			}
		}
		if batch.NextCursor == cursor {
			t.Fatalf("%s page %d did not advance", name, page)
		}
		cursor = batch.NextCursor
	}
	t.Fatalf("%s did not drain", name)
	return nil
}

func splitRef(ref string) (contractsv1.ContextFabricEvidenceEntityType, string) {
	rest := strings.TrimPrefix(ref, contractsv1.ContextFabricEvidenceRefPrefix)
	kind, id, _ := strings.Cut(rest, ":")
	return contractsv1.ContextFabricEvidenceEntityType(kind), id
}

// rowCase is one seeded row: the components its ref must carry, who may
// read it, and a text of that row (and of no other) the expansion shows.
type rowCase struct {
	kind   contractsv1.ContextFabricEvidenceEntityType
	values []string
	// grantedServed: a caller granted only acme/api is served;
	// orgWideServed: an unrestricted caller is served.
	grantedServed, orgWideServed bool
	// shows is a substring of the served row's citation.
	shows string
	// oneOf names a group of rows the projector projects ONE edge for: the
	// deployment/incident rows sharing an edge_id (the projector's
	// relationship id), of which it keeps one and quarantines the rest as
	// duplicate_within_batch. Exactly one ref of the group is minted; each
	// row still has its own ref, and each ref expands to its own row.
	oneOf string
}

func rowCases() []rowCase {
	dependency := contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2
	hierarchy := contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2
	team := contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2
	deploymentIncident := contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2
	return []rowCase{
		// codex r2's pair: one retired ref, two rows, two repositories.
		{dependency, []string{"jira:A:B", "jira:C", "blocks:fwd"}, true, true, "Grant dependency", ""},
		{dependency, []string{"jira:A", "B:jira:C", "blocks:fwd"}, false, true, "Secret dependency", ""},
		// Spelling twins are one relation: the newer row is its
		// representative (latest last_synced).
		{dependency, []string{"jira:T1", "jira:T2", "blocks:fwd"}, true, true, "Newer twin", ""},
		// A target that is no work item: authorized by the source alone.
		{dependency, []string{"jira:A:B", "EXT-1", "relates_to:fwd"}, true, true, "Stub target", ""},
		{dependency, []string{"jira:L:M", "jira:N", "blocks:fwd"}, true, true, "Collider", ""},
		{dependency, []string{"pct%3A:1", "pct%25", "relates_to:fwd"}, true, true, "Percent ids", ""},
		// The hierarchy shift pair: both rows exist, each is its own ref.
		{hierarchy, []string{grantedID, "jira:P:Q", "jira:R"}, true, true, "child=jira:P:Q, parent=jira:R", ""},
		{hierarchy, []string{grantedID, "jira:P", "Q:jira:R"}, true, true, "child=jira:P, parent=Q:jira:R", ""},
		{hierarchy, []string{grantedID, "jira:U", "V:jira:W"}, true, true, "child=jira:U, parent=V:jira:W", ""},
		// A parent in two repositories: one row (no fan-out join).
		{hierarchy, []string{grantedID, "jira:K", "jira:PARENT2"}, true, true, "child=jira:K", ""},
		// A parent only in other-org/secret.
		{hierarchy, []string{grantedID, "jira:S1", "jira:SP"}, false, true, "child=jira:S1", ""},
		{hierarchy, []string{grantedID, "a%b:c", "jira:R"}, true, true, "child=a%b:c", ""},
		// The ARI shift pair (the retired <repo>:<wi>:<team> join collided).
		// Fail closed for a restricted caller until CHAOS-7227's team gate.
		{team, []string{zeroRepoID, "jira:ABC-1", "ari:cloud:identity::team/1", "native_team"}, false, true, "source=native_team", ""},
		{team, []string{zeroRepoID, "jira:ABC-1:ari", "cloud:identity::team/1", "issue_project"}, false, true, "source=issue_project", ""},
		// One (deployment, incident), two sources: two rows, two refs.
		{deploymentIncident, []string{grantedID, "dep-1", "INC-1", "native"}, true, true, "asserted link", "edge-1"},
		{deploymentIncident, []string{grantedID, "dep-1", "INC-1", "heuristic"}, true, true, "inferred link", "edge-1"},
		// An incident no service maps: refused to every caller.
		{deploymentIncident, []string{grantedID, "dep:2", "INC-U", "native"}, false, false, "", ""},
		// An incident mapped only to other-org/secret.
		{deploymentIncident, []string{grantedID, "dep-3", "INC-S", "native"}, false, true, "secret incident", ""},
	}
}

func TestEncodedRefsFromTheRealProducersExpandToTheirRow(t *testing.T) {
	ctx := context.Background()
	query, direct := startClickHouse(t, ctx)
	seed7252(t, ctx, direct)

	// The REAL producers.
	projection, err := devhealthsource.NewClickHouseProjectionSource(query)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := devhealthsource.NewTeamsProjectsSource(query, true)
	if err != nil {
		t.Fatal(err)
	}
	minted := drain(t, ctx, projection, devhealthsource.SourceName)
	for ref := range drain(t, ctx, teams, devhealthsource.TeamsProjectsSourceName) {
		minted[ref] = true
	}

	// Mint -> parse -> the same components, for every row: the set the
	// producers minted is exactly the set of seeded rows, one ref each.
	want := map[string]rowCase{}
	for _, row := range rowCases() {
		ref, fits := evidenceref.Mint(row.kind, row.values...)
		if !fits {
			t.Fatalf("%s %q: the ref does not fit", row.kind, row.values)
		}
		want[ref] = row
	}
	projectTeam, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityProjectTeamV2, "gitlab", integrationOrg+":gitlab:71133891", "gl:full.chaos", "native")
	for ref := range minted {
		kind, id := splitRef(ref)
		values, ok := evidenceref.Parse(kind, id)
		if !ok {
			t.Fatalf("a producer minted %q, which does not parse", ref)
		}
		if again, _ := evidenceref.Mint(kind, values...); again != ref {
			t.Fatalf("%q parses to %q, which mints %q", ref, values, again)
		}
		if row, seeded := want[ref]; ref != projectTeam && (!seeded || !reflect.DeepEqual(row.values, values)) {
			t.Errorf("a producer minted %q (%s %q) for no seeded row", ref, kind, values)
		}
	}
	groups := map[string]int{}
	for ref, row := range want {
		switch {
		case row.oneOf != "":
			if minted[ref] {
				groups[row.oneOf]++
			}
		case !minted[ref]:
			t.Errorf("no producer minted %s %q (%q)", row.kind, row.values, ref)
		}
	}
	if groups["edge-1"] != 1 {
		t.Errorf("the two rows under edge-1 minted %d refs, want exactly the one edge the projector keeps", groups["edge-1"])
	}
	if !minted[projectTeam] {
		t.Errorf("the project-team producer did not mint %q; minted %v", projectTeam, sortedKeys(minted))
	}

	// The fact providers mint the SAME dependency refs as the projection.
	facts := map[contextfabric.FactKind]contextfabric.FactProvider{}
	for _, provider := range devhealthfacts.NewProviders(query) {
		facts[provider.Capability().Kind] = provider
	}
	orgWide := storage.Principal{OrgID: integrationOrg}
	granted := storage.Principal{OrgID: integrationOrg, RepositoryScopes: []string{grantedRep}}
	subject := func(repo, id string) contextfabric.SubjectRef {
		canonical, _, err := identity.Derive(identity.KindWorkItem, []string{repo, id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectWorkItem, CanonicalID: canonical}
	}
	blockers, err := facts[contextfabric.FactBlockers].ReadFacts(ctx, orgWide, contextfabric.FactQuery{
		Kind: contextfabric.FactBlockers, Subjects: []contextfabric.SubjectRef{subject(grantedID, "jira:C"), subject(grantedID, "jira:T2")},
		Time: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
	})
	if err != nil {
		t.Fatal(err)
	}
	factRefs := map[string]bool{}
	for _, fact := range blockers.Facts {
		for _, ref := range fact.EvidenceRefIDs {
			factRefs[ref] = true
		}
	}
	for _, values := range [][]string{{"jira:A:B", "jira:C", "blocks:fwd"}, {"jira:T1", "jira:T2", "blocks:fwd"}} {
		ref, _ := evidenceref.Mint(contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, values...)
		if !factRefs[ref] || !minted[ref] {
			t.Errorf("%q: fact providers minted it %v, the projection %v (facts %v)", ref, factRefs[ref], minted[ref], sortedKeys(factRefs))
		}
	}
	if len(factRefs) != 2 {
		t.Errorf("blockers facts cite %v, want the two relations", sortedKeys(factRefs))
	}

	// Every minted ref expands to its row -- the SQL-rendered id of the
	// served row equals the Go-minted one -- and only to a caller who may
	// read every subject that row names.
	recorder := &recordingClient{inner: query}
	resolve, err := sourcerow.New(contextpacket.NewCatalogClickHouseRows(recorder), contextpacket.NewEvidenceResolver(contextpacket.EvidenceResolverOptions{}), nil)
	if err != nil {
		t.Fatal(err)
	}
	structuredKeys := map[contractsv1.ContextFabricEvidenceEntityType]string{
		contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2: "dependency_id",
		contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2:  "hierarchy_id",
		contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2:       "work_item_team_id",
		contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2: "deployment_incident_id",
	}
	for ref, row := range want {
		kind, id := splitRef(ref)
		for _, caller := range []struct {
			principal storage.Principal
			served    bool
		}{{granted, row.grantedServed}, {orgWide, row.orgWideServed}} {
			expanded, decision := resolve.ResolveSourceRow(ctx, caller.principal, string(kind), id)
			if got := decision.Reason == contextfabric.SourceRowServed; got != caller.served {
				t.Errorf("%s %q (%v): served %v, want %v (%+v)", kind, row.values, caller.principal.RepositoryScopes, got, caller.served, decision)
				continue
			}
			if !caller.served {
				if decision.Reason != contextfabric.SourceRowNoRow {
					t.Errorf("%s %q (%v): refusal %+v", kind, row.values, caller.principal.RepositoryScopes, decision)
				}
				continue
			}
			if err := expanded.Validate(); err != nil {
				t.Fatalf("%s: invalid expansion: %v", ref, err)
			}
			if expanded.Evidence.EvidenceRefID != ref || decision.Rows != 1 || expanded.Structured[structuredKeys[kind]] != id {
				t.Errorf("%s: served %q, rows %d, SQL id %v (want the Go id %q)", ref, expanded.Evidence.EvidenceRefID, decision.Rows, expanded.Structured[structuredKeys[kind]], id)
			}
			if !strings.Contains(expanded.Evidence.Citation, row.shows) || strings.Contains(expanded.Evidence.Citation, "FOREIGN") {
				t.Errorf("%s: citation %q does not show %q", ref, expanded.Evidence.Citation, row.shows)
			}
		}
	}

	// Collider-left: each ref names a row that does not exist, and whose
	// retired-grammar text equals the text of a row that does. Every subject
	// it names exists and is readable, so the org-wide read REACHES the row
	// statement -- which must match nothing. Nothing is served, to anyone.
	for _, gone := range []struct {
		kind   contractsv1.ContextFabricEvidenceEntityType
		values []string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, []string{"jira:L", "M:jira:N", "blocks:fwd"}},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchyV2, []string{grantedID, "jira:U:V", "jira:W"}},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeamV2, []string{zeroRepoID, "jira:ABC-1:ari:cloud", "identity::team/1", "native_team"}},
		// The twin's raw spelling is not a relation key.
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependencyV2, []string{"jira:T1", "jira:T2", "blocks"}},
		// The (deployment, incident) pair under a source it has no row for.
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncidentV2, []string{grantedID, "dep-1", "INC-1", "manual"}},
	} {
		ref, id := mustMint(t, gone.kind, gone.values...)
		for _, principal := range []storage.Principal{granted, orgWide} {
			recorder.log = nil
			_, decision := resolve.ResolveSourceRow(ctx, principal, string(gone.kind), id)
			if decision.Reason != contextfabric.SourceRowNoRow || decision.Rows != 0 {
				t.Errorf("collider-left %s %q: %+v", gone.kind, gone.values, decision)
			}
			read := false
			for _, line := range recorder.log {
				read = read || strings.Contains(line, "{evidence_locator "+ref+"}")
			}
			if principal.RepositoryScopes == nil && !read {
				t.Errorf("collider-left %s %q: the org-wide read never reached the row statement: %v", gone.kind, gone.values, recorder.log)
			}
		}
	}

	// The retired refs of the same rows never reach a source read.
	for _, retired := range []struct {
		kind contractsv1.ContextFabricEvidenceEntityType
		id   string
	}{
		{contractsv1.ContextFabricEvidenceEntityWorkItemDependency, "jira:A:B:jira:C:blocks:fwd"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemHierarchy, grantedID + ":jira:P:Q:jira:R"},
		{contractsv1.ContextFabricEvidenceEntityWorkItemTeam, grantedID + ":jira:ABC-1:ari:cloud:identity::team/1"},
		{contractsv1.ContextFabricEvidenceEntityDeploymentIncident, "edge-1"},
		{contractsv1.ContextFabricEvidenceEntityProjectTeam, "gitlab:" + integrationOrg + ":gitlab:71133891:gl:full.chaos"},
		{contractsv1.ContextFabricEvidenceEntityProjectTeamV2, strings.TrimPrefix(projectTeam, contractsv1.ContextFabricEvidenceRefPrefix+string(contractsv1.ContextFabricEvidenceEntityProjectTeamV2)+":")},
	} {
		if _, decision := resolve.ResolveSourceRow(ctx, orgWide, string(retired.kind), retired.id); decision.Reason != contextfabric.SourceRowKindOnRecord {
			t.Errorf("%s %q: %+v", retired.kind, retired.id, decision)
		}
	}
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
