package falkorgraph

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func columnEdgeBatch(org, id string, observed time.Time, rels []contextfabric.RelationshipProjection, tombs []contextfabric.ProjectionTombstone) contextfabric.ProjectionBatch {
	return contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: id, OrgID: org, Source: "live-test",
		SourceVersion: "v1", Cursor: "", NextCursor: id, GeneratedAt: observed,
		Entities: []contextfabric.EntityProjection{}, Relationships: rels, Contents: []contextfabric.ContentProjection{},
		Episodes: []contextfabric.EpisodeProjection{}, Tombstones: tombs,
	}
}

func columnEdge(rid string, from, to contextfabric.SubjectRef, observed time.Time, validFrom *time.Time) contextfabric.RelationshipProjection {
	return contextfabric.RelationshipProjection{
		RelationshipID: rid, Type: contractsv1.ContextFabricRelationshipBelongsToProject, From: from, To: to,
		Derivation: contractsv1.ContextFabricDerivationCanonicalStructured, EpistemicStatus: contractsv1.ContextFabricEpistemicObserved,
		Authorization: contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/a"}}, EvidenceRefIDs: []string{"evidence_column"},
		ObservedAt: observed, SourceVersion: "v1", ValidFrom: validFrom,
	}
}

func openEdgeIDs(t *testing.T, ctx context.Context, a *Adapter, key, org string, from contextfabric.SubjectRef) []string {
	t.Helper()
	rows, err := a.api.query(ctx, key, fmt.Sprintf("MATCH (a:%s {%s:$org, %s:$kind, %s:$id})-[r:%s]->() WHERE r.%s IS NULL RETURN r.%s AS rid",
		labelSubject, propOrgID, propKind, propCanonicalID, labelRelation, propValidFromNs, propRelationshipID),
		map[string]interface{}{"org": org, "kind": string(from.Kind), "id": from.CanonicalID}, true)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprint(r["rid"]))
	}
	sort.Strings(out)
	return out
}

func TestLiveColumnProjectEdgesKeepOneOpenEdgePerWorkItem(t *testing.T) {
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	org := "live-colproj-" + time.Now().UTC().Format("20060102T150405.000000000")
	key := graphKey(adapter.config.GraphPrefix, org)
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), org) })

	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	wi := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_item:w1", Label: "w1"}
	pr := contextfabric.SubjectRef{Kind: contextfabric.SubjectPullRequest, CanonicalID: "pull_request:r:1", Label: "PR #1"}
	projA := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:A", Label: "A"}
	projB := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project:B", Label: "B"}
	from := t0.Add(-time.Hour)
	apply := func(id string, observed time.Time, rels []contextfabric.RelationshipProjection, tombs []contextfabric.ProjectionTombstone) {
		t.Helper()
		if _, err := adapter.ApplyProjectionBatch(ctx, columnEdgeBatch(org, id, observed, rels, tombs)); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	retract := func(observed time.Time) contextfabric.ProjectionTombstone {
		return contextfabric.ProjectionTombstone{Kind: contextfabric.TombstoneKindColumnProjectEdges, CanonicalID: wi.CanonicalID, Reason: "column_project_changed", EffectiveAt: observed, SourceVersion: "v1"}
	}

	apply("batch-0001", t0, []contextfabric.RelationshipProjection{
		columnEdge("rel-w1-A", wi, projA, t0, nil),
		columnEdge("rel-w1-A-interval", wi, projA, t0, &from),
		columnEdge("rel-pr-A", pr, projA, t0, nil),
		columnEdge("rel-pr-B", pr, projB, t0, nil),
	}, []contextfabric.ProjectionTombstone{})
	if got := openEdgeIDs(t, ctx, adapter, key, org, wi); fmt.Sprint(got) != "[rel-w1-A]" {
		t.Fatalf("seed: open edges %v", got)
	}

	t1 := t0.Add(time.Minute)
	apply("batch-0002", t1, []contextfabric.RelationshipProjection{columnEdge("rel-w1-B", wi, projB, t1, nil), columnEdge("rel-pr-A", pr, projA, t1, nil)}, []contextfabric.ProjectionTombstone{})
	if got := openEdgeIDs(t, ctx, adapter, key, org, wi); fmt.Sprint(got) != "[rel-w1-B]" {
		t.Fatalf("column project changed A to B: open edges %v, want only rel-w1-B", got)
	}
	var interval int
	rows, err := adapter.api.query(ctx, key, "MATCH ()-[r:Relates {relationship_id:'rel-w1-A-interval'}]->() RETURN count(r) AS n", nil, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("interval read: %v", err)
	}
	interval = int(rows[0]["n"].(int64))
	if interval != 1 {
		t.Fatalf("the transition-interval edge was deleted")
	}
	if got := openEdgeIDs(t, ctx, adapter, key, org, pr); fmt.Sprint(got) != "[rel-pr-A rel-pr-B]" {
		t.Fatalf("a pull request may belong to several projects: open edges %v", got)
	}

	// The asserting row is the current truth: an edge to A stamped later than the current B row still goes.
	apply("batch-0003", t1, []contextfabric.RelationshipProjection{columnEdge("rel-w1-A", wi, projA, t1.Add(time.Hour), nil)}, []contextfabric.ProjectionTombstone{})
	apply("batch-0004", t1, []contextfabric.RelationshipProjection{columnEdge("rel-w1-B", wi, projB, t1, nil)}, []contextfabric.ProjectionTombstone{})
	if got := openEdgeIDs(t, ctx, adapter, key, org, wi); fmt.Sprint(got) != "[rel-w1-B]" {
		t.Fatalf("an edge to A stamped after the current B row leaves %v open, want only rel-w1-B", got)
	}

	t2 := t1.Add(2 * time.Hour)
	apply("batch-0005", t2, []contextfabric.RelationshipProjection{}, []contextfabric.ProjectionTombstone{retract(t2)})
	if got := openEdgeIDs(t, ctx, adapter, key, org, wi); len(got) != 0 {
		t.Fatalf("column project unresolved or superseded: open edges %v, want none", got)
	}
	if got := openEdgeIDs(t, ctx, adapter, key, org, pr); len(got) != 2 {
		t.Fatalf("a work item retraction touched a pull request: %v", got)
	}
}
