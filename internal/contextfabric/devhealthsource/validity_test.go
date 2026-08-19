package devhealthsource_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-3781: every producer must emit a valid-time window derived from
// its source row's own interval columns. Before this, nothing here set
// ValidFrom/ValidTo at all, so the graph carried no windows and
// AC-3781-4's "an edge whose validity window excludes the requested time
// is not returned" had nothing to filter on.
//
// These tests read the windows off a projected batch rather than
// inspecting SQL, so they keep holding if the queries are rewritten.

func projectOneBatch(t *testing.T, tables []fakeTable) contextfabric.ProjectionBatch {
	t.Helper()
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(),
		contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("no batch was available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
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
	// CHAOS-3802 carried a second copy of this helper whose failure listed
	// what the batch DID hold. Merging the copies kept that: a producer
	// that projects the wrong canonical ID is the common failure, and
	// naming only the id that was absent does not say which one it got.
	ids := make([]string, 0, len(batch.Entities))
	for _, entity := range batch.Entities {
		ids = append(ids, entity.Subject.CanonicalID)
	}
	t.Fatalf("no entity %q in batch; got %v", canonicalID, ids)
	return contractsv1.ContextFabricEntityProjection{}
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

func requireWindow(t *testing.T, label string, validFrom, validTo *time.Time, wantFrom, wantTo *time.Time) {
	t.Helper()
	switch {
	case wantFrom == nil && validFrom != nil:
		t.Fatalf("%s: valid_from = %v, want unbounded", label, validFrom)
	case wantFrom != nil && validFrom == nil:
		t.Fatalf("%s: valid_from is unbounded, want %v", label, wantFrom)
	case wantFrom != nil && !validFrom.Equal(*wantFrom):
		t.Fatalf("%s: valid_from = %v, want %v", label, validFrom, wantFrom)
	}
	switch {
	case wantTo == nil && validTo != nil:
		t.Fatalf("%s: valid_to = %v, want open-ended", label, validTo)
	case wantTo != nil && validTo == nil:
		t.Fatalf("%s: valid_to is open-ended, want %v", label, wantTo)
	case wantTo != nil && !validTo.Equal(*wantTo):
		t.Fatalf("%s: valid_to = %v, want %v", label, validTo, wantTo)
	}
}

// TestProducersEmitClosedValidityWindows covers the entities whose source
// row records BOTH ends of the interval -- the ones a historical read can
// actually exclude.
func TestProducersEmitClosedValidityWindows(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	ended := time.Date(2026, 2, 20, 17, 30, 0, 0, time.UTC)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		switch table.match {
		case "FROM work_items AS w":
			// A completed work item: valid from creation to completion.
			tables[index].rows = [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service",
				"Investigate checkout flake", "done", "", at, created, uint8(1), ended, "", "", "", []string{}}}
		case "FROM git_pull_requests AS p":
			// A merged pull request.
			tables[index].rows = [][]any{{"repo-1", "example-org/widget-service", uint32(1042),
				"Typed session tokens", "merged", at, created, uint8(1), ended, "", ""}}
		case "FROM operational_incidents AS i":
			// A resolved incident.
			tables[index].rows = [][]any{{"incident-1", "repo-1", "example-org/widget-service",
				"Widget incident", "resolved", "low", at, uint8(0), uint8(1), created, uint8(1), ended, ""}}
		default:
			tables[index].rows = nil
		}
	}
	batch := projectOneBatch(t, tables)

	for _, subject := range []string{"work_item.v2:repo-1:WIDGET-101", "pull_request:repo-1:1042", "incident:incident-1"} {
		entity := entityByCanonicalID(t, batch, subject)
		requireWindow(t, subject, entity.ValidFrom, entity.ValidTo, &created, &ended)
	}

	// The BELONGS_TO_REPOSITORY edge inherits the member's window: a
	// membership stops being valid exactly when the member does.
	edge := relationshipByID(t, batch, "relationship:belongs_to_repository:work_item.v2:repo-1:WIDGET-101")
	requireWindow(t, "belongs_to_repository", edge.ValidFrom, edge.ValidTo, &created, &ended)
}

