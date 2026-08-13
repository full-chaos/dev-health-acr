package devhealthsource_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// liveOrgID is the ground-truth organization every live verification in this
// issue was run against (see docs/design/context-fabric-team-project-subjects.md).
const liveOrgID = "70d529e0-3c06-4597-8480-794fd02328b6"

func TestTeamsProjectsSourceDisabledIsANoop(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewTeamsProjectsSource(&fakeClient{}, false)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("disabled source returned an error: %v", err)
	}
	if available {
		t.Fatalf("disabled source must never claim a batch is available: %+v", batch)
	}
}

// teamRow / projectRow mirror the exact column order queryTeams and
// queryProjects SELECT, so a change to either statement's projection list
// without a matching change here fails at Scan rather than silently
// asserting the wrong column.
func teamRow(id, name, description, provider, nativeKey string, isActive uint8, updatedAt time.Time) []any {
	return []any{id, name, description, provider, nativeKey, isActive, updatedAt}
}

func projectRow(id, name, projectKey, provider, state, url string, isActive uint8, updatedAt time.Time) []any {
	return []any{id, name, projectKey, provider, state, url, isActive, updatedAt}
}

// liveShapedTeamsProjectsClient replays the ground-truth org's real row
// shapes (org 70d529e0-3c06-4597-8480-794fd02328b6, read 2026-08-13): three
// teams across three providers, and projects spanning the two id shapes that
// org actually holds -- a provider-composite id for gitlab/linear-key
// projects and a bare UUID for Linear projects.
func liveShapedTeamsProjectsClient() *fakeClient {
	teamsUpdated := time.Date(2026, 8, 13, 19, 0, 3, 742975000, time.UTC)
	projectsUpdated := time.Date(2026, 8, 13, 19, 0, 2, 504000000, time.UTC)
	return &fakeClient{tables: []fakeTable{
		{match: "FROM teams FINAL\nWHERE", rows: [][]any{
			teamRow("gh:ops-team", "Ops Team", "Ops Team", "github", "ops-team", 1, teamsUpdated),
			teamRow("gl:full.chaos", "fullchaos", "", "gitlab", "full.chaos", 1, teamsUpdated.Add(-time.Hour)),
			teamRow("CHAOS", "Fullchaos", "", "linear", "CHAOS", 1, teamsUpdated.Add(-2*time.Hour)),
		}},
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			projectRow("70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891", "chaos-ops", "full.chaos/chaos-ops", "gitlab", "", "https://gitlab.com/full.chaos/chaos-ops", 1, projectsUpdated),
			projectRow("631fcb5f-c3e9-49ff-b17c-07877aaac9b7", "Chaos Draw", "", "linear", "backlog", "https://linear.app/fullchaos/project/chaos-draw-0d9bd4168c10", 1, projectsUpdated.Add(-time.Minute)),
		}},
	}}
}

func enabledTeamsProjectsSource(t *testing.T, client *fakeClient) *devhealthsource.TeamsProjectsSource {
	t.Helper()
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	return source
}

func teamsProjectsBatch(t *testing.T, client *fakeClient) contextfabric.ProjectionBatch {
	t.Helper()
	batch, available, err := enabledTeamsProjectsSource(t, client).NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("enabled source with live-shaped rows reported no batch available")
	}
	return batch
}

func entityByCanonicalID(t *testing.T, batch contextfabric.ProjectionBatch, canonicalID string) contractsv1.ContextFabricEntityProjection {
	t.Helper()
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID == canonicalID {
			return entity
		}
	}
	ids := make([]string, 0, len(batch.Entities))
	for _, entity := range batch.Entities {
		ids = append(ids, entity.Subject.CanonicalID)
	}
	t.Fatalf("no entity with canonical id %q in batch; got %v", canonicalID, ids)
	return contractsv1.ContextFabricEntityProjection{}
}

