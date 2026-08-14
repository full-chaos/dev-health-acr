package devhealthsource_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// testCursor builds a devhealthsource cursor string without depending on
// the package's unexported encodeCursor -- same wire shape
// (base64.RawURLEncoding of {"since":...,"after":...}, cursor.go), used to
// start a test directly in the incremental() path (a non-empty cursor)
// without the fullSnapshot org-anchor candidate complicating row-boundary
// arithmetic.
func testCursor(t *testing.T, since time.Time, after string) string {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Since time.Time `json:"since"`
		After string    `json:"after"`
	}{Since: since, After: after})
	if err != nil {
		t.Fatalf("encode test cursor: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

type fakeTable struct {
	match string
	rows  [][]any
	err   error
	// cursorOf extracts (observedAt, rowKey) from a raw row, matching
	// exactly what that table's real query's sincePredicate/orderBy pair
	// (tables.go) compares -- CHAOS-3753 codex finding W2: the old fake
	// ignored the since/after bindings entirely and always replayed every
	// row regardless of cursor, which is why the C5 keyset-tiebreaker bug
	// was never caught by this suite. Any test exercising pagination or
	// same-timestamp ordering must set this; tests that only care about one
	// unpaginated batch may leave it nil (all rows are returned, unfiltered,
	// as before).
	cursorOf func(row []any) (observedAt time.Time, rowKey string)
}

type fakeClient struct {
	tables []fakeTable
}

func (c *fakeClient) Query(_ context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	requireOrgIDBinding(statement, bindings)
	for _, table := range c.tables {
		if strings.Contains(statement, table.match) {
			if table.err != nil {
				return nil, table.err
			}
			rows := table.rows
			if table.cursorOf != nil {
				rows = applyCursor(rows, table.cursorOf, bindings)
			}
			return &fakeScanner{rows: rows}, nil
		}
	}
	return &fakeScanner{}, nil
}

// applyCursor reproduces, in the fake, the exact semantics of
// sincePredicate + orderBy (tables.go): keep only rows strictly after
// (since, after) in (observedAt, rowKey) lexicographic order, then sort by
// the same key. Without this, a fake table can't distinguish "already
// returned on a previous page" from "not yet returned", which is precisely
// the class of bug C5 introduced (see the doc comment on fakeTable.cursorOf).
func applyCursor(rows [][]any, cursorOf func(row []any) (time.Time, string), bindings []contextpacket.ClickHouseBinding) [][]any {
	var since time.Time
	var after string
	for _, binding := range bindings {
		switch binding.Name {
		case "since":
			if value, ok := binding.Value.(time.Time); ok {
				since = value
			}
		case "after":
			if value, ok := binding.Value.(string); ok {
				after = value
			}
		}
	}
	kept := make([][]any, 0, len(rows))
	for _, row := range rows {
		observedAt, rowKey := cursorOf(row)
		if observedAt.After(since) || (observedAt.Equal(since) && rowKey > after) {
			kept = append(kept, row)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		ti, ki := cursorOf(kept[i])
		tj, kj := cursorOf(kept[j])
		if ti.Equal(tj) {
			return ki < kj
		}
		return ti.Before(tj)
	})
	return kept
}

// requireOrgIDBinding is CHAOS-3753 codex finding W2's fake-assertion fix:
// the fake previously accepted any bindings at all, silently ignoring them,
// so a query that forgot to bind org_id (and therefore would have scoped a
// real ClickHouse WHERE clause to nothing, or -- worse -- to every tenant)
// would still pass every test in this file. Every production query text
// this package builds references the {org_id:String} placeholder; this
// panics (not t.Fatalf -- fakeClient has no *testing.T handle, and a
// missing binding here is a programming error in the query under test, not
// a test-data assumption) if a statement claims to use it but no matching
// binding was actually supplied.
func requireOrgIDBinding(statement string, bindings []contextpacket.ClickHouseBinding) {
	if !strings.Contains(statement, "{org_id:String}") {
		return
	}
	for _, binding := range bindings {
		if binding.Name == "org_id" {
			if value, ok := binding.Value.(string); ok && strings.TrimSpace(value) != "" {
				return
			}
			panic("devhealthsource_test: fakeClient.Query received a blank org_id binding for a statement that requires one")
		}
	}
	panic("devhealthsource_test: fakeClient.Query received a statement referencing {org_id:String} with no org_id binding -- the query under test forgot to scope itself to an organization")
}

type fakeScanner struct {
	rows [][]any
	row  int
}

func (s *fakeScanner) Next() bool { return s.row < len(s.rows) }

func (s *fakeScanner) Scan(dest ...any) error {
	row := s.rows[s.row]
	for index, target := range dest {
		switch value := target.(type) {
		case *string:
			*value = row[index].(string)
		case *int64:
			*value = row[index].(int64)
		case *uint32:
			*value = row[index].(uint32)
		case *uint8:
			*value = row[index].(uint8)
		case *time.Time:
			*value = row[index].(time.Time)
		default:
			return errors.New("devhealthsource_test: unsupported scan destination")
		}
	}
	s.row++
	return nil
}

func (s *fakeScanner) Err() error   { return nil }
func (s *fakeScanner) Close() error { return nil }

// repoCursorOf matches queryRepositories' sincePredicate/orderBy pair
// (tables.go: sincePredicate(cursor, "last_synced", "id")): column 3 is
// last_synced, column 0 is id.
func repoCursorOf(row []any) (time.Time, string) { return row[3].(time.Time), row[0].(string) }

// zeroTime is what nullableTimestamp's ifNull fallback selects when the
// underlying column is NULL (validity.go). Every fake row pairing it with
// a uint8(0) presence flag models an OPEN interval -- an unfinished CI
// run, an unmerged pull request, an unresolved incident -- which
// optionalTime turns back into a nil bound, never a 1970 timestamp.
var zeroTime = time.Unix(0, 0).UTC()

func repoRow(id, slug, provider string, at time.Time) fakeTable {
	return fakeTable{match: "FROM repos", rows: [][]any{{id, slug, provider, at, at}}, cursorOf: repoCursorOf}
}

func baseTables(at time.Time) []fakeTable {
	return []fakeTable{
		repoRow("repo-1", "example-org/widget-service", "synthetic", at),
		{match: "FROM work_items AS w", rows: [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime}}},
		{match: "FROM git_pull_requests AS p", rows: [][]any{{"repo-1", "example-org/widget-service", uint32(1042), "Typed session tokens", "open", at, at, uint8(0), zeroTime}}},
		{match: "FROM deployments AS d", rows: [][]any{{"repo-1", "example-org/widget-service", "deploy-1", "success", "production", at, uint8(1), at, uint8(0), zeroTime}}},
		{match: "FROM operational_incidents AS i", rows: [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(0), uint8(1), at, uint8(0), zeroTime}}},
		{match: "FROM work_item_dependencies AS d", rows: [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime}}},
		{match: "FROM work_graph_deployment_incident_edges AS e", rows: [][]any{{"edge-1", "deploy-1", "incident-1", "example-org/widget-service", at, uint8(1), at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime}}},
	}
}

