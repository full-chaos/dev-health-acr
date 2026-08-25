package devhealthsource_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-4193: querySubjectProjectMemberships (teams_projects_edges.go) swaps
// this source's work_item/pull_request -> project read from
// work_items.project_id directly to the project_membership_presence VIEW
// (ops migration 077). These tests exercise the producer's OWN
// responsibilities under that swap -- the view's own multi-membership and
// survives-a-removal semantics are ops's SQL to prove (077's own doc
// comment); this file proves the Go side never collapses what the view
// already resolved, never falls back to the retired column, states the
// validity discriminator correctly, and surfaces resolution failures as
// telemetry rather than silently.

// chaos4193ProjectsRow mirrors queryProjects/queryWorkItemProjects' shared
// `projects` join target's SELECT list (teams_projects.go's queryProjects,
// used verbatim by the fakeClient match below).
func chaos4193ProjectsRow(id, name, projectKey, provider, state, url string, updatedAt time.Time) []any {
	return projectRow(id, name, projectKey, provider, state, url, 1, updatedAt)
}

func chaos4193MinimalClient(projects []fakeTable, presence fakeTable) *fakeClient {
	client := &fakeClient{tables: append([]fakeTable{}, projects...)}
	client.tables = append(client.tables, presence)
	return client
}

// TestSubjectOnMultipleProjectsEmitsOneEdgePerProject is the acr-side half
// of Context Fabric ruling 2026-08-24 11:40 (presence keyed per (subject,
// project), not per subject): the view can now return SEVERAL active rows
// for the SAME subject, one per board it currently sits on. A pre-ruling
// producer that grouped by subject alone would keep only the LATEST board;
// this producer must not collapse anything at all -- every row the view
// hands it becomes its own edge. This is what actually makes "a PR removed
// from board B keeps its edge to board A" true end to end: the view decides
// which rows survive removal (077's own doc comment/tests), this producer
// only has to not throw any of the survivors away.
func TestSubjectOnMultipleProjectsEmitsOneEdgePerProject(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("board-a", "Board A", "", "github", "open", "", at),
			chaos4193ProjectsRow("board-b", "Board B", "", "github", "open", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("pull_request", "repo-1", "532", "acme/repo", at, "transition", "github", "board-a", "board-a", 1),
		presenceRow("pull_request", "repo-1", "532", "acme/repo", at, "transition", "github", "board-b", "board-b", 1),
	}})

	batch := teamsProjectsBatch(t, client)
	// CHAOS-4109: a transition-sourced edge's RelationshipID now carries a
	// ValidFrom (here, `at`) suffix so multiple intervals for the same
	// (subject, project) pair can coexist -- see relationshipIDIntervalSuffix's
	// own doc comment, teams_projects_edges.go.
	suffix := ":" + at.Format(time.RFC3339Nano) + ":"
	edgeA := relationshipByID(t, batch, "relationship:pull_request_project:repo-1:532:github:board-a"+suffix)
	edgeB := relationshipByID(t, batch, "relationship:pull_request_project:repo-1:532:github:board-b"+suffix)
	if edgeA.From.CanonicalID != edgeB.From.CanonicalID {
		t.Fatalf("both edges must share the SAME pull_request From endpoint (one subject, two memberships), got %q vs %q", edgeA.From.CanonicalID, edgeB.From.CanonicalID)
	}
	if edgeA.To.CanonicalID == edgeB.To.CanonicalID {
		t.Fatalf("edges must target two DISTINCT projects, both resolved to %q", edgeA.To.CanonicalID)
	}
}