// TestProducersLeaveOpenIntervalsUnbounded is the other half: a nil end
// must mean "still valid", never a zero timestamp. Getting this wrong
// would make every open work item drop out of every historical read,
// because a 1970 valid_to excludes every requested time.
func TestProducersLeaveOpenIntervalsUnbounded(t *testing.T) {
	t.Parallel()

	created := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[index].rows = [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service",
				"Investigate checkout flake", "in_progress", "", at, created, uint8(0), zeroTime, "", "", "", []string{}}}
			continue
		}
		tables[index].rows = nil
	}
	// ci_pipeline_runs is not part of baseTables. A run still executing:
	// started, never finished.
	tables = append(tables, fakeTable{match: "FROM ci_pipeline_runs AS c", rows: [][]any{
		{"run-1", "repo-1", "main", "running", "example-org/widget-service", at, created, uint8(0), zeroTime, ""}}})
	batch := projectOneBatch(t, tables)

	for _, subject := range []string{"work_item.v2:repo-1:WIDGET-101", "ci_pipeline_run.v2:repo-1:run-1"} {
		entity := entityByCanonicalID(t, batch, subject)
		requireWindow(t, subject, entity.ValidFrom, entity.ValidTo, &created, nil)
	}
}

// TestHierarchyEdgeIntersectsBothEndpointWindows proves the edge rule:
// a PART_OF association is valid only while BOTH work items are, so the
// window is the later start and the earlier end -- not either endpoint's
// own window.
func TestHierarchyEdgeIntersectsBothEndpointWindows(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	parentCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	childCreated := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC) // later start wins
	childEnded := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)    // earlier end wins
	parentEnded := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index := range tables {
		tables[index].rows = nil
	}
	// The PART_OF producer ("FROM work_items AS c") is not part of
	// baseTables, so it is added here rather than overridden.
	tables = append(tables, fakeTable{match: "FROM work_items AS c", rows: [][]any{
		{"WIDGET-101", "WIDGET-050", "repo-1", "example-org/widget-service", at,
			childCreated, uint8(1), childEnded, parentCreated, uint8(1), parentEnded, "repo-1"}}})
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:work_item_hierarchy:repo-1:WIDGET-101:WIDGET-050")
	requireWindow(t, "part_of", edge.ValidFrom, edge.ValidTo, &childCreated, &childEnded)
}

// TestHierarchyEdgeStaysOpenWhenBothEndpointsAreOpen guards the nil
// handling in edgeValidity: an unbounded end on either side must not be
// mistaken for an early end.
func TestHierarchyEdgeStaysOpenWhenBothEndpointsAreOpen(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	parentCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	childCreated := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index := range tables {
		tables[index].rows = nil
	}
	tables = append(tables, fakeTable{match: "FROM work_items AS c", rows: [][]any{
		{"WIDGET-101", "WIDGET-050", "repo-1", "example-org/widget-service", at,
			childCreated, uint8(0), zeroTime, parentCreated, uint8(0), zeroTime, "repo-1"}}})
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:work_item_hierarchy:repo-1:WIDGET-101:WIDGET-050")
	requireWindow(t, "part_of", edge.ValidFrom, edge.ValidTo, &childCreated, nil)
}