// TestQueryWorkItemsDistinguishesRepolessFromOrphanedAuthorization is
// CHAOS-3785 codex round-1 finding F2: a work item whose repos join found no
// match is repo-less for one of two distinct reasons, and the two must not
// collapse into a single sentinel. repo_id = the zero UUID means "never had
// a repository" (WIDGET-LINEAR, the Linear shape this issue exists for);
// any OTHER repo_id that still fails to resolve means "named one that
// didn't" (WIDGET-ORPHAN, a sync race / deleted repository / stale seed
// data -- live-verified: 5 such rows exist across 5 organizations in dev
// ClickHouse today). A third row (the base fixture's WIDGET-101,
// repo-backed) proves the ordinary case is untouched.
func TestQueryWorkItemsDistinguishesRepolessFromOrphanedAuthorization(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for i, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime},
				{"WIDGET-LINEAR", "00000000-0000-0000-0000-000000000000", "", "Linear-sourced item", "open", "", at, at, uint8(0), zeroTime},
				{"WIDGET-ORPHAN", "99999999-9999-9999-9999-999999999999", "", "Orphaned repo_id", "open", "", at, at, uint8(0), zeroTime},
			}}
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}

	want := map[string][]string{
		"work_item:WIDGET-101":    {"example-org/widget-service"},
		"work_item:WIDGET-LINEAR": {"acr-context-fabric:no-repository"},
		"work_item:WIDGET-ORPHAN": {"acr-context-fabric:orphaned-repository"},
	}
	found := map[string]bool{}
	for _, entity := range batch.Entities {
		wantSlugs, tracked := want[entity.Subject.CanonicalID]
		if !tracked {
			continue
		}
		found[entity.Subject.CanonicalID] = true
		if len(entity.Authorization.RepositorySlugs) != len(wantSlugs) || entity.Authorization.RepositorySlugs[0] != wantSlugs[0] {
			t.Fatalf("%s authorization = %+v, want RepositorySlugs=%v", entity.Subject.CanonicalID, entity.Authorization, wantSlugs)
		}
	}
	for id := range want {
		if !found[id] {
			t.Fatalf("expected %s to be projected: entities=%+v", id, batch.Entities)
		}
	}

	// WIDGET-ORPHAN has no resolvable repository, so -- exactly like a
	// Linear-shaped work item -- it must never carry a BELONGS_TO_REPOSITORY
	// edge (there is no repository entity to point one at).
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "work_item:WIDGET-ORPHAN" && relationship.Type == "BELONGS_TO_REPOSITORY" {
			t.Fatalf("WIDGET-ORPHAN unexpectedly carries a BELONGS_TO_REPOSITORY edge: %+v", relationship)
		}
	}
}

// TestNextProjectionBatchLogsOrphanedWorkItemCount is CHAOS-3785 codex
// round-2 finding R2-3: an orphaned work item is otherwise indistinguishable
// from any other projected row unless someone goes looking for the sentinel
// value directly in the graph. A batch containing one must surface the
// count through the source's logger; a batch containing none must stay
// quiet (this signal exists to flag something worth investigating, not to
// add an always-on line to every ordinary tick).
func TestNextProjectionBatchLogsOrphanedWorkItemCount(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const orphanRepoID = "99999999-9999-9999-9999-999999999999"

	orphanTables := baseTables(at)
	for i, table := range orphanTables {
		switch table.match {
		case "FROM work_items AS w":
			orphanTables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime},
				{"WIDGET-ORPHAN", orphanRepoID, "", "Orphaned repo_id", "open", "", at, at, uint8(0), zeroTime},
			}}
		case "FROM work_item_dependencies AS d":
			// WIDGET-ORPHAN carries TWO orphan-scoped candidates (its own
			// entity, plus this dependency edge rooted at it) -- codex
			// round-3 finding R3-4: the log must still report exactly ONE
			// orphaned work item, not two, proving the count is over
			// DISTINCT work-item IDs, not raw candidates.
			orphanTables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
				{"WIDGET-ORPHAN", "WIDGET-101", "blocks", orphanRepoID, "", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
			}}
		}
	}
	orphanSource, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: orphanTables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var orphanLogs bytes.Buffer
	orphanSource.WithLogger(slog.New(slog.NewJSONHandler(&orphanLogs, nil)))
	batch, available, err := orphanSource.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	logged := orphanLogs.String()
	if !strings.Contains(logged, `"orphaned_work_items":1`) {
		t.Fatalf("expected the orphan count to report 1 DISTINCT work item (not 2 candidates), got: %s", logged)
	}
	// R3-3: the warning must carry the same batch-identity vocabulary
	// coordinator.go's own per-run "projection batch applied" log uses, so
	// an operator can correlate the two (or notice this batch never reached
	// that later line at all).
	for _, want := range []string{
		`"source":"` + devhealthsource.SourceName + `"`,
		`"batch_id":"` + batch.BatchID + `"`,
		`"cursor":"` + batch.Cursor + `"`,
		`"next_cursor":"` + batch.NextCursor + `"`,
	} {
		if !strings.Contains(logged, want) {
			t.Fatalf("expected the orphan warning to carry %s, got: %s", want, logged)
		}
	}

	cleanSource, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: baseTables(at)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	var cleanLogs bytes.Buffer
	cleanSource.WithLogger(slog.New(slog.NewJSONHandler(&cleanLogs, nil)))
	if _, _, err := cleanSource.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if cleanLogs.Len() != 0 {
		t.Fatalf("expected no log output for a batch with zero orphaned work items, got: %s", cleanLogs.String())
	}
}