// TestWorkItemsProjectIdColumnIsNoLongerReadDirectly is the "old path is
// retired" proof the CHAOS-4193 brief asks for: a work_items row carrying a
// real project_id, seeded ONLY into work_items (never surfaced by
// project_membership_presence -- the fake's default empty-rows-on-no-match
// behaviour stands in for a view that has no row for this subject at all,
// e.g. a live database where the view genuinely returns nothing for it).
// Before this producer's read swap, this exact fixture shape produced a
// BELONGS_TO_PROJECT edge straight from work_items.project_id; after it,
// the presence table is the ONLY source, so no edge may appear.
func TestWorkItemsProjectIdColumnIsNoLongerReadDirectly(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-1", "Project One", "", "linear", "backlog", "", at),
		}},
		// Deliberately shaped like the RETIRED pre-CHAOS-4193
		// queryWorkItemProjects SELECT list (work_item_id, project_id,
		// toString(repo_id), repo slug, updated_at, provider, resolved
		// project id, key_resolution_count) and matched by the FROM clause
		// that statement used to emit. querySubjectProjectMemberships never
		// builds that statement anymore -- if a regression reintroduced it
		// (even alongside the new presence read), this row would answer it
		// and get scanned into a spurious edge, exactly proving the old
		// path is gone rather than merely unexercised by this fixture.
		{match: "FROM work_items AS w FINAL", rows: [][]any{
			{"WI-DIRECT", "proj-1", "repo-1", "acme/repo", at, "linear", "proj-1", uint64(1)},
		}},
	}}
	batch := teamsProjectsBatch(t, client)
	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject {
			t.Fatalf("found a BELONGS_TO_PROJECT edge with no project_membership_presence row to source it from: %+v -- the old work_items.project_id path must be retired", relationship)
		}
	}
}

// TestTransitionSourceEdgeOpensAValidFromWindow and
// TestWorkItemColumnSourceEdgeCarriesNoValidityInterval are a deliberate
// PAIR (mirroring TestClosedOwnershipWindowEndsTheEdge's own "makes the
// discriminator the thing under test" discipline): a producer that always
// set ValidFrom, or never did, would pass exactly one of these two. Only
// together do they pin `source` as the actual discriminator for the
// ValidFrom clause in querySubjectProjectMemberships.
func TestTransitionSourceEdgeOpensAValidFromWindow(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-1", "Project One", "", "github", "open", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("work_item", "repo-1", "gh:acme/repo#7", "acme/repo", at, "transition", "github", "proj-1", "proj-1", 1),
	}})
	batch := teamsProjectsBatch(t, client)
	workItemID, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "gh:acme/repo#7"}, nil)
	if err != nil {
		t.Fatalf("identity.Derive: %v", err)
	}
	edge := relationshipByID(t, batch, "relationship:work_item_project:repo-1:gh:acme/repo#7:github:proj-1:"+at.Format(time.RFC3339Nano)+":")
	if edge.From.CanonicalID != workItemID {
		t.Fatalf("edge From = %q, want %q", edge.From.CanonicalID, workItemID)
	}
	if edge.ValidFrom == nil || !edge.ValidFrom.Equal(at) {
		t.Fatalf("transition-sourced edge ValidFrom = %v, want %v (the row's own observed_at -- the view's own filter guarantees this is the row that MADE the membership active)", edge.ValidFrom, at)
	}
	if edge.ValidTo != nil {
		t.Fatalf("transition-sourced edge ValidTo = %v, want nil -- presence only ever reports ACTIVE memberships, so there is by construction no later closing row", edge.ValidTo)
	}
}

func TestWorkItemColumnSourceEdgeCarriesNoValidityInterval(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 9, 30, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-1", "Project One", "", "linear", "backlog", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("work_item", "repo-1", "linear:CHAOS-1", "acme/repo", at, "work_item_column", "linear", "proj-1", "proj-1", 1),
	}})
	batch := teamsProjectsBatch(t, client)
	edge := relationshipByID(t, batch, "relationship:work_item_project:repo-1:linear:CHAOS-1:linear:proj-1")
	if edge.ValidFrom != nil || edge.ValidTo != nil {
		t.Fatalf("work_item_column-sourced edge validity = (%v, %v), want (nil, nil) -- a plain canonical-column read, presence only, exactly as work_items.project_id always was", edge.ValidFrom, edge.ValidTo)
	}
	if edge.Derivation != contractsv1.ContextFabricDerivationCanonicalStructured || edge.EpistemicStatus != contractsv1.ContextFabricEpistemicObserved {
		t.Fatalf("edge derivation/status = %q/%q, want canonical_structured/observed for both source arms", edge.Derivation, edge.EpistemicStatus)
	}
}