// TestSourceVersionIsBumpedForValidityWindows pins the version bump that
// forces the rebuild. Without it, an already-projected organization keeps
// a graph of window-less nodes that a historical read would admit at
// every requested time -- see ClickHouseSourceVersion's doc comment.
func TestSourceVersionIsBumpedForValidityWindows(t *testing.T) {
	t.Parallel()

	if devhealthsource.ClickHouseSourceVersion == "devhealthsource.clickhouse.v3" {
		t.Fatal("emitting validity windows changes what this producer projects; the source version must advance past v3 so ErrProjectionSourceVersionChanged forces a rebuild")
	}
	// CHAOS-3833 advanced past CHAOS-3781's v4 for the same reason on a
	// different axis: the embed-text producer fields change what this
	// producer projects, so v4 must be as unreachable as v3 is.
	if devhealthsource.ClickHouseSourceVersion == "devhealthsource.clickhouse.v4" {
		t.Fatal("emitting the embed-text fields changes what this producer projects; the source version must advance past v4 so ErrProjectionSourceVersionChanged forces a rebuild")
	}
	if devhealthsource.ClickHouseSourceVersion != "devhealthsource.clickhouse.v5" {
		t.Fatalf("ClickHouseSourceVersion = %q, want devhealthsource.clickhouse.v5", devhealthsource.ClickHouseSourceVersion)
	}
}

// --- CHAOS-3781 codex round-1 regressions ---

// TestF5_DependencyEdgeIntersectsBothEndpoints is round-1 F5: the edge's
// window used to come from the SOURCE work item alone, which asserted the
// dependency was valid while the TARGET did not yet exist -- and made the
// window depend on which endpoint happened to be joined rather than on the
// data.
func TestF5_DependencyEdgeIntersectsBothEndpoints(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sourceCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	targetCreated := time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC) // later start wins
	targetEnded := time.Date(2026, 2, 10, 0, 0, 0, 0, time.UTC)   // earlier end wins
	sourceEnded := time.Date(2026, 2, 25, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[index].rows = [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at,
				sourceCreated, uint8(1), sourceEnded, uint8(1), targetCreated, uint8(1), targetEnded, "repo-1"}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:work_item_dependency:repo-1:WIDGET-101:WIDGET-099:blocks")
	requireWindow(t, "dependency", edge.ValidFrom, edge.ValidTo, &targetCreated, &targetEnded)
}

