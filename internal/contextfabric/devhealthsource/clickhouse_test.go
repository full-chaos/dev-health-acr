package devhealthsource_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

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

func repoRow(id, slug, provider string, at time.Time) fakeTable {
	return fakeTable{match: "FROM repos", rows: [][]any{{id, slug, provider, at}}, cursorOf: repoCursorOf}
}

func baseTables(at time.Time) []fakeTable {
	return []fakeTable{
		repoRow("repo-1", "example-org/widget-service", "synthetic", at),
		{match: "FROM work_items AS w", rows: [][]any{{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at}}},
		{match: "FROM git_pull_requests AS p", rows: [][]any{{"repo-1", "example-org/widget-service", int64(1042), "Typed session tokens", "open", at}}},
		{match: "FROM deployments AS d", rows: [][]any{{"repo-1", "example-org/widget-service", "deploy-1", "success", "production", at}}},
		{match: "FROM operational_incidents AS i", rows: [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(0)}}},
		{match: "FROM work_item_dependencies AS d", rows: [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "example-org/widget-service", at}}},
		{match: "FROM work_graph_deployment_incident_edges AS e", rows: [][]any{{"edge-1", "deploy-1", "incident-1", "example-org/widget-service", at}}},
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
			tables[index].rows = [][]any{{"incident-1", "repo-1", "example-org/widget-service", "Widget incident", "open", "low", at, uint8(1)}}
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
	rows := make([][]any, 0, repoCount)
	for i := 0; i < repoCount; i++ {
		rows = append(rows, []any{fmt.Sprintf("repo-%03d", i), fmt.Sprintf("example-org/repo-%03d", i), "synthetic", at.Add(time.Duration(i) * time.Second)})
	}
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM repos" {
			tables[index].rows = rows
		} else {
			tables[index].rows = nil // isolate this scenario to one oversized table
		}
	}
	client := &fakeClient{tables: tables}
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}
	seenRepos := map[string]bool{}
	sawOrganization := false
	pages := 0
	for pages = 0; pages < repoCount; pages++ { // generous upper bound; must converge well before this
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
			if entity.Subject.Kind == contextfabric.SubjectOrganization {
				if sawOrganization {
					t.Fatal("the organization entity must be projected exactly once across the whole catch-up, not once per page")
				}
				sawOrganization = true
				continue
			}
			if seenRepos[entity.Subject.CanonicalID] {
				t.Fatalf("page %d: repository %s was projected twice -- pagination skipped or replayed a row", pages, entity.Subject.CanonicalID)
			}
			seenRepos[entity.Subject.CanonicalID] = true
		}
		checkpoint.Cursor = batch.NextCursor
	}
	if len(seenRepos) != repoCount {
		t.Fatalf("expected all %d repositories to be projected across %d pages, got %d: missing >= 1", repoCount, pages, len(seenRepos))
	}
	if !sawOrganization {
		t.Fatal("expected the organization entity to be projected exactly once during catch-up")
	}
	if pages < 2 {
		t.Fatalf("expected catch-up to take more than one page given the oversized table, took %d", pages)
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
		rows = append(rows, []any{id, "repo-1", "example-org/widget-service", "task " + id, "in_progress", "", at})
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