// TestPullRequestSubjectUsesLegacyCanonicalID pins the decision this PR's
// implementation made against the literal brief: pull_request stays
// grandfathered OUT of identity.Registry (chaos3898_s3_census_bridge.go's
// bridgePullRequestSatisfier doc comment; registry_parity_test.go's
// TestRegistryCoversEveryChangedKind "zero exemptions, no extras"), so the
// new BELONGS_TO_PROJECT edge's pull_request endpoint must carry the SAME
// legacy "pull_request:<repo_id>:<number>" id every live PR entity/edge
// already uses (tables.go:295,897), never a `pull_request.v2:...` id --
// which would match no real pull_request node in the graph.
func TestPullRequestSubjectUsesLegacyCanonicalID(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("board-1", "Board One", "", "github", "open", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("pull_request", "repo-9", "532", "acme/repo", at, "transition", "github", "board-1", "board-1", 1),
	}})
	batch := teamsProjectsBatch(t, client)
	edge := relationshipByID(t, batch, "relationship:pull_request_project:repo-9:532:github:board-1:"+at.Format(time.RFC3339Nano)+":")
	if edge.From.Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("From.Kind = %q, want pull_request", edge.From.Kind)
	}
	const wantLegacyID = "pull_request:repo-9:532"
	if edge.From.CanonicalID != wantLegacyID {
		t.Fatalf("From.CanonicalID = %q, want the legacy scheme %q (never identity.Derive's pull_request.v2:...)", edge.From.CanonicalID, wantLegacyID)
	}
	if strings.Contains(edge.From.CanonicalID, ".v2:") {
		t.Fatalf("From.CanonicalID = %q carries the .v2: registry namespace -- pull_request must never be minted through identity.Derive", edge.From.CanonicalID)
	}
}

// pullRequestEntityRow mirrors queryPullRequests' SELECT list exactly
// (tables.go:279-282): toString(p.repo_id), r.repo, p.number, title, state,
// last_synced, created_at, (hasEnded, endedAt) from nullableTimestamp,
// head_branch, body.
func pullRequestEntityRow(repoID, repoSlug string, number uint32, title, state string, lastSynced, createdAt time.Time) []any {
	return []any{repoID, repoSlug, number, title, state, lastSynced, createdAt, uint8(0), time.Unix(0, 0).UTC(), "", ""}
}

// TestPullRequestProjectEdgeResolvesToAnExistingPullRequestNode is the
// dangling-edge regression team-lead asked for: CHAOS-4108 fixed exactly
// this class for the project endpoint (an edge resolving to a canonical id
// no `projects` entity actually carried); this proves the SAME thing holds
// for the pull_request endpoint CHAOS-4193 adds. It runs BOTH real
// projection sources against ONE shared fixture -- ClickHouseProjectionSource
// (which mints the pull_request ENTITY via queryPullRequests, tables.go) and
// TeamsProjectsSource (which mints the BELONGS_TO_PROJECT edge via
// querySubjectProjectMemberships) -- and asserts the edge's From.CanonicalID
// is the EXACT SAME string the entity producer minted, not merely a string
// that happens to look similar. Two producers computing the same id via two
// independent format literals is exactly the failure mode a single shared
// scheme (or, short of that, an explicit cross-check like this) exists to
// catch.
func TestPullRequestProjectEdgeResolvesToAnExistingPullRequestNode(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: []fakeTable{
		{match: "FROM git_pull_requests AS p FINAL INNER JOIN repos AS r FINAL", rows: [][]any{
			pullRequestEntityRow("repo-9", "acme/repo", 532, "Add widget", "open", at, at.Add(-24*time.Hour)),
		}},
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("board-1", "Board One", "", "github", "open", "", at),
		}},
		{match: "FROM project_membership_presence AS m", rows: [][]any{
			presenceRow("pull_request", "repo-9", "532", "acme/repo", at, "transition", "github", "board-1", "board-1", 1),
		}},
	}}

	entitySource, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("NewClickHouseProjectionSource: %v", err)
	}
	entityBatch, available, err := entitySource.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("entity NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("expected the pull_request entity batch to be available")
	}
	prEntity := entityByCanonicalID(t, entityBatch, "pull_request:repo-9:532")
	if prEntity.Subject.Kind != contractsv1.ContextFabricSubjectPullRequest {
		t.Fatalf("pull_request entity kind = %q, want pull_request", prEntity.Subject.Kind)
	}

	edgeBatch := teamsProjectsBatch(t, client)
	edge := relationshipByID(t, edgeBatch, "relationship:pull_request_project:repo-9:532:github:board-1:"+at.Format(time.RFC3339Nano)+":")

	if edge.From.CanonicalID != prEntity.Subject.CanonicalID {
		t.Fatalf("edge From.CanonicalID = %q, pull_request entity CanonicalID = %q -- these MUST be byte-identical or the edge dangles (CHAOS-4108's own defect class, reached on the other endpoint)", edge.From.CanonicalID, prEntity.Subject.CanonicalID)
	}
	if edge.From.Kind != prEntity.Subject.Kind {
		t.Fatalf("edge From.Kind = %q, entity Subject.Kind = %q, want equal", edge.From.Kind, prEntity.Subject.Kind)
	}
}