// TestTeamSubjectsProjectAtTheFactProviderIdentity pins the one part of this
// producer that is NOT a free choice. devhealthfacts minted
// teamPrefix = "team:" (workload.go) and its subjectIndex strips exactly that
// prefix to feed `team_id IN (...)` against capacity_forecasts,
// estimate_coverage_metrics_daily and friends -- whose team_id values were
// live-verified to be the teams.id space ({CHAOS, gl:full.chaos}). Any other
// canonical id shape leaves all five existing team fact providers dark while
// still looking like a working projection.
func TestTeamSubjectsProjectAtTheFactProviderIdentity(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	entity := entityByCanonicalID(t, batch, "team:CHAOS")
	if entity.Subject.Kind != contractsv1.ContextFabricSubjectTeam {
		t.Fatalf("team entity kind = %q, want %q", entity.Subject.Kind, contractsv1.ContextFabricSubjectTeam)
	}
	if entity.Subject.Label != "Fullchaos" {
		t.Fatalf("team label = %q, want the teams.name value %q", entity.Subject.Label, "Fullchaos")
	}
	if got := entity.Authorization.TeamIDs; len(got) != 1 || got[0] != "CHAOS" {
		t.Fatalf("team authorization TeamIDs = %v, want [CHAOS]", got)
	}
	if len(entity.Authorization.ProjectIDs) != 0 || len(entity.Authorization.RepositorySlugs) != 0 {
		t.Fatalf("team authorization must be team-scoped only, got %+v", entity.Authorization)
	}
}

// TestTeamSearchTextCarriesTheLexicalHandlesAUserWouldType guards the whole
// point of the issue: falkorgraph's entitySearchText is Label + Aliases +
// PreviousNames, so a team is only lexically resolvable by the strings put
// in those fields. "CHAOS" (the team key someone actually types) is not a
// substring of the label "Fullchaos"-as-a-word, so without the alias the
// corpus questions naming a team by its key cannot resolve.
func TestTeamSearchTextCarriesTheLexicalHandlesAUserWouldType(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	entity := entityByCanonicalID(t, batch, "team:gl:full.chaos")
	aliases := map[string]bool{}
	for _, alias := range entity.Aliases {
		aliases[alias] = true
	}
	for _, want := range []string{"gl:full.chaos", "full.chaos"} {
		if !aliases[want] {
			t.Fatalf("team aliases = %v, missing lexical handle %q", entity.Aliases, want)
		}
	}
	if entity.ProviderIDs["gitlab"] != "gl:full.chaos" {
		t.Fatalf("team ProviderIDs = %v, want gitlab -> gl:full.chaos", entity.ProviderIDs)
	}
}

// TestProjectSubjectsProjectAtTheWorkItemJoinIdentity pins project canonical
// ids to projects.id -- the only id space work_items.project_id joins
// (live: 16 of 18 distinct values resolve, 3080 of 3086 rows). The two id
// SHAPES that space contains (provider-composite and bare UUID) must both
// survive verbatim; deriving an id from project_key instead would strand
// every Linear project, which has no project_key at all.
func TestProjectSubjectsProjectAtTheWorkItemJoinIdentity(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	composite := entityByCanonicalID(t, batch, "project:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891")
	if composite.Subject.Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("project entity kind = %q, want %q", composite.Subject.Kind, contractsv1.ContextFabricSubjectProject)
	}
	if got := composite.Authorization.ProjectIDs; len(got) != 1 || got[0] != "70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891" {
		t.Fatalf("project authorization ProjectIDs = %v, want the raw projects.id", got)
	}
	bare := entityByCanonicalID(t, batch, "project:631fcb5f-c3e9-49ff-b17c-07877aaac9b7")
	if bare.Subject.Label != "Chaos Draw" {
		t.Fatalf("project label = %q, want the projects.name value %q", bare.Subject.Label, "Chaos Draw")
	}
	if bare.Properties["state"].String == nil || *bare.Properties["state"].String != "backlog" {
		t.Fatalf("project properties = %+v, want state=backlog", bare.Properties)
	}
}

// TestActiveTeamProjectRowsAssertAWindowFreeValidity is the owned-write
// validity discipline from CHAOS-3785 R3-1 read from the outside: an owned
// entity with no validity window of its own must state that explicitly
// (nil/nil), because falkorgraph's owned branch writes both keys either way
// so a nil actively CLEARS a window some earlier referenced/stub write may
// have seeded. A producer that simply never thinks about validity and a
// producer that deliberately asserts "no window" are indistinguishable here
// -- which is exactly why the projected value, not the code path, is what
// gets pinned.
func TestActiveTeamProjectRowsAssertAWindowFreeValidity(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	for _, canonicalID := range []string{"team:CHAOS", "project:631fcb5f-c3e9-49ff-b17c-07877aaac9b7"} {
		entity := entityByCanonicalID(t, batch, canonicalID)
		if entity.ValidFrom != nil || entity.ValidTo != nil {
			t.Fatalf("%s: active row must project a window-free validity, got from=%v to=%v", canonicalID, entity.ValidFrom, entity.ValidTo)
		}
	}
}