// TestClickHouseProjectionSourceProjectsPullRequestReviewsAndCIRuns is
// CHAOS-3753 codex finding C7's regression test: the design doc declared
// git_pull_request_reviews and ci_pipeline_runs coverage, but no batch
// ever emitted them. Proves both now project as entities (with a
// BELONGS_TO_PULL_REQUEST / BELONGS_TO_REPOSITORY relationship each).
func TestClickHouseProjectionSourceProjectsPullRequestReviewsAndCIRuns(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	tables = append(tables,
		fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{{"review-1", "repo-1", uint32(1042), "approved", at, "example-org/widget-service", at, uint8(0), zeroTime}}},
		fakeTable{match: "FROM ci_pipeline_runs AS c", rows: [][]any{{"run-1", "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at}}},
	)
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}

	foundReview, foundReviewRelationship := false, false
	foundRun, foundRunRelationship := false, false
	for _, entity := range batch.Entities {
		switch entity.Subject.CanonicalID {
		case "pull_request_review:review-1":
			foundReview = true
			if entity.Subject.Kind != contractsv1.ContextFabricSubjectPullRequestReview {
				t.Fatalf("review entity kind = %q", entity.Subject.Kind)
			}
		case "ci_pipeline_run:run-1":
			foundRun = true
			if entity.Subject.Kind != contractsv1.ContextFabricSubjectCIRun {
				t.Fatalf("CI run entity kind = %q", entity.Subject.Kind)
			}
		}
	}
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "pull_request_review:review-1" && relationship.Type == "BELONGS_TO_PULL_REQUEST" {
			foundReviewRelationship = true
			if relationship.To.CanonicalID != "pull_request:repo-1:1042" {
				t.Fatalf("review relationship target = %q", relationship.To.CanonicalID)
			}
		}
		if relationship.From.CanonicalID == "ci_pipeline_run:run-1" && relationship.Type == "BELONGS_TO_REPOSITORY" {
			foundRunRelationship = true
		}
	}
	if !foundReview || !foundReviewRelationship {
		t.Fatalf("pull request review not fully projected: entity=%t relationship=%t, batch=%+v", foundReview, foundReviewRelationship, batch)
	}
	if !foundRun || !foundRunRelationship {
		t.Fatalf("CI run not fully projected: entity=%t relationship=%t, batch=%+v", foundRun, foundRunRelationship, batch)
	}
}

// TestClickHouseProjectionSourceProjectsEveryClosedVocabularyRelationshipType
// is CHAOS-3779's central regression test, binding several ACs at once:
//
//   - AC-3779-5 (first half): a real source row deterministically maps to
//     the correct closed-vocabulary Type, for both new CHAOS-3779 producers
//     (PART_OF, from work_items.parent_id) and the formalized BLOCKS
//     producer that TRD §19.13 Correction 1 found already flowing.
//   - The single most important regression in the whole issue: RELATES_TO
//     and DUPLICATES are the two OTHER live work_item_dependencies.
//     relationship_type values Correction 1 found (verified live against
//     ClickHouse: 'relates_to', 'blocks', 'duplicates'). Closing
//     ContextFabricRelationshipProjection.Type into an enum without
//     covering these would make every 'relates_to' and 'duplicates' row in
//     production fail loudly -- exactly the H4 failure this issue exists
//     to close, just inverted (loud failure of GOOD data instead of silent
//     admission of BAD data). RELATED_TO (the ifNull default for a NULL
//     relationship_type) is covered the same way.
//   - AC-3779-7: source.NextProjectionBatch takes no ModelRuntime
//     parameter, and this package (grep-verified) imports nothing from
//     model_runtime.go -- projection is structurally incapable of model
//     participation, not just incidentally free of it. This call proves
//     every edge type still projects with that structural guarantee in
//     force.
//   - AC-3779-8: every relationship this source projects carries a
//     derivation method, an epistemic status, a non-empty authorization
//     scope, at least one evidence reference, and a source version --
//     asserted explicitly below, not just implied by batch.Validate()
//     passing.
//
// everyRelationshipTypeFixtureTables seeds fakeTables covering every
// relationship type devhealthsource.ProducedRelationshipTypes() declares
// (BELONGS_TO_REPOSITORY/BELONGS_TO_PULL_REQUEST/CORRELATED_WITH_INCIDENT
// come from baseTables itself; the rest are added here). Shared by
// TestClickHouseProjectionSourceProjectsEveryClosedVocabularyRelationshipType
// and TestProducedRelationshipTypesMatchesWhatProjectionActuallyProduces so
// both exercise the identical fixture.
func everyRelationshipTypeFixtureTables(at time.Time) []fakeTable {
	tables := baseTables(at)
	for i, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			// blocks/relates_to/duplicates are the three live values TRD
			// §19.13 Correction 1 verified against ClickHouse;
			// related_to is the ifNull(...,'related_to') default for a
			// NULL relationship_type -- already collapsed to a non-null
			// string by the real SQL's ifNull before Go ever scans it.
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
				{"WIDGET-101", "WIDGET-098", "relates_to", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
				{"WIDGET-101", "WIDGET-097", "duplicates", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
				{"WIDGET-101", "WIDGET-096", "related_to", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
			}}
		}
	}
	return append(tables,
		fakeTable{match: "FROM git_pull_request_reviews AS r", rows: [][]any{{"review-1", "repo-1", uint32(1042), "approved", at, "example-org/widget-service", at, uint8(0), zeroTime}}},
		// FROM work_items AS c is queryWorkItemHierarchy's child-side
		// alias -- distinct from baseTables' "FROM work_items AS w"
		// (queryWorkItems' entity query), so this does not collide with
		// it in fakeClient's substring match.
		fakeTable{match: "FROM work_items AS c", rows: [][]any{{"WIDGET-101", "WIDGET-050", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, at, uint8(0), zeroTime}}},
	)
}