// TestF5_DependencyEdgeOmitsAnUnresolvedTarget: target_work_item_id is not
// guaranteed to name a work item (it can carry a cross-system PR
// reference), so the join is LEFT. CHAOS-3898 (design brief §1.3/§1.5):
// once work_item's canonical id is repo-scoped (work_item.v2:<repo>:<id>),
// an unresolved target has no repo_id to derive one FROM at all -- there is
// no safe id to mint. Rather than dangling-reference the pre-CHAOS-3898
// unqualified "work_item:<id>" shape (which would reintroduce exactly the
// cross-repo collision class this ticket closes) the row is OMITTED,
// consuming its page budget via a progress candidate so pagination still
// advances -- never silently dropped, and never fabricated. The
// work_item_ref non-authoritative stub kind (design brief §1.5) that heals
// this deterministically on re-sync is out of this slice's scope (a new
// contract-first SubjectKind); see this PR's own description for the
// explicit follow-up.
func TestF5_DependencyEdgeOmitsAnUnresolvedTarget(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sourceCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			// uint8(0) on both target flags: the LEFT JOIN found nothing.
			tables[index].rows = [][]any{{"WIDGET-101", "ghpr:owner/repo#7", "blocks", "repo-1", "example-org/widget-service", at,
				sourceCreated, uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, ""}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	for _, relationship := range batch.Relationships {
		if relationship.Type == "work_item_dependency" || relationship.From.CanonicalID == "work_item.v2:repo-1:WIDGET-101" {
			t.Fatalf("an unresolved dependency target must be omitted, not dangling-referenced; got %+v", relationship)
		}
	}
}

// TestF4_DeploymentIncidentEdgeDerivesItsWindowFromBothEndpoints is
// round-1 F4: work_graph_deployment_incident_edges carries no interval of
// its own, and the edge was previously left unbounded -- so it was
// admitted at EVERY requested time, correlating a deployment with an
// incident years before either happened. The semantic interval IS
// knowable from the endpoints, so leaving it absent was the wrong kind of
// honest.
func TestF4_DeploymentIncidentEdgeDerivesItsWindowFromBothEndpoints(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	deployStarted := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	incidentStarted := time.Date(2026, 2, 3, 0, 0, 0, 0, time.UTC) // later start wins
	incidentResolved := time.Date(2026, 2, 5, 0, 0, 0, 0, time.UTC)
	deployFinished := time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_graph_deployment_incident_edges AS e" {
			tables[index].rows = [][]any{{"edge-1", "deploy-1", "incident-1", "example-org/widget-service", at,
				uint8(1), deployStarted, uint8(1), deployFinished,
				uint8(1), incidentStarted, uint8(1), incidentResolved, "repo-1"}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:deployment_incident:edge-1")
	requireWindow(t, "deployment_incident", edge.ValidFrom, edge.ValidTo, &incidentStarted, &incidentResolved)
}

// TestF4_DeploymentIncidentEdgeStaysUnboundedWhenEndpointsDoNotResolve:
// both joins are LEFT, so an edge whose endpoint rows are missing still
// projects -- with the window absent, which is the admit-count-label path
// and the correct answer when the interval genuinely is unknowable.
func TestF4_DeploymentIncidentEdgeStaysUnboundedWhenEndpointsDoNotResolve(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_graph_deployment_incident_edges AS e" {
			tables[index].rows = [][]any{{"edge-1", "deploy-1", "incident-1", "example-org/widget-service", at,
				uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, uint8(0), zeroTime, "repo-1"}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:deployment_incident:edge-1")
	requireWindow(t, "deployment_incident", edge.ValidFrom, edge.ValidTo, nil, nil)
}

// --- CHAOS-3825: disjoint endpoint windows ---
//
// edgeValidity intersects the two endpoints' windows: the later start and
// the earlier end. When the two windows are DISJOINT that intersection is
// empty, and the naive pair (later start, earlier end) is INVERTED --
// valid_to strictly before valid_from. ContextFabricRelationshipProjection
// rejects that ("valid_to precedes valid_from"), and because
// ContextFabricProjectionBatch.Validate() is all-or-nothing, one such row
// poisons the ENTIRE batch: NextProjectionBatch errors, the coordinator
// holds the checkpoint, and the same poisoned page rebuilds every tick --
// the organization's projection is wedged forever, exactly the failure
// shape queryWorkItemHierarchy's self-reference filter already exists to
// prevent, reached through the temporal axis instead.
//
// Disjoint endpoint windows are ORDINARY data, not corruption: a review
// submitted after its pull request merged is a post-merge approval, which
// GitHub allows and dev ClickHouse holds today (CHAOS-3825: 35 such rows
// for one organization).
//
// The ruled representation is the DEGENERATE half-open window
// [later-start, later-start): the association never held while both
// endpoints were valid, and a zero-width half-open interval is exactly
// that statement. The contract accepts it (only a STRICTLY earlier end is
// rejected), every time-filtered read admits it nowhere (no instant
// satisfies valid_from <= t < valid_to when the bounds are equal), and
// structural reads that ignore the temporal axis still see the edge. The
// alternatives were both worse: widening to either endpoint's own window
// asserts an interval the source never stated, and dropping the edge
// silently loses a real association.

// TestPostMergeReviewEdgeCollapsesToADegenerateWindow builds the exact
// live shape through the real query/assembly path: a review submitted
// AFTER its pull request's coalesce(merged_at, closed_at). Before the fix
// this failed at batch validation (tables.go:610).
func TestPostMergeReviewEdgeCollapsesToADegenerateWindow(t *testing.T) {
	t.Parallel()

	pullRequestCreated := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	pullRequestMerged := time.Date(2026, 2, 1, 17, 0, 0, 0, time.UTC)
	// The post-merge approval: submitted after the pull request ended, so
	// the review's [submitted, unbounded) window and the pull request's
	// [created, merged) window never overlap.
	submitted := time.Date(2026, 2, 10, 11, 0, 0, 0, time.UTC)

	tables := baseTables(submitted)
	for index := range tables {
		tables[index].rows = nil
	}
	tables = append(tables, fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{
		{"review-1", "repo-1", uint32(1042), "approved", submitted, "example-org/widget-service",
			pullRequestCreated, uint8(1), pullRequestMerged, "Typed session tokens"}}})
	batch := projectOneBatch(t, tables)

	// The review itself is untouched: a submitted review is never
	// retracted, so its own window stays open-ended from submitted_at.
	// Only the ASSOCIATION collapses.
	entity := entityByCanonicalID(t, batch, "pull_request_review.v2:repo-1:1042:review-1")
	requireWindow(t, "pull_request_review", entity.ValidFrom, entity.ValidTo, &submitted, nil)

	edge := relationshipByID(t, batch, "relationship:belongs_to_pull_request:pull_request_review.v2:repo-1:1042:review-1")
	requireWindow(t, "belongs_to_pull_request", edge.ValidFrom, edge.ValidTo, &submitted, &submitted)
}

// TestDisjointDependencyEdgeCollapsesToADegenerateWindow is the same
// defect reached through tables.go:418 -- proving the fix is class-wide
// (inside edgeValidity, inherited by all four call sites) rather than
// patched at the one site the live data happened to hit. A dependency
// whose target work item was created only after the source one closed is
// the ordinary "this was blocked by something filed later" shape.
func TestDisjointDependencyEdgeCollapsesToADegenerateWindow(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	sourceCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	sourceEnded := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	targetCreated := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // after the source ended
	targetEnded := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[index].rows = [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at,
				sourceCreated, uint8(1), sourceEnded, uint8(1), targetCreated, uint8(1), targetEnded, "repo-1"}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:work_item_dependency:repo-1:WIDGET-101:WIDGET-099:blocks")
	requireWindow(t, "dependency", edge.ValidFrom, edge.ValidTo, &targetCreated, &targetCreated)
}

// TestEdgeValidityNeverInverts is the unit-level half: the invariant
// belongs to edgeValidity, not to any one caller, so it is asserted
// through the function directly across the combinations the fixtures do
// not reach. Each case pins the EXACT expected pair, so a fix that
// clamped the window into an interval the source never asserted (say, by
// widening valid_to to the later end) fails here even though it would
// satisfy the invariant.
func TestEdgeValidityNeverInverts(t *testing.T) {
	t.Parallel()

	jan1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jan10 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	feb1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	feb20 := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name                       string
		fromValidFrom, fromValidTo *time.Time
		toValidFrom, toValidTo     *time.Time
		wantValidFrom, wantValidTo *time.Time
	}{{
		// The live shape: the second endpoint starts after the first ended.
		name:          "disjoint, from ends before to starts",
		fromValidFrom: &jan1, fromValidTo: &jan10, toValidFrom: &feb1, toValidTo: &feb20,
		wantValidFrom: &feb1, wantValidTo: &feb1,
	}, {
		// The mirror image: argument order must not change the answer.
		name:          "disjoint, to ends before from starts",
		fromValidFrom: &feb1, fromValidTo: &feb20, toValidFrom: &jan1, toValidTo: &jan10,
		wantValidFrom: &feb1, wantValidTo: &feb1,
	}, {
		// Disjoint with only the inverting bounds recorded: still empty.
		name:          "disjoint with nil outer bounds",
		fromValidFrom: nil, fromValidTo: &jan10, toValidFrom: &feb1, toValidTo: nil,
		wantValidFrom: &feb1, wantValidTo: &feb1,
	}, {
		// TOUCHING, not disjoint: the earlier end equals the later start.
		// Already zero-width and already accepted, so the guard must
		// leave it exactly as it was rather than "correcting" it.
		name:          "touching, end equals start",
		fromValidFrom: &jan1, fromValidTo: &feb1, toValidFrom: &feb1, toValidTo: &feb20,
		wantValidFrom: &feb1, wantValidTo: &feb1,
	}, {
		// The ordinary overlapping case: the true intersection, unchanged.
		name:          "overlapping",
		fromValidFrom: &jan1, fromValidTo: &feb1, toValidFrom: &jan10, toValidTo: &feb20,
		wantValidFrom: &jan10, wantValidTo: &feb1,
	}, {
		name:          "one endpoint open-ended",
		fromValidFrom: &jan1, fromValidTo: nil, toValidFrom: &jan10, toValidTo: &feb20,
		wantValidFrom: &jan10, wantValidTo: &feb20,
	}, {
		name:          "both open-ended",
		fromValidFrom: &jan1, fromValidTo: nil, toValidFrom: &jan10, toValidTo: nil,
		wantValidFrom: &jan10, wantValidTo: nil,
	}, {
		name:          "no bounds at all",
		fromValidFrom: nil, fromValidTo: nil, toValidFrom: nil, toValidTo: nil,
		wantValidFrom: nil, wantValidTo: nil,
	}, {
		// A start with no end on either side cannot invert, and must not
		// be closed by the guard.
		name:          "starts only",
		fromValidFrom: &jan1, fromValidTo: nil, toValidFrom: &feb1, toValidTo: nil,
		wantValidFrom: &feb1, wantValidTo: nil,
	}, {
		// An end with no start on either side: unbounded below, so there
		// is nothing to invert against.
		name:          "ends only",
		fromValidFrom: nil, fromValidTo: &jan10, toValidFrom: nil, toValidTo: &feb20,
		wantValidFrom: nil, wantValidTo: &jan10,
	}}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			validFrom, validTo := devhealthsource.EdgeValidityForTest(
				testCase.fromValidFrom, testCase.fromValidTo, testCase.toValidFrom, testCase.toValidTo)
			requireWindow(t, testCase.name, validFrom, validTo, testCase.wantValidFrom, testCase.wantValidTo)
			if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
				t.Fatalf("%s: edgeValidity returned an inverted window [%v, %v)", testCase.name, validFrom, validTo)
			}
		})
	}
}

