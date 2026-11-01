package devhealthsource_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12226: with work-item project membership on, every work-item entity
// states its CURRENT project set from its own project link
// (project_membership_presence, resolved to exactly one project row); off,
// it states nothing.

const (
	chaos12226Linear = "00000000-0000-0000-0000-000000000000"
	chaos12226Orphan = "99999999-9999-9999-9999-999999999999"
)

func chaos12226Tables(at time.Time, memberships [][]any, membershipErr error) []fakeTable {
	tables := baseTables(at)
	for i, table := range tables {
		if table.match == "FROM work_items AS w" {
			tables[i] = fakeTable{match: table.match, rows: [][]any{
				{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime, "bug", "CHAOS", "Widget Reliability", []string{"checkout", "flake"}},
				{"WIDGET-LINEAR", chaos12226Linear, "", "Linear-sourced item", "open", "", at, at, uint8(0), zeroTime, "", "", "", []string{}},
				{"WIDGET-ORPHAN", chaos12226Orphan, "", "Orphaned repo_id", "open", "", at, at, uint8(0), zeroTime, "", "", "", []string{}},
			}}
		}
	}
	return append(tables, fakeTable{match: workItemMembershipMarker, rows: memberships, err: membershipErr})
}

// membershipRow is one row of the page membership read: (repo_id,
// work_item_id, provider, resolved project id, key_resolution_count).
func membershipRow(repoID, workItemID, provider, resolvedProjectID string, keyResolutionCount uint64) []any {
	return []any{repoID, workItemID, provider, resolvedProjectID, keyResolutionCount}
}

func chaos12226Batch(t *testing.T, client *fakeClient, membership bool) (contextfabric.ProjectionBatch, error) {
	t.Helper()
	source, err := devhealthsource.NewClickHouseProjectionSource(client)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(membership)
	batch, available, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil {
		return contextfabric.ProjectionBatch{}, err
	}
	if !available {
		t.Fatal("expected a batch to be available")
	}
	if err := batch.Validate(); err != nil {
		t.Fatalf("batch failed contract validation: %v", err)
	}
	return batch, nil
}

func chaos12226WorkItems(batch contextfabric.ProjectionBatch) map[string]contextfabric.EntityProjection {
	out := map[string]contextfabric.EntityProjection{}
	for _, entity := range batch.Entities {
		if entity.Subject.Kind == contractsv1.ContextFabricSubjectWorkItem {
			out[entity.Subject.CanonicalID] = entity
		}
	}
	return out
}

func TestAWorkItemStatesItsCurrentProjectMembership(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: chaos12226Tables(at, [][]any{
		membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1),
		membershipRow("repo-1", "WIDGET-101", "jira", "ENG", 1),
		// The same project twice (a column row and a transition row of one
		// project) is one membership.
		membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1),
		// Ambiguous (two projects share the key) and unresolved (no project
		// row): neither names a project.
		membershipRow(chaos12226Linear, "WIDGET-LINEAR", "linear", "X", 2),
		membershipRow(chaos12226Linear, "WIDGET-LINEAR", "linear", "", 0),
		// A row that claims one resolution but names no project row id
		// names no project either.
		membershipRow(chaos12226Orphan, "WIDGET-ORPHAN", "github", "", 1),
		// A row for a work item NOT on this page is never attached to it.
		membershipRow("repo-1", "WIDGET-OFF-PAGE", "github", "P-9", 1),
	}, nil)}
	batch, err := chaos12226Batch(t, client, true)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	want := map[string][]string{
		"work_item.v2:repo-1:WIDGET-101":                      {"project.v2:github:P-1", "project.v2:jira:ENG"},
		"work_item.v2:" + chaos12226Linear + ":WIDGET-LINEAR": {},
		"work_item.v2:" + chaos12226Orphan + ":WIDGET-ORPHAN": {},
	}
	items := chaos12226WorkItems(batch)
	for id, wantSet := range want {
		entity, found := items[id]
		if !found {
			t.Fatalf("work item %s not projected", id)
		}
		if entity.ProjectIdentities == nil {
			t.Fatalf("%s: ProjectIdentities = nil, want the stated set %v", id, wantSet)
		}
		if !reflect.DeepEqual(*entity.ProjectIdentities, wantSet) {
			t.Fatalf("%s: ProjectIdentities = %v, want %v", id, *entity.ProjectIdentities, wantSet)
		}
	}
	// Repository authorization is unchanged.
	if got := items["work_item.v2:repo-1:WIDGET-101"].Authorization.RepositorySlugs; !reflect.DeepEqual(got, []string{"example-org/widget-service"}) {
		t.Fatalf("repository authorization = %v, want it unchanged", got)
	}
	if client.workItemMembershipQueries != 1 {
		t.Fatalf("membership reads = %d, want 1 for the one page", client.workItemMembershipQueries)
	}
}