// TestInactiveRowsCloseTheirValidityWindowInsteadOfTombstoning is D3.
// is_active = 0 is not a deletion: the ground-truth org's inactive projects
// are state = "completed". A tombstone would erase the very history the
// CHAOS-3781 temporal axis exists to answer over, so the entity stays and
// its window closes at updated_at instead.
func TestInactiveRowsCloseTheirValidityWindowInsteadOfTombstoning(t *testing.T) {
	t.Parallel()
	retiredAt := time.Date(2026, 8, 13, 19, 0, 2, 504000000, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			projectRow("c040b7df-5488-4ddc-adf2-858bcf45ae0b", "Sync observability UX", "", "linear", "completed", "", 0, retiredAt),
		}},
	}}
	batch := teamsProjectsBatch(t, client)
	if len(batch.Tombstones) != 0 {
		t.Fatalf("an inactive project must not be tombstoned, got %+v", batch.Tombstones)
	}
	entity := entityByCanonicalID(t, batch, "project:c040b7df-5488-4ddc-adf2-858bcf45ae0b")
	if entity.ValidTo == nil || !entity.ValidTo.Equal(retiredAt) {
		t.Fatalf("inactive project ValidTo = %v, want updated_at %v", entity.ValidTo, retiredAt)
	}
	if entity.ValidFrom != nil {
		t.Fatalf("inactive project ValidFrom = %v, want nil (no valid_from column exists)", entity.ValidFrom)
	}
	// Paired with TestActiveTeamProjectRowsAssertAWindowFreeValidity, this
	// pins is_active as the DISCRIMINATOR and not merely as one branch that
	// happens to be taken: a producer that closed every row's window, or one
	// that closed none, fails exactly one of the two.
	if entity.Properties["is_active"].Boolean == nil || *entity.Properties["is_active"].Boolean {
		t.Fatalf("inactive project must carry is_active=false, got %+v", entity.Properties)
	}
}

// TestProjectsInTheReservedOrganizationScopeNamespaceAreRejected is the
// end-to-end discharge of the reservation obligation teams_projects.go and
// organizationScopePrefix (clickhouse.go) have both carried since
// CHAOS-3753: queryProjects is the first producer ever to populate real
// ProjectIDs, the same ContextFabricAuthorizationScope field the synthesized
// Organization entity uses for its org-wide scope, so a real project id
// colliding with that namespace would silently inherit organization-wide
// authorization.
//
// The enforcement deliberately lives in the contract
// (ContextFabricEntityProjection.Validate, CHAOS-3753 finding W2) rather
// than in a producer-local guard, so it cannot be forgotten by the next
// producer -- this test therefore pins the BEHAVIOR (rejected, nothing
// projected) and not which layer refuses. Both halves matter: rejection
// must not be a silent rename, because a renamed project would look
// projected while being unjoinable to every work item referencing its real
// id.
func TestProjectsInTheReservedOrganizationScopeNamespaceAreRejected(t *testing.T) {
	t.Parallel()
	reserved := contractsv1.ContextFabricReservedOrganizationScopePrefix + liveOrgID
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			projectRow(reserved, "Impersonator", "", "linear", "backlog", "", 1, time.Unix(1700000000, 0).UTC()),
		}},
	}}
	batch, available, err := enabledTeamsProjectsSource(t, client).NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err == nil {
		t.Fatal("a project id inside the reserved organization-scope namespace must be rejected, not projected")
	}
	if available || len(batch.Entities) != 0 {
		t.Fatalf("nothing may be projected for a colliding project id; got available=%v entities=%+v", available, batch.Entities)
	}
	// A rename would have produced a batch with a *different*, non-colliding
	// canonical id and no error at all -- the failure mode this asserts
	// against, and the one the obligation names explicitly.
	if !strings.Contains(err.Error(), "reserved organization-scope namespace") {
		t.Fatalf("rejection must name the reserved namespace so an operator can act on it; got %v", err)
	}
}