func TestClickHouseProjectionSourceProjectsEveryClosedVocabularyRelationshipType(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := everyRelationshipTypeFixtureTables(at)
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	// No ModelRuntime is constructed, configured, or passed anywhere on
	// this call path (AC-3779-7) -- NextProjectionBatch's signature has no
	// parameter for one.
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}

	wantDependencyTargets := map[string]contractsv1.ContextFabricRelationshipType{
		"work_item:WIDGET-099": contractsv1.ContextFabricRelationshipBlocks,
		"work_item:WIDGET-098": contractsv1.ContextFabricRelationshipRelatesTo,
		"work_item:WIDGET-097": contractsv1.ContextFabricRelationshipDuplicates,
		"work_item:WIDGET-096": contractsv1.ContextFabricRelationshipRelatedTo,
	}
	seenTypes := map[contractsv1.ContextFabricRelationshipType]bool{}
	partOfFound := false
	for _, relationship := range batch.Relationships {
		seenTypes[relationship.Type] = true

		// AC-3779-8: every envelope field is present, not merely
		// non-erroring under Validate().
		if relationship.Derivation == "" {
			t.Fatalf("relationship %s has no derivation method: %+v", relationship.RelationshipID, relationship)
		}
		if relationship.EpistemicStatus == "" {
			t.Fatalf("relationship %s has no epistemic status: %+v", relationship.RelationshipID, relationship)
		}
		if len(relationship.Authorization.RepositorySlugs)+len(relationship.Authorization.ProjectIDs)+len(relationship.Authorization.TeamIDs) == 0 {
			t.Fatalf("relationship %s has an empty authorization scope: %+v", relationship.RelationshipID, relationship)
		}
		if len(relationship.EvidenceRefIDs) == 0 {
			t.Fatalf("relationship %s has no evidence references: %+v", relationship.RelationshipID, relationship)
		}
		if relationship.SourceVersion == "" {
			t.Fatalf("relationship %s has no source version: %+v", relationship.RelationshipID, relationship)
		}

		if relationship.From.CanonicalID != "work_item:WIDGET-101" {
			continue
		}
		if relationship.Type == contractsv1.ContextFabricRelationshipPartOf {
			partOfFound = true
			if relationship.To.CanonicalID != "work_item:WIDGET-050" {
				t.Fatalf("PART_OF target = %q, want work_item:WIDGET-050", relationship.To.CanonicalID)
			}
			continue
		}
		if wantType, tracked := wantDependencyTargets[relationship.To.CanonicalID]; tracked {
			if relationship.Type != wantType {
				t.Fatalf("relationship work_item:WIDGET-101 -> %s Type = %q, want %q", relationship.To.CanonicalID, relationship.Type, wantType)
			}
			delete(wantDependencyTargets, relationship.To.CanonicalID)
		}
	}
	if !partOfFound {
		t.Fatalf("PART_OF relationship not found in batch: %+v", batch.Relationships)
	}
	if len(wantDependencyTargets) != 0 {
		t.Fatalf("dependency relationships not found for targets: %+v, batch=%+v", wantDependencyTargets, batch.Relationships)
	}

	// AC-3779-7: every edge type this source can produce is present in one
	// deterministic, model-runtime-free projection.
	wantTypes := []contractsv1.ContextFabricRelationshipType{
		contractsv1.ContextFabricRelationshipBelongsToRepository,
		contractsv1.ContextFabricRelationshipBelongsToPullRequest,
		contractsv1.ContextFabricRelationshipCorrelatedWithIncident,
		contractsv1.ContextFabricRelationshipRelatedTo,
		contractsv1.ContextFabricRelationshipBlocks,
		contractsv1.ContextFabricRelationshipPartOf,
		contractsv1.ContextFabricRelationshipRelatesTo,
		contractsv1.ContextFabricRelationshipDuplicates,
	}
	for _, want := range wantTypes {
		if !seenTypes[want] {
			t.Fatalf("relationship type %q was not produced by this projection batch: %+v", want, batch.Relationships)
		}
	}
}