func TestWithMembershipOffAWorkItemStatesNothing(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: chaos12226Tables(at, [][]any{membershipRow("repo-1", "WIDGET-101", "github", "P-1", 1)}, nil)}
	batch, err := chaos12226Batch(t, client, false)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	for id, entity := range chaos12226WorkItems(batch) {
		if entity.ProjectIdentities != nil {
			t.Fatalf("%s: ProjectIdentities = %v, want nil with membership off", id, *entity.ProjectIdentities)
		}
	}
	if client.workItemMembershipQueries != 0 {
		t.Fatalf("membership reads = %d, want none with membership off", client.workItemMembershipQueries)
	}
}

// A failed membership read fails the page: no work item is written with a
// partial or missing set stated as current.
func TestAFailedMembershipReadFailsThePage(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	boom := errors.New("membership read failed")
	client := &fakeClient{tables: chaos12226Tables(at, nil, boom)}
	// Errors leave the source as bounded classifications (no raw text): the
	// page's table read is what failed.
	if _, err := chaos12226Batch(t, client, true); err == nil || !strings.Contains(err.Error(), "read work_items") {
		t.Fatalf("next projection batch error = %v, want the work_items page read to fail", err)
	}
}

// The refresh table re-emits a work item whose membership changed without
// the work item itself syncing, with its full current set: here the work item
// LEFT its last project, so the set is stated empty.
func TestTheRefreshTableReemitsAWorkItemWhoseMembershipChanged(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	changed := at.Add(time.Hour)
	tables := chaos12226Tables(at, nil, nil)
	for i, table := range tables {
		if table.match == "FROM work_items AS w" {
			// The work item itself did not sync again: the work-item table
			// holds nothing for it.
			tables[i].rows = nil
		}
	}
	tables = append(tables, fakeTable{match: workItemMembershipRefreshMarker, rows: [][]any{
		{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime, "bug", "CHAOS", "Widget Reliability", []string{"checkout", "flake"}, changed},
	}})
	client := &fakeClient{tables: tables}
	batch, err := chaos12226Batch(t, client, true)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	entity, found := chaos12226WorkItems(batch)["work_item.v2:repo-1:WIDGET-101"]
	if !found {
		t.Fatal("the refresh table did not re-emit the work item whose membership changed")
	}
	if entity.ProjectIdentities == nil || len(*entity.ProjectIdentities) != 0 {
		t.Fatalf("ProjectIdentities = %v, want the stated empty set after the last project was left", entity.ProjectIdentities)
	}
}

// With membership off the refresh table does not exist: nothing reads it.
func TestWithMembershipOffTheRefreshTableIsNotRead(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := chaos12226Tables(at, nil, nil)
	tables = append(tables, fakeTable{match: workItemMembershipRefreshMarker, err: errors.New("the refresh table must not be read with membership off")})
	if _, err := chaos12226Batch(t, &fakeClient{tables: tables}, false); err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
}