// TestTeamsProjectsSourceStampsItsOwnSourceIdentity is what makes the
// acceptance criterion ("an org rebuild picks the new kinds up with no
// second migration") true: projectionrun checkpoints are keyed
// (org_id, source), so this source carries its own name and version and
// never borrows dev_health_clickhouse's -- whose checkpoint has long since
// advanced past these rows' timestamps.
func TestTeamsProjectsSourceStampsItsOwnSourceIdentity(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	if batch.Source != devhealthsource.TeamsProjectsSourceName {
		t.Fatalf("batch source = %q, want %q", batch.Source, devhealthsource.TeamsProjectsSourceName)
	}
	if batch.Source == devhealthsource.SourceName {
		t.Fatal("teams/projects must not share dev_health_clickhouse's checkpoint identity")
	}
	if batch.SourceVersion != devhealthsource.TeamsProjectsSourceVersion {
		t.Fatalf("batch source version = %q, want %q", batch.SourceVersion, devhealthsource.TeamsProjectsSourceVersion)
	}
	for _, entity := range batch.Entities {
		if entity.SourceVersion != devhealthsource.TeamsProjectsSourceVersion {
			t.Fatalf("entity %s source version = %q, want %q", entity.Subject.CanonicalID, entity.SourceVersion, devhealthsource.TeamsProjectsSourceVersion)
		}
	}
}

// TestTeamsProjectsFullSnapshotClaimsCompleteEnumeration keeps the
// full-snapshot contract honest for the new source: a from-scratch batch
// small enough to fit must claim both FullSnapshot and CompleteEnumeration
// (ContextFabricProjectionBatch.Validate() requires them together), which is
// what lets the backend apply full-snapshot deletion semantics.
func TestTeamsProjectsFullSnapshotClaimsCompleteEnumeration(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedTeamsProjectsClient())
	if !batch.FullSnapshot || !batch.CompleteEnumeration {
		t.Fatalf("from-scratch batch full_snapshot=%v complete_enumeration=%v, want both true", batch.FullSnapshot, batch.CompleteEnumeration)
	}
	if len(batch.Entities) != 5 {
		t.Fatalf("batch entities = %d, want 3 teams + 2 projects", len(batch.Entities))
	}
}

func workItemProjectRow(workItemID, projectID, repoID, repoSlug string, updatedAt time.Time) []any {
	return []any{workItemID, projectID, repoID, repoSlug, updatedAt}
}

func workItemTeamRow(workItemID, teamID, source, confidence, repoID, repoSlug string, computedAt time.Time) []any {
	return []any{workItemID, teamID, source, confidence, repoID, repoSlug, computedAt}
}

func projectTeamRow(projectID, teamID, source string, validFrom time.Time, hasOpenWindow uint8, maxValidTo, updatedAt time.Time) []any {
	return []any{projectID, teamID, source, validFrom, hasOpenWindow, maxValidTo, updatedAt}
}

func relationshipByID(t *testing.T, batch contextfabric.ProjectionBatch, relationshipID string) contractsv1.ContextFabricRelationshipProjection {
	t.Helper()
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == relationshipID {
			return relationship
		}
	}
	ids := make([]string, 0, len(batch.Relationships))
	for _, relationship := range batch.Relationships {
		ids = append(ids, relationship.RelationshipID)
	}
	t.Fatalf("no relationship %q in batch; got %v", relationshipID, ids)
	return contractsv1.ContextFabricRelationshipProjection{}
}

// liveShapedEdgeClient replays the ground-truth org's real edge row shapes:
// a Linear work item whose repo_id is the zero UUID (3298 of that org's 3304
// primary attributions), a gitlab work item with a real repo, and the
// collapsed ownership row the Trap C GROUP BY produces.
func liveShapedEdgeClient() *fakeClient {
	at := time.Date(2026, 8, 13, 19, 0, 2, 504000000, time.UTC)
	client := liveShapedTeamsProjectsClient()
	client.tables = append(client.tables,
		fakeTable{match: "FROM work_items AS w FINAL", rows: [][]any{
			workItemProjectRow("linear:CHAOS-3802", "631fcb5f-c3e9-49ff-b17c-07877aaac9b7", zeroRepositoryUUID, "", at),
		}},
		fakeTable{match: "FROM work_item_team_attributions AS a FINAL", rows: [][]any{
			workItemTeamRow("linear:CHAOS-3802", "CHAOS", "native_team", "high", zeroRepositoryUUID, "", at),
			workItemTeamRow("gl:42", "gl:full.chaos", "project_ownership", "medium", "cd620f84-2602-8dea-7809-8d1f11825cf4", "full.chaos/dev-health-ops", at),
		}},
		fakeTable{match: "FROM team_project_ownership FINAL", rows: [][]any{
			projectTeamRow("70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891", "gl:full.chaos", "native",
				time.Date(2026, 8, 12, 13, 8, 20, 79000000, time.UTC), 1, time.Unix(0, 0).UTC(), at),
		}},
	)
	return client
}