// TestPresenceRowsMustResolveToExactlyOneProject proves the LEFT JOIN's two
// failure shapes are both visible to telemetry and both omit the edge,
// never guess one. keyResolutionCount 0 models a presence row whose
// (provider, project_id) matches no `projects` row at all (the LEFT JOIN
// miss this producer's own INNER-vs-LEFT choice exists to surface, rather
// than silently vanishing before the scan ever runs); 2 models an
// ambiguous match. Distinguished in the log line so an operator can tell
// "nothing ever wrote this project" from "two projects claim the same id"
// apart.
func TestPresenceRowsMustResolveToExactlyOneProject(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-resolved", "Resolved Project", "", "linear", "backlog", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("work_item", "repo-1", "linear:MISSING-1", "acme/repo", at, "work_item_column", "linear", "proj-missing", "", 0),
		presenceRow("work_item", "repo-1", "linear:AMBIG-1", "acme/repo", at.Add(time.Minute), "work_item_column", "linear", "proj-ambiguous", "proj-ambiguous", 2),
		presenceRow("work_item", "repo-1", "linear:OK-1", "acme/repo", at.Add(2*time.Minute), "work_item_column", "linear", "proj-resolved", "proj-resolved", 1),
	}})

	logged := &bytes.Buffer{}
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch (the resolved row still projects)")
	}

	for _, relationship := range batch.Relationships {
		if relationship.Type == contractsv1.ContextFabricRelationshipBelongsToProject &&
			(strings.Contains(relationship.RelationshipID, "MISSING-1") || strings.Contains(relationship.RelationshipID, "AMBIG-1")) {
			t.Fatalf("an unresolved/ambiguous presence row must never be guessed into an edge: %+v", relationship)
		}
	}
	if _, ok := findRelationship(batch, "relationship:work_item_project:repo-1:linear:OK-1:linear:proj-resolved"); !ok {
		t.Fatalf("the resolved row must still project its edge; relationships=%+v", batch.Relationships)
	}

	output := logged.String()
	if !strings.Contains(output, "unresolved_project_entity=1") {
		t.Fatalf("want unresolved_project_entity=1 (the zero-match row), got:\n%s", output)
	}
	if !strings.Contains(output, "ambiguous_project_entity=1") {
		t.Fatalf("want ambiguous_project_entity=1 (the two-match row), got:\n%s", output)
	}
	if !strings.Contains(output, "presence_rows_work_item_column_work_item=3") {
		t.Fatalf("want a read count of 3 work_item_column/work_item rows (all three, resolved or not -- reads are counted before resolution), got:\n%s", output)
	}
	// Nothing tenant-identifying may reach the log line.
	for _, secret := range []string{"proj-missing", "proj-ambiguous", "proj-resolved", liveOrgID} {
		if strings.Contains(output, secret) {
			t.Errorf("presence telemetry leaked tenant data %q:\n%s", secret, output)
		}
	}
}