// Even when the read returns a row for a work item the statement did not
// name, the producer never attaches it: the set is the named work item's own.
func TestAMembershipRowForAnotherWorkItemIsNeverAttached(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := chaos12226Tables(at, nil, nil)
	tables[len(tables)-1] = fakeTable{match: workItemMembershipMarker, unfiltered: true, rows: [][]any{
		membershipRow("repo-1", "WIDGET-OFF-PAGE", "github", "P-9", 1),
	}}
	batch, err := chaos12226Batch(t, &fakeClient{tables: tables}, true)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	for id, entity := range chaos12226WorkItems(batch) {
		if entity.ProjectIdentities == nil || len(*entity.ProjectIdentities) != 0 {
			t.Fatalf("%s: ProjectIdentities = %v, want the stated empty set: the only row named another work item", id, entity.ProjectIdentities)
		}
	}
}

// Codex r2: EncodeSegment does not escape '|', and the contract refuses '|'
// in every authorization value. A project whose provider or id holds one is
// left out (the work item states the set without it), so the page's batch
// stays valid instead of failing every work item on it.
func TestAProjectWhoseIdentityHoldsASeparatorIsLeftOut(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	client := &fakeClient{tables: chaos12226Tables(at, [][]any{
		membershipRow("repo-1", "WIDGET-101", "gh|org", "P-1", 1),
		membershipRow("repo-1", "WIDGET-101", "github", "P|2", 1),
		membershipRow("repo-1", "WIDGET-101", "github", "P-3", 1),
	}, nil)}
	batch, err := chaos12226Batch(t, client, true)
	if err != nil {
		t.Fatalf("next projection batch: %v", err)
	}
	entity, found := chaos12226WorkItems(batch)["work_item.v2:repo-1:WIDGET-101"]
	if !found || entity.ProjectIdentities == nil {
		t.Fatalf("WIDGET-101 not projected with a stated set: %+v", entity)
	}
	if !reflect.DeepEqual(*entity.ProjectIdentities, []string{"project.v2:github:P-3"}) {
		t.Fatalf("ProjectIdentities = %v, want only [project.v2:github:P-3]", *entity.ProjectIdentities)
	}
}

// CHAOS-12410: a refresh is logged by the batch that commits it. A peek commits
// nothing, so it logs nothing, and the call after it does.
func TestAPeekLogsNoRefreshAndTheCommittingCallDoes(t *testing.T) {
	t.Parallel()
	const line = "context fabric: membership refresh of every subject owed"
	at := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tables := append(chaos12226Tables(at, nil, nil), fakeTable{match: workItemMembershipRefreshMarker, rows: [][]any{
		{"WIDGET-101", "repo-1", "example-org/widget-service", "Investigate checkout flake", "in_progress", "", at, at, uint8(0), zeroTime, "bug", "CHAOS", "Widget Reliability", []string{"checkout", "flake"}, at.Add(time.Hour)},
	}})
	var logged bytes.Buffer
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatalf("new source: %v", err)
	}
	source.WithWorkItemProjectMembership(true)
	source.WithLogger(slog.New(slog.NewJSONHandler(&logged, nil)))
	ctx := context.Background()
	first, available, err := source.NextProjectionBatch(ctx, contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName})
	if err != nil || !available {
		t.Fatalf("first batch: %v, %v", available, err)
	}
	if strings.Contains(logged.String(), line) {
		t.Fatalf("a from-zero read owes no refresh: %s", logged.String())
	}
	checkpoint := contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName, Cursor: chaos12410WithField(t, first.NextCursor, "")}
	peeked, err := source.PeekProjectionBatch(ctx, checkpoint)
	if err != nil || !peeked {
		t.Fatalf("premise: the peek found a batch: %v, %v", peeked, err)
	}
	if strings.Contains(logged.String(), line) {
		t.Fatalf("a peek logged a refresh it did not commit: %s", logged.String())
	}
	if _, available, err := source.NextProjectionBatch(ctx, checkpoint); err != nil || !available {
		t.Fatalf("second batch: %v, %v", available, err)
	}
	if !strings.Contains(logged.String(), line) {
		t.Fatalf("the committing call logged no refresh: %s", logged.String())
	}
}
