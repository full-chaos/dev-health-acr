package devhealthsource_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/devhealthsource"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/identity"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/relationcurrent"
	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// The producer reads an ended blocks link as a row flagged ended (the shared
// relation-current rule runs in the statement). It must retract the edge under
// the ids the asserting branch derives, in both spellings of the target, and
// assert nothing for it.
func TestEndedBlockerLinkRowIsRetractedNotAsserted(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	endedAt := at.Add(2 * time.Hour)
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[index].rows = [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at,
				created, uint8(0), zeroTime, uint8(1), created, uint8(0), zeroTime, "repo-1", uint8(1), endedAt, endedAt}}
			continue
		}
		tables[index].rows = nil
	}
	batch := projectOneBatch(t, tables)

	resolved := workItemDependencyRelationshipID(t, "repo-1", "WIDGET-101", "repo-1", "WIDGET-099", "blocks")
	for _, relationship := range batch.Relationships {
		if relationship.RelationshipID == resolved {
			t.Fatalf("an ended link was asserted as an edge: %+v", relationship)
		}
	}
	source, _, err := identity.Derive(identity.KindWorkItem, []string{"repo-1", "WIDGET-101"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	refID, refOmitted := identity.DeriveWorkItemRef("WIDGET-099", nil)
	if refOmitted {
		t.Fatal("ref id omitted")
	}
	refEdge := identity.DeriveRelationship(identity.RelationshipFamilyWorkItemDependency, source, refID, "blocks")

	seen := map[string]string{}
	for _, tomb := range batch.Tombstones {
		seen[tomb.CanonicalID] = tomb.Kind
		if !tomb.EffectiveAt.Equal(endedAt) {
			t.Fatalf("tombstone %q effective at %v, want the time the link ended %v", tomb.CanonicalID, tomb.EffectiveAt, endedAt)
		}
	}
	for id, kind := range map[string]string{resolved: "relationship", refEdge: "relationship", refID: "work_item_ref"} {
		if seen[id] != kind {
			t.Fatalf("no %s tombstone for %q; tombstones: %v", kind, id, seen)
		}
	}
}

// A removed-link set over the bound is cut, and the cut is disclosed with the
// total and the count kept, never dropped silently.
func TestRemovedLinkSetOverTheBoundIsDisclosed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	tables := baseTables(at)
	for index, table := range tables {
		if table.match == "FROM work_item_dependencies AS d" {
			tables[index].rows = [][]any{{"WIDGET-101", "WIDGET-099", "blocks", "repo-1", "example-org/widget-service", at,
				at, uint8(0), zeroTime, uint8(1), at, uint8(0), zeroTime, "repo-1", uint8(0), zeroTime, at}}
			continue
		}
		tables[index].rows = nil
	}
	tables = append([]fakeTable{{match: endedLinksMarker, rows: [][]any{{[]string{"AA.BB.CC.1"}, uint64(relationcurrent.MaxEndedLinks + 5), uint64(3)}}}}, tables...)
	var logged bytes.Buffer
	source, err := devhealthsource.NewClickHouseProjectionSource(&fakeClient{tables: tables})
	if err != nil {
		t.Fatal(err)
	}
	source.WithLogger(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatal(err)
	}
	line := logged.String()
	want := fmt.Sprintf("removed_links=%d", relationcurrent.MaxEndedLinks+5)
	if !strings.Contains(line, "removed-link set cut") || !strings.Contains(line, want) || !strings.Contains(line, "retracted_links=1") || !strings.Contains(line, "oldest removed links") || !strings.Contains(line, "left_out_links=3") {
		t.Fatalf("the cut was not disclosed with its total and kept count (%s): %q", want, line)
	}
}

type endedLinksStatementRecorder struct {
	inner      *fakeClient
	statements []string
}

func (r *endedLinksStatementRecorder) Query(ctx context.Context, statement string, bindings []contextpacket.ClickHouseBinding) (contextpacket.ClickHouseRowScanner, error) {
	if strings.Contains(statement, endedLinksMarker) {
		r.statements = append(r.statements, statement)
	}
	return r.inner.Query(ctx, statement, bindings)
}

// A source built without a test override reads the removed-link set at the
// production bound, so the lowered bound of the over-bound row cannot hide a
// wrong production default.
func TestRemovedLinkSetReadUsesTheProductionBoundByDefault(t *testing.T) {
	t.Parallel()
	if relationcurrent.MaxEndedLinks != 20000 {
		t.Fatalf("MaxEndedLinks = %d, want 20000 (about 87 times the largest organization's removed links)", relationcurrent.MaxEndedLinks)
	}
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	recorder := &endedLinksStatementRecorder{inner: &fakeClient{tables: baseTables(at)}}
	source, err := devhealthsource.NewClickHouseProjectionSource(recorder)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.NextProjectionBatch(context.Background(), contextfabric.ProjectionCheckpoint{OrgID: "org-1", Source: devhealthsource.SourceName}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.statements) != 1 {
		t.Fatalf("%d removed-link set reads in one call, want exactly 1", len(recorder.statements))
	}
	if want := relationcurrent.EndedLinksSQL(20000); recorder.statements[0] != want {
		t.Fatalf("the removed-link set was read with a bound other than 20000:\n%s", recorder.statements[0])
	}
}