func findRelationship(batch contextfabric.ProjectionBatch, relationshipID string) (contractsv1.ContextFabricRelationshipProjection, bool) {
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == relationshipID {
			return relationship, true
		}
	}
	return contractsv1.ContextFabricRelationshipProjection{}, false
}

// TestUnresolvedFanOutIsCountedByRowNotJustByDistinctKey is codex xhigh
// review R1's Medium finding, fixed: a single bad (provider, project_id)
// touching MANY rows must report a row count larger than 1, not just a
// distinct-key count of 1. Before this fix, presenceTelemetryLedger only
// tracked the distinct-key set, so this exact fixture (three rows, one bad
// key) logged "unresolved_project_entity=1" with no way to tell a
// one-row miss from a thousand-row one.
func TestUnresolvedFanOutIsCountedByRowNotJustByDistinctKey(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient(nil, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("work_item", "repo-1", "linear:A", "acme/repo", at, "work_item_column", "linear", "proj-missing", "", 0),
		presenceRow("work_item", "repo-1", "linear:B", "acme/repo", at.Add(time.Minute), "work_item_column", "linear", "proj-missing", "", 0),
		presenceRow("work_item", "repo-1", "linear:C", "acme/repo", at.Add(2*time.Minute), "work_item_column", "linear", "proj-missing", "", 0),
	}})

	logged := &bytes.Buffer{}
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName}); err != nil {
		t.Fatalf("NextProjectionBatch: %v", err)
	}

	output := logged.String()
	if !strings.Contains(output, "unresolved_project_entity=1") {
		t.Fatalf("want unresolved_project_entity=1 (ONE distinct bad key), got:\n%s", output)
	}
	if !strings.Contains(output, "unresolved_project_entity_rows=3") {
		t.Fatalf("want unresolved_project_entity_rows=3 (THREE rows dropped by that one key -- the fan-out this fix makes visible), got:\n%s", output)
	}
}

// TestUnrecognizedSourceFailsClosed is codex xhigh review R1's Low/Medium
// finding, fixed: querySubjectProjectMemberships used to fall through any
// `source` value other than "transition" into work_item_column semantics
// (no interval) silently. A source value neither producer arm can emit
// today (schema drift, a future view change) must now be rejected outright,
// the same discipline the subject_kind switch already applied.
func TestUnrecognizedSourceFailsClosed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-1", "Project One", "", "linear", "backlog", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("work_item", "repo-1", "linear:X", "acme/repo", at, "some_future_source", "linear", "proj-1", "proj-1", 1),
	}})
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err == nil {
		t.Fatal("expected an error for an unrecognized presence `source` value, got nil (silent misroute)")
	}
	if !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("error = %v, want it to name the unknown source", err)
	}
}

// TestUnrecognizedSubjectKindFailsClosed is the sibling closed-vocabulary
// guard for `subject_kind`, proven with the SAME rigor as `source` above --
// this one already returned an error before the R1 fix, but the fix also
// switched it to ProducerRejection so its message actually reaches an
// operator (tableReadError, assemble.go, bounds every OTHER error type's
// text to a generic "dependency unavailable: read <table>" and discards it).
func TestUnrecognizedSubjectKindFailsClosed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	client := chaos4193MinimalClient([]fakeTable{
		{match: "FROM projects FINAL\nWHERE", rows: [][]any{
			chaos4193ProjectsRow("proj-1", "Project One", "", "linear", "backlog", "", at),
		}},
	}, fakeTable{match: "FROM project_membership_presence AS m", rows: [][]any{
		presenceRow("epic", "repo-1", "linear:X", "acme/repo", at, "work_item_column", "linear", "proj-1", "proj-1", 1),
	}})
	source, err := devhealthsource.NewTeamsProjectsSource(client, true)
	if err != nil {
		t.Fatalf("NewTeamsProjectsSource: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: liveOrgID, Source: devhealthsource.TeamsProjectsSourceName})
	if err == nil {
		t.Fatal("expected an error for an unrecognized presence `subject_kind` value, got nil (silent misroute)")
	}
	if !strings.Contains(err.Error(), "unknown subject_kind") {
		t.Fatalf("error = %v, want it to name the unknown subject_kind (a ProducerRejection message must survive tableReadError's bounding)", err)
	}
}