// zeroRepositoryUUID mirrors devhealthsource's own zeroRepositoryID: the
// placeholder repo_id a Linear-sourced work item carries by design.
const zeroRepositoryUUID = "00000000-0000-0000-0000-000000000000"

// TestWorkItemProjectEdgeUsesTheCanonicalStructuredColumn pins the
// work_item -> project edge to work_items.project_id and to the id space the
// project subject itself is projected at, so the edge's To endpoint actually
// lands on a real projected node rather than a dangling stub.
func TestWorkItemProjectEdgeUsesTheCanonicalStructuredColumn(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	edge := relationshipByID(t, batch, "relationship:work_item_project:linear:CHAOS-3802:631fcb5f-c3e9-49ff-b17c-07877aaac9b7")
	if edge.Type != contractsv1.ContextFabricRelationshipBelongsToProject {
		t.Fatalf("edge type = %q, want BELONGS_TO_PROJECT", edge.Type)
	}
	if edge.To.CanonicalID != "project:631fcb5f-c3e9-49ff-b17c-07877aaac9b7" || edge.To.Kind != contractsv1.ContextFabricSubjectProject {
		t.Fatalf("edge To = %+v, want the projected project subject identity", edge.To)
	}
	// A direct canonical column, unlike the two attribution-derived edges.
	if edge.Derivation != contractsv1.ContextFabricDerivationCanonicalStructured || edge.EpistemicStatus != contractsv1.ContextFabricEpistemicObserved {
		t.Fatalf("edge derivation/status = %q/%q, want canonical_structured/observed", edge.Derivation, edge.EpistemicStatus)
	}
}

// TestAttributionDerivedEdgesAreNotLabelledCanonicalTruth is the
// "graph discoveries may not mint canonical truth" rule (this package's
// AGENTS.md) applied to the two Ops-COMPUTED attribution tables. Both carry a
// source enum spanning native through manual_fallback/inferred; presenting a
// manual_fallback attribution as observed canonical structure would be a lie
// a consumer cannot detect. The enums must ride along so it can.
func TestAttributionDerivedEdgesAreNotLabelledCanonicalTruth(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	for _, id := range []string{
		"relationship:work_item_team:gl:42:gl:full.chaos",
		"relationship:project_team:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891:gl:full.chaos:native",
	} {
		edge := relationshipByID(t, batch, id)
		if edge.Derivation == contractsv1.ContextFabricDerivationCanonicalStructured {
			t.Fatalf("%s: an Ops-computed attribution must not be labelled canonical_structured", id)
		}
		if edge.EpistemicStatus != contractsv1.ContextFabricEpistemicSourceAsserted {
			t.Fatalf("%s: epistemic status = %q, want source_asserted", id, edge.EpistemicStatus)
		}
		if edge.Properties["attribution_source"].String == nil {
			t.Fatalf("%s: the attribution's own source enum must ride along, got %+v", id, edge.Properties)
		}
	}
	confidence := relationshipByID(t, batch, "relationship:work_item_team:gl:42:gl:full.chaos").Properties["attribution_confidence"]
	if confidence.String == nil || *confidence.String != "medium" {
		t.Fatalf("work-item attribution confidence = %+v, want medium", confidence)
	}
}