// TestProducedRelationshipTypesMatchesWhatProjectionActuallyProduces is
// CHAOS-3779 codex round-1 finding L4: devhealthsource.
// ProducedRelationshipTypes() is a hand-maintained list (the AC-3779-9
// cross-check in cmd/acr-projector reads it as ground truth), which can
// silently drift from what the real projection code actually does -- a
// query function added later and forgotten in the list, or a type
// removed from a query but left in the list, would both go undetected by
// a test that only inspects the declared list itself. This binds the
// declared list to a REAL executed projection batch, built from the exact
// same fixture as
// TestClickHouseProjectionSourceProjectsEveryClosedVocabularyRelationshipType,
// and requires exact set equality in both directions: every declared type
// must actually appear in the batch, and every type the batch actually
// produced must be declared.
func TestProducedRelationshipTypesMatchesWhatProjectionActuallyProduces(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: everyRelationshipTypeFixtureTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}

	actual := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, relationship := range batch.Relationships {
		actual[relationship.Type] = true
	}
	declared := devhealthsource.ProducedRelationshipTypes()
	if len(declared) == 0 {
		t.Fatal("ProducedRelationshipTypes() is empty -- this test would pass vacuously with nothing to check")
	}
	for _, want := range declared {
		if !actual[want] {
			t.Fatalf("ProducedRelationshipTypes() declares %q, but a real projection run over the full fixture never produced it -- the hand-maintained list has drifted from the actual projection mapping path", want)
		}
	}
	declaredSet := map[contractsv1.ContextFabricRelationshipType]bool{}
	for _, d := range declared {
		declaredSet[d] = true
	}
	for got := range actual {
		if !declaredSet[got] {
			t.Fatalf("a real projection run produced relationship type %q, but ProducedRelationshipTypes() does not declare it -- the AC-3779-9 cross-check would silently miss this type", got)
		}
	}
}

// TestClickHouseProjectionSourceKeepsBothEdgesForASourceTargetPairWithTwoRelationshipTypes
// is CHAOS-3779 codex round-1 finding H2's regression test. work_item_
// dependencies' natural key is (org, source, target, relationship_type) --
// live ClickHouse holds real rows where the same (source, target) pair
// carries both 'blocks' and 'relates_to' (verified against the running
// database: org 70d529e0-3c06-4597-8480-794fd02328b6,
// linear:CHAOS-3292->linear:CHAOS-3289 and
// linear:CHAOS-3300->linear:CHAOS-3219). Before this fix, RelationshipID
// and the keyset-pagination rowKey both derived from (source, target)
// alone, so two rows sharing a pair collapsed onto one identity: within a
// single page, ContextFabricProjectionBatch.Validate() would reject the
// whole batch outright ("relationship IDs must be unique within a
// batch") -- this test seeds exactly that shape (one source_work_item_id,
// two target rows sharing ONE pair by using the SAME target for both
// rows) and proves both a 'blocks' edge and a 'relates_to' edge survive
// into the batch, with distinct RelationshipIDs, and the batch still
// validates.
func TestClickHouseProjectionSourceKeepsBothEdgesForASourceTargetPairWithTwoRelationshipTypes(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for i, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "WIDGET-050", "blocks", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
				{"WIDGET-101", "WIDGET-050", "relates_to", "repo-1", "example-org/widget-service", at, at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime},
			}}
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation -- H2 regression: two relationship_type rows for the same (source, target) pair must not collide on RelationshipID: %v", err)
	}

	var blocksFound, relatesToFound bool
	seenIDs := map[string]int{}
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID != "work_item:WIDGET-101" || relationship.To.CanonicalID != "work_item:WIDGET-050" {
			continue
		}
		seenIDs[relationship.RelationshipID]++
		switch relationship.Type {
		case contractsv1.ContextFabricRelationshipBlocks:
			blocksFound = true
		case contractsv1.ContextFabricRelationshipRelatesTo:
			relatesToFound = true
		}
	}
	if !blocksFound || !relatesToFound {
		t.Fatalf("blocksFound=%t relatesToFound=%t, want both edges present for the same (source, target) pair: %+v", blocksFound, relatesToFound, batch.Relationships)
	}
	for id, count := range seenIDs {
		if count > 1 {
			t.Fatalf("RelationshipID %q used by %d relationships, want each relationship_type to have its own identity", id, count)
		}
	}
}

func TestClickHouseProjectionSourceFullSnapshotIsOneCompleteBatch(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: baseTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if !batch.FullSnapshot || !batch.CompleteEnumeration {
		t.Fatalf("full snapshot batch must claim complete enumeration: %+v", batch)
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	// organization + repository + work item + pull request + deployment + incident
	if len(batch.Entities) != 6 {
		t.Fatalf("entities = %d, want 6: %+v", len(batch.Entities), batch.Entities)
	}
	// 4 BELONGS_TO_REPOSITORY (work item, PR, deployment, incident) + 1 work item dependency + 1 deployment/incident edge
	if len(batch.Relationships) != 6 {
		t.Fatalf("relationships = %d, want 6: %+v", len(batch.Relationships), batch.Relationships)
	}
	if len(batch.Tombstones) != 0 {
		t.Fatalf("unexpected tombstones: %+v", batch.Tombstones)
	}
	if batch.Cursor != "" || batch.NextCursor == "" {
		t.Fatalf("cursor = %q, next_cursor = %q", batch.Cursor, batch.NextCursor)
	}
}

func TestClickHouseProjectionSourceIncidentTombstoneOnSoftDelete(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM operational_incidents AS i" {
			tables[index].rows = [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(1), uint8(1), at, uint8(0), zeroTime}}
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	found := false
	for _, tombstone := range batch.Tombstones {
		if tombstone.Kind == "incident" && tombstone.CanonicalID == "incident:incident-1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("deleted incident did not become a tombstone: %+v", batch.Tombstones)
	}
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID == "incident:incident-1" {
			t.Fatalf("deleted incident must not also be projected as a live entity: %+v", entity)
		}
	}
	// A tombstoned incident has no BELONGS_TO_REPOSITORY relationship either.
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "incident:incident-1" {
			t.Fatalf("deleted incident must not carry a live relationship: %+v", relationship)
		}
	}
}