// TestEdgeValidityDoesNotAliasItsArguments guards the collapse's
// mechanics: returning the SAME pointer for both ends would make a caller
// that later adjusted one bound silently move the other. Every call site
// here passes pointers it also uses for the endpoint entity's own window
// (tables.go:610 passes the review entity's validFrom straight in), so
// aliasing would be a live hazard, not a theoretical one.
func TestEdgeValidityDoesNotAliasItsArguments(t *testing.T) {
	t.Parallel()

	jan1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jan10 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	feb1 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	feb20 := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	validFrom, validTo := devhealthsource.EdgeValidityForTest(&jan1, &jan10, &feb1, &feb20)
	if validFrom == validTo {
		t.Fatal("edgeValidity returned the same pointer for both bounds; a caller adjusting one would move the other")
	}
	*validTo = feb20
	if !validFrom.Equal(feb1) {
		t.Fatalf("mutating valid_to changed valid_from to %v", validFrom)
	}
}

// TestDisjointHierarchyEdgeCollapsesToADegenerateWindow is the third of
// the four edgeValidity call sites (tables.go:486). It is here rather
// than left to the unit tests because the sweep for this issue found
// live rows for it too -- 42 child/parent pairs whose windows do not
// overlap -- so it is a REACHED site, not a hypothetical one. A child
// closed before its parent was created is the ordinary "an old ticket was
// re-parented under an epic filed later" shape.
func TestDisjointHierarchyEdgeCollapsesToADegenerateWindow(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	childCreated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	childEnded := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	parentCreated := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC) // after the child closed
	parentEnded := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index := range tables {
		tables[index].rows = nil
	}
	tables = append(tables, fakeTable{match: "FROM work_items AS c", rows: [][]any{
		{"WIDGET-101", "WIDGET-050", "repo-1", "example-org/widget-service", at,
			childCreated, uint8(1), childEnded, parentCreated, uint8(1), parentEnded, "repo-1"}}})
	batch := projectOneBatch(t, tables)

	edge := relationshipByID(t, batch, "relationship:work_item_hierarchy:repo-1:WIDGET-101:WIDGET-050")
	requireWindow(t, "part_of", edge.ValidFrom, edge.ValidTo, &parentCreated, &parentCreated)
}