// TestWorkItemTeamEdgeScopesOnTheWorkItemsOwnRepository is the CHAOS-3785
// zero-UUID trap for the new producer. work_item_team_attributions carries
// its own repo_id column, but 5077 of that org's 5089 rows hold the zero
// UUID, so authorization must come from work_items via LEFT JOIN repos. A
// Linear work item is repo-less BY DESIGN and must get the no-repository
// sentinel, never the orphan one -- the distinction CHAOS-3785 exists to keep.
func TestWorkItemTeamEdgeScopesOnTheWorkItemsOwnRepository(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	linear := relationshipByID(t, batch, "relationship:work_item_team:linear:CHAOS-3802:CHAOS")
	if got := linear.Authorization.RepositorySlugs; len(got) != 1 || got[0] != "acr-context-fabric:no-repository" {
		t.Fatalf("repo-less-by-design work item scoped as %v, want the no-repository sentinel (not the orphan one)", got)
	}
	gitlab := relationshipByID(t, batch, "relationship:work_item_team:gl:42:gl:full.chaos")
	if got := gitlab.Authorization.RepositorySlugs; len(got) != 1 || got[0] != "full.chaos/dev-health-ops" {
		t.Fatalf("work item with a real repository scoped as %v, want its repo slug", got)
	}
}

// TestProjectTeamEdgeStatesAnOpenOwnershipWindow pins the one edge in this
// issue with real validity data. team_project_ownership's windows are
// collapsed per edge before they ever reach here (see queryProjectTeams'
// Trap C note); an edge with any still-open window is currently owned and
// must carry no end, or a temporal read would report live ownership as
// historical.
func TestProjectTeamEdgeStatesAnOpenOwnershipWindow(t *testing.T) {
	t.Parallel()
	batch := teamsProjectsBatch(t, liveShapedEdgeClient())
	edge := relationshipByID(t, batch, "relationship:project_team:70d529e0-3c06-4597-8480-794fd02328b6:gitlab:71133891:gl:full.chaos:native")
	if edge.ValidFrom == nil {
		t.Fatal("a collapsed ownership edge must state when ownership began")
	}
	if !edge.ValidFrom.Equal(time.Date(2026, 8, 12, 13, 8, 20, 79000000, time.UTC)) {
		t.Fatalf("ValidFrom = %v, want the earliest observed valid_from", edge.ValidFrom)
	}
	if edge.ValidTo != nil {
		t.Fatalf("ValidTo = %v, want nil while any ownership window is still open", edge.ValidTo)
	}
	if got := edge.Authorization; len(got.ProjectIDs) != 1 || len(got.TeamIDs) != 1 {
		t.Fatalf("project->team edge authorization = %+v, want both endpoints' scopes", got)
	}
}

// TestClosedOwnershipWindowEndsTheEdge is the companion that makes
// has_open_window the DISCRIMINATOR rather than one branch that happens to be
// taken: a producer returning nil either way passes the test above.
func TestClosedOwnershipWindowEndsTheEdge(t *testing.T) {
	t.Parallel()
	began := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM team_project_ownership FINAL", rows: [][]any{
			projectTeamRow("project-x", "team-y", "manual", began, 0, ended, ended),
		}},
	}}
	edge := relationshipByID(t, teamsProjectsBatch(t, client), "relationship:project_team:project-x:team-y:manual")
	if edge.ValidTo == nil || !edge.ValidTo.Equal(ended) {
		t.Fatalf("ValidTo = %v, want the latest closed window %v", edge.ValidTo, ended)
	}
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(began) {
		t.Fatalf("ValidFrom = %v, want %v", edge.ValidFrom, began)
	}
}

// TestEveryTeamsProjectsEdgeTypeIsDeclared keeps
// TeamsProjectsRelationshipTypes honest against what the producers actually
// emit -- the list cmd/acr-projector's AC-3779-9 cross-wiring test trusts. A
// declared-but-unproduced type, or a produced-but-undeclared one, is the
// exact drift AC-3779-9 exists to prevent.
func TestEveryTeamsProjectsEdgeTypeIsDeclared(t *testing.T) {
	t.Parallel()
	declared := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, kind := range devhealthsource.TeamsProjectsRelationshipTypes() {
		declared[kind] = true
	}
	emitted := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, relationship := range teamsProjectsBatch(t, liveShapedEdgeClient()).Relationships {
		emitted[relationship.Type] = true
		if !declared[relationship.Type] {
			t.Fatalf("producer emitted %q but TeamsProjectsRelationshipTypes does not declare it", relationship.Type)
		}
	}
	for kind := range declared {
		if !emitted[kind] {
			t.Fatalf("TeamsProjectsRelationshipTypes declares %q but no producer emitted it against live-shaped rows", kind)
		}
	}
}