// TestClickHouseProjectionSourceFullSnapshotPagesWhenAggregateEntitiesExceedTheContractBound
// is CHAOS-3753 codex round-2 finding K4's regression test: fullSnapshot
// only treated an organization as oversized when a SINGLE table's query
// was individually truncated at snapshotPerQueryCap (150). Seven
// entity-producing tables can each independently stay under that per-table
// cap (149 rows apiece) while their SUM (1043 entities) still exceeds the
// v1 contract's aggregate 1000-entity bound -- oversized stayed false, so
// fullSnapshot proceeded straight to buildBatch, which failed contract
// validation instead of paging.
func TestClickHouseProjectionSourceFullSnapshotPagesWhenAggregateEntitiesExceedTheContractBound(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const perTable = 149 // strictly under snapshotPerQueryCap (150): no single table ever reports truncated
	repoRows, workItemRows, pullRequestRows, deploymentRows, incidentRows, reviewRows, ciRunRows :=
		make([][]any, 0, perTable), make([][]any, 0, perTable), make([][]any, 0, perTable), make([][]any, 0, perTable), make([][]any, 0, perTable), make([][]any, 0, perTable), make([][]any, 0, perTable)
	for i := 0; i < perTable; i++ {
		id := fmt.Sprintf("%03d", i)
		repoRows = append(repoRows, []any{"repo-" + id, "example-org/repo-" + id, "synthetic", at, at})
		workItemRows = append(workItemRows, []any{"WIDGET-" + id, "repo-1", "example-org/widget-service", "task " + id, "in_progress", "", at, at, uint8(0), zeroTime})
		pullRequestRows = append(pullRequestRows, []any{"repo-1", "example-org/widget-service", uint32(i), "PR " + id, "open", at, at, uint8(0), zeroTime})
		deploymentRows = append(deploymentRows, []any{"repo-1", "example-org/widget-service", "deploy-" + id, "success", "production", at, uint8(1), at, uint8(0), zeroTime})
		incidentRows = append(incidentRows, []any{"incident-" + id, "repo-1", "example-org/widget-service", "incident " + id, "open", "low", at, uint8(0), uint8(1), at, uint8(0), zeroTime})
		reviewRows = append(reviewRows, []any{"review-" + id, "repo-1", uint32(i), "approved", at, "example-org/widget-service", at, uint8(0), zeroTime})
		ciRunRows = append(ciRunRows, []any{"run-" + id, "repo-1", "main", "success", "example-org/widget-service", at, at, uint8(1), at})
	}
	tables := baseTables(at)
	for index, table := range tables {
		switch table.match {
		case "FROM repos":
			tables[index].rows = repoRows
		case "FROM work_items AS w":
			tables[index].rows = workItemRows
		case "FROM git_pull_requests AS p":
			tables[index].rows = pullRequestRows
		case "FROM deployments AS d":
			tables[index].rows = deploymentRows
		case "FROM operational_incidents AS i":
			tables[index].rows = incidentRows
		default:
			tables[index].rows = nil
		}
	}
	tables = append(tables,
		fakeTable{match: "FROM git_pull_request_reviews AS r", rows: reviewRows},
		fakeTable{match: "FROM ci_pipeline_runs AS c", rows: ciRunRows},
	)
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("next projection batch: %v (aggregate entity count across tables must trigger paging, not a contract-bound validation failure)", err)
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if batch.FullSnapshot {
		t.Fatalf("an aggregate-oversized organization must page (FullSnapshot=false), not claim a complete single-batch snapshot: %+v", batch)
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	if len(batch.Entities) > 1000 {
		t.Fatalf("entities = %d, must stay within the v1 contract bound", len(batch.Entities))
	}
}

// TestClickHouseProjectionSourcePagedBatchNeverSplitsARowsCandidatesAcrossAPageBoundary
// is CHAOS-3753 codex round-2 finding K2's regression test. RULING
// (team-lead): truncation happens on SQL-row boundaries only -- an
// entity's relationship must never be split from it by a page boundary;
// the cursor advances only past fully-emitted rows. pagedBatch used to
// slice the flattened, merged candidate list at a fixed index
// (incrementalBatchCap candidates, not rows), which could land between an
// entity and its relationship (two candidates sharing one source row) and
// silently drop the relationship forever -- the cursor would still advance
// past that row's position (the entity was the last emitted candidate, so
// it becomes the batch's NextCursor position), so the next page's strict
// "> after" predicate would never revisit that row again.
//
// Seeds incrementalBatchCap-1 (199) single-candidate repos rows, then one
// work_item row (entity + BELONGS_TO_REPOSITORY relationship, two
// candidates from one row) timestamped strictly after all of them. Sorted,
// that's 201 raw candidates across exactly incrementalBatchCap (200) rows
// -- the work item's entity is the 200th candidate (right at the old
// candidate-count cap) and its relationship is the 201st (just past it):
// exactly the split position. Starts from a non-empty cursor (bypassing
// fullSnapshot's org-anchor candidate, which would otherwise consume one
// of the row slots and shift this arithmetic) to land squarely in the
// incremental()/pagedBatch(includeOrganization=false) path under test.
func TestClickHouseProjectionSourcePagedBatchNeverSplitsARowsCandidatesAcrossAPageBoundary(t *testing.T) {
	t.Parallel()
	const incrementalBatchCap = 200 // must match clickhouse.go's incrementalBatchCap
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)

	repoRows := make([][]any, 0, incrementalBatchCap-1)
	for i := 0; i < incrementalBatchCap-1; i++ {
		repoRows = append(repoRows, []any{fmt.Sprintf("repo-%03d", i), fmt.Sprintf("example-org/repo-%03d", i), "synthetic", at.Add(time.Duration(i) * time.Second), at})
	}
	workItemAt := at.Add(incrementalBatchCap * time.Second) // strictly after every repos row
	tables := baseTables(at)
	for index, table := range tables {
		switch table.match {
		case "FROM repos":
			tables[index].rows = repoRows
		case "FROM work_items AS w":
			tables[index].rows = [][]any{{"WIDGET-1", "repo-000", "example-org/repo-000", "Investigate checkout flake", "in_progress", "", workItemAt, workItemAt, uint8(0), zeroTime}}
			tables[index].cursorOf = workItemCursorOf
		default:
			tables[index].rows = nil
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	// Start strictly before every seeded row, in the incremental() path.
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: testCursor(t, at.Add(-time.Hour), "")}

	batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if !available {
		t.Fatal("expected a batch")
	}
	if len(batch.Entities) != incrementalBatchCap {
		t.Fatalf("entities = %d, want exactly %d (%d repos + the work item, all fitting in one row-capped page): %+v", len(batch.Entities), incrementalBatchCap, incrementalBatchCap-1, batch.Entities)
	}
	foundWorkItemEntity, foundRelationship := false, false
	for _, entity := range batch.Entities {
		if entity.Subject.CanonicalID == "work_item:WIDGET-1" {
			foundWorkItemEntity = true
		}
	}
	for _, relationship := range batch.Relationships {
		if relationship.From.CanonicalID == "work_item:WIDGET-1" && relationship.Type == "BELONGS_TO_REPOSITORY" {
			foundRelationship = true
		}
	}
	if !foundWorkItemEntity {
		t.Fatal("the work item's entity must be projected")
	}
	if !foundRelationship {
		t.Fatal("the work item's BELONGS_TO_REPOSITORY relationship must be projected alongside its entity -- a page boundary must never split them, silently losing the relationship forever")
	}
}

// TestClickHouseProjectionSourceFullSnapshotPagesToCompletionWhenOversized
// is CHAOS-3753 codex finding C6's regression test: an organization too
// large for one complete-enumeration batch must page to completion across
// ticks (each a bounded, ordinary FullSnapshot=false batch), not error
// forever on the same oversized single-batch attempt. Seeds 200 distinct
// repositories (each its own keyset-pagination identity, unlike the
// single-ID fixture the old error-path test used) against a
// snapshotPerQueryCap of 150 and drives NextProjectionBatch across
// multiple ticks until caught up.
func TestClickHouseProjectionSourceFullSnapshotPagesToCompletionWhenOversized(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const repoCount = 200
	const workItemCount = 200
	repoRows := make([][]any, 0, repoCount)
	for i := 0; i < repoCount; i++ {
		repoRows = append(repoRows, []any{fmt.Sprintf("repo-%03d", i), fmt.Sprintf("example-org/repo-%03d", i), "synthetic", at.Add(time.Duration(i) * time.Second), at})
	}
	// Interleaved with the single-candidate repos rows above (same
	// timestamp range) rather than segregated into their own time window:
	// a real oversized organization has multiple tables contributing at
	// once, and every page boundary should have a real chance of landing
	// on a multi-candidate row -- CONTRIVED-proof gap flagged in the codex
	// round-2 review: the previous fixture isolated this scenario to
	// repos alone (every other table's rows set to nil), which cannot
	// exercise K2's row-group-safe truncation (an entity plus its
	// BELONGS_TO_REPOSITORY relationship, two candidates sharing one row)
	// under multi-page paging at all.
	workItemRows := make([][]any, 0, workItemCount)
	for i := 0; i < workItemCount; i++ {
		id := fmt.Sprintf("WIDGET-%03d", i)
		workItemRows = append(workItemRows, []any{id, "repo-000", "example-org/repo-000", "task " + id, "in_progress", "", at.Add(time.Duration(i)*time.Second + 500*time.Millisecond), at, uint8(0), zeroTime})
	}
	tables := baseTables(at)
	for index, table := range tables {
		switch table.match {
		case "FROM repos":
			tables[index].rows = repoRows
		case "FROM work_items AS w":
			tables[index].rows = workItemRows
			tables[index].cursorOf = workItemCursorOf
		default:
			tables[index].rows = nil // isolate this scenario to the two oversized tables
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	seenRepos := map[string]bool{}
	seenWorkItemEntities := map[string]bool{}
	seenWorkItemRelationships := map[string]bool{}
	sawOrganization := false
	pages := 0
	for pages = 0; pages < repoCount+workItemCount; pages++ { // generous upper bound; must converge well before this
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("page %d: next projection batch: %v", pages, err)
		}
		if !available {
			break
		}
		if batch.FullSnapshot {
			t.Fatalf("page %d: a paged catch-up batch must not claim FullSnapshot", pages)
		}
		if err := batch.Validate(); err != nil {
			t.Fatalf("page %d: batch failed contract validation: %v", pages, err)
		}
		for _, entity := range batch.Entities {
			switch entity.Subject.Kind {
			case contextfabric.SubjectOrganization:
				if sawOrganization {
					t.Fatal("the organization entity must be projected exactly once across the whole catch-up, not once per page")
				}
				sawOrganization = true
			case contextfabric.SubjectWorkItem:
				if seenWorkItemEntities[entity.Subject.CanonicalID] {
					t.Fatalf("page %d: work item %s was projected twice -- pagination skipped or replayed a row", pages, entity.Subject.CanonicalID)
				}
				seenWorkItemEntities[entity.Subject.CanonicalID] = true
			default:
				if seenRepos[entity.Subject.CanonicalID] {
					t.Fatalf("page %d: repository %s was projected twice -- pagination skipped or replayed a row", pages, entity.Subject.CanonicalID)
				}
				seenRepos[entity.Subject.CanonicalID] = true
			}
		}
		for _, relationship := range batch.Relationships {
			if relationship.Type != "BELONGS_TO_REPOSITORY" || relationship.From.Kind != contextfabric.SubjectWorkItem {
				continue
			}
			if seenWorkItemRelationships[relationship.From.CanonicalID] {
				t.Fatalf("page %d: work item %s's relationship was projected twice", pages, relationship.From.CanonicalID)
			}
			// K2: this relationship must appear in the SAME batch as its
			// entity -- never split across a page boundary.
			if !seenWorkItemEntities[relationship.From.CanonicalID] {
				t.Fatalf("page %d: work item %s's relationship was projected without its entity in the same or an earlier batch", pages, relationship.From.CanonicalID)
			}
			seenWorkItemRelationships[relationship.From.CanonicalID] = true
		}
		checkpoint.Cursor = batch.NextCursor
	}
	if len(seenRepos) != repoCount {
		t.Fatalf("expected all %d repositories to be projected across %d pages, got %d: missing >= 1", repoCount, pages, len(seenRepos))
	}
	if len(seenWorkItemEntities) != workItemCount || len(seenWorkItemRelationships) != workItemCount {
		t.Fatalf("expected all %d work items and their relationships to be projected across %d pages, got %d entities / %d relationships",
			workItemCount, pages, len(seenWorkItemEntities), len(seenWorkItemRelationships))
	}
	if !sawOrganization {
		t.Fatal("expected the organization entity to be projected exactly once during catch-up")
	}
	if pages < 2 {
		t.Fatalf("expected catch-up to take more than one page given the oversized tables, took %d", pages)
	}
}

// workItemCursorOf matches queryWorkItems' sincePredicate/orderBy pair
// (tables.go: sincePredicate(cursor, "w.updated_at", "w.work_item_id")):
// column 6 is w.updated_at, column 0 is w.work_item_id. This is
// deliberately a JOINED table (work_items INNER JOIN repos AS r) -- the
// exact shape codex finding C5 broke, since "repos AS r" has its own "id"
// column that the old hardcoded bare-"id" tiebreaker silently resolved to
// instead of the work item's own identifier. A test against the repos
// table alone would NOT catch that regression, because repos was the one
// table the old bug happened to get right.
func workItemCursorOf(row []any) (time.Time, string) { return row[6].(time.Time), row[0].(string) }

// TestClickHouseProjectionSourceKeysetPaginationSurvivesTiedTimestamps is
// CHAOS-3753 codex finding C5's regression test: bulk syncs commonly land
// many rows with the identical updated_at value, and the previous
// hardcoded-"id" tiebreaker resolved (via the "INNER JOIN repos AS r"
// present in this and five other table queries) to repos.id -- an entirely
// different row's identity -- rather than the work item's own id, silently
// skipping or replaying rows whenever a page boundary fell inside a group
// of tied timestamps. This seeds 300 work items sharing one exact
// timestamp -- forcing every page boundary through a tie -- and asserts
// every row is projected exactly once, in strict ascending id order.
func TestClickHouseProjectionSourceKeysetPaginationSurvivesTiedTimestamps(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	const workItemCount = 300
	rows := make([][]any, 0, workItemCount)
	for i := 0; i < workItemCount; i++ {
		id := fmt.Sprintf("WIDGET-%03d", i)
		rows = append(rows, []any{id, "repo-1", "example-org/widget-service", "task " + id, "in_progress", "", at, at, uint8(0), zeroTime})
	}
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[index].rows = rows
			tables[index].cursorOf = workItemCursorOf
		} else {
			tables[index].rows = nil
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	var order []string
	seen := map[string]bool{}
	for page := 0; page < workItemCount; page++ {
		batch, available, err := source.NextProjectionBatch(context.Background(), checkpoint)
		if err != nil {
			t.Fatalf("page %d: next projection batch: %v", page, err)
		}
		if !available {
			break
		}
		for _, entity := range batch.Entities {
			if entity.Subject.Kind == contextfabric.SubjectOrganization {
				continue
			}
			id := entity.Subject.CanonicalID
			if seen[id] {
				t.Fatalf("page %d: work item %s was projected twice under a tied timestamp -- tiebreaker skipped or replayed", page, id)
			}
			seen[id] = true
			order = append(order, id)
		}
		checkpoint.Cursor = batch.NextCursor
	}
	if len(seen) != workItemCount {
		t.Fatalf("expected all %d tied-timestamp work items to be projected, got %d", workItemCount, len(seen))
	}
	if !sort.StringsAreSorted(order) {
		t.Fatalf("expected strict ascending id order across pages under tied timestamps, got: %v", order)
	}
}

func TestClickHouseProjectionSourceIncrementalNoNewRowsReturnsUnavailable(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: baseTables(at)})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	snapshot, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		t.Fatalf("snapshot batch: %v", err)
	}
	// Same cursor, now against an empty backend (nothing new since the
	// snapshot): no batch should be available.
	empty, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	batch, available, err := empty.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: snapshot.NextCursor})
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	if available {
		t.Fatalf("expected no batch to be available, got: %+v", batch)
	}
}

func TestClickHouseProjectionSourceReplayIsDeterministic(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: baseTables(at)}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	first, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	second, _, err := source.NextProjectionBatch(context.Background(), checkpoint)
	if err != nil {
		t.Fatalf("replayed batch: %v", err)
	}
	if first.BatchID != second.BatchID {
		t.Fatalf("replay must be idempotent: batch IDs %q != %q", first.BatchID, second.BatchID)
	}
}

func TestClickHouseProjectionSourceRejectsEmptyOrganization(t *testing.T) {
	t.Parallel()
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{}); err == nil {
		t.Fatal("expected an error for a missing organization")
	}
}

func TestClickHouseProjectionSourceWrapsQueryFailureAsUnavailable(t *testing.T) {
	t.Parallel()
	tables := baseTables(time.Now())
	tables[0] = fakeTable{match: "FROM repos", err: errors.New("connection reset")}
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	_, _, err = source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if !errors.Is(err, contextfabric.ErrUnavailable) {
		t.Fatalf("expected a retryable ErrUnavailable, got: %v", err)
	}
}
