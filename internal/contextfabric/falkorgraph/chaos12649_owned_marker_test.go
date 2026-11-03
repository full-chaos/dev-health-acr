package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// CHAOS-12649: the owned writer marks the node it writes; a stub a relationship
// creates never carries the marker. The first batch of a walk from zero marks the
// source's watermark, and later batches keep it. CountKind counts marked nodes.

func TestTheOwnedWriterMarksItsNodeAndAStubNeverDoes(t *testing.T) {
	ctx := context.Background()
	team := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team:T", Label: "T"}
	adapter, writes := capturingAdapter(t)
	if err := adapter.projectRelationship(ctx, "key", "org-1", ownershipEdge(team, "acme/r")); err != nil {
		t.Fatal(err)
	}
	edge := (*writes)[len(*writes)-1]
	for _, side := range []string{"fromAttrs", "toAttrs"} {
		if _, ok := attrsOf(t, edge, side)[propProjectionOwned]; ok {
			t.Fatalf("the stub on the %s side carries %s: a referenced node would count as projected", side, propProjectionOwned)
		}
	}
	entity := contextfabric.EntityProjection{
		Subject: team, Aliases: []string{}, PreviousNames: []string{}, ProviderIDs: map[string]string{},
		Authorization:  contextfabric.AuthorizationScope{TeamIDs: []string{"T"}},
		EvidenceRefIDs: []string{"evidence_team_t"}, ObservedAt: time.Date(2026, 9, 30, 12, 1, 0, 0, time.UTC), SourceVersion: "v1",
	}
	if err := adapter.projectEntity(ctx, "key", "org-1", entity); err != nil {
		t.Fatal(err)
	}
	if owned, _ := attrsOf(t, (*writes)[len(*writes)-1], "attrs")[propProjectionOwned].(bool); !owned {
		t.Fatalf("the owned write does not set %s = true", propProjectionOwned)
	}
}

func TestTheFirstBatchOfAWalkFromZeroMarksTheWatermark(t *testing.T) {
	ctx := context.Background()
	for cursor, want := range map[string]bool{"": true, "c1": false} {
		adapter, writes := capturingAdapter(t)
		batch := contextfabric.ProjectionBatch{OrgID: "org-1", Source: "source-a", SourceVersion: "v1", Cursor: cursor, NextCursor: "c2"}
		if err := adapter.writeWatermark(ctx, "key", batch, "falkordb:w"); err != nil {
			t.Fatal(err)
		}
		w := (*writes)[len(*writes)-1]
		if !strings.Contains(w.cypher, labelWatermark) {
			t.Fatalf("the captured write is not the watermark write: %s", w.cypher)
		}
		marked, present := attrsOf(t, w, "attrs")[watermarkOwnedMarkedWalk].(bool)
		if present != want || (present && !marked) {
			t.Fatalf("batch from cursor %q: %s present=%v value=%v, want present=%v", cursor, watermarkOwnedMarkedWalk, present, marked, want)
		}
		// `w += $attrs` keeps the key a later batch omits.
		if !strings.Contains(w.cypher, "w += $attrs") {
			t.Fatalf("the watermark write no longer merges its attributes, so a later batch would drop the marker: %s", w.cypher)
		}
	}
}

func TestCountKindCountsOnlyMarkedNodes(t *testing.T) {
	var captured []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
		captured = append(captured, q)
		return []row{{"total": int64(3)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if _, err := adapter.CountKind(context.Background(), "org-1", contextfabric.SubjectPullRequest); err != nil {
		t.Fatal(err)
	}
	if len(captured) != 1 || !strings.Contains(captured[0], "n."+propProjectionOwned+" = true") {
		t.Fatalf("CountKind does not count marked nodes only: %v", captured)
	}
}

func TestOwnedCountsKnownReadsTheWatermarkMarker(t *testing.T) {
	for total, want := range map[int64]bool{1: true, 0: false} {
		var captured []string
		fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, _ map[string]interface{}, _ bool) ([]row, error) {
			captured = append(captured, q)
			return []row{{"total": total}}, nil
		}}
		adapter := newFakeAdapter(t, fake)
		known, err := adapter.OwnedCountsKnown(context.Background(), "org-1", "source-a")
		if err != nil || known != want {
			t.Fatalf("OwnedCountsKnown with %d marked watermarks = %v, %v; want %v", total, known, err, want)
		}
		if len(captured) != 1 || !strings.Contains(captured[0], labelWatermark) || !strings.Contains(captured[0], "w."+watermarkOwnedMarkedWalk+" = true") {
			t.Fatalf("OwnedCountsKnown does not read the watermark marker: %v", captured)
		}
	}
}

func TestCountKindByDayCountsMarkedStampedNodesPerDay(t *testing.T) {
	var captured []string
	var params map[string]interface{}
	fake := &fakeConn{queryFunc: func(_ context.Context, _, q string, p map[string]interface{}, _ bool) ([]row, error) {
		captured, params = append(captured, q), p
		return []row{{"day": int64(20387), "total": int64(997)}, {"day": float64(20388), "total": int64(3)}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	days, err := adapter.CountKindByDay(context.Background(), "org-1", contextfabric.SubjectKind(contractsv1.ContextFabricSubjectCIRun))
	if err != nil || days[20387] != 997 || days[20388] != 3 || len(days) != 2 {
		t.Fatalf("CountKindByDay = %v, %v; want {20387: 997, 20388: 3}", days, err)
	}
	stamp := "n." + propPropertyPrefix + safeName(contextfabric.ProjectionSyncedAtProperty)
	if len(captured) != 1 || !strings.Contains(captured[0], "n."+propProjectionOwned+" = true") || !strings.Contains(captured[0], stamp+" IS NOT NULL") || !strings.Contains(captured[0], "toInteger("+stamp+" / $day)") {
		t.Fatalf("CountKindByDay does not count owned, stamped nodes per day of the stamp: %v", captured)
	}
	if params["day"] != nanosPerDay {
		t.Fatalf("day bucket = %v, want %d nanoseconds", params["day"], nanosPerDay)
	}
}
