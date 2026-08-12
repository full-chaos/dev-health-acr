package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// This file holds the delta-review (round 2) probe tests for the two REAL
// P2s that survived round 1's fixes, plus the P2e edge note. See
// codex_round1_fake_test.go's doc comment for why a fake conn is the right
// tool for these specific properties (exact call order/count, an injected
// failure at one specific call site).

// TestBootstrapSchemaRejectsEmptyConstraintsResultSet is the Codex P2e
// round-2 edge note: bootstrapSchema just created two constraints
// immediately before polling for their status, so a report of ZERO
// constraints for this key is itself a symptom something is wrong, not a
// legitimate "nothing to check" state -- the round-1 allowlist handled an
// unknown/empty STATUS STRING but an empty ROW SLICE still passed vacuously
// (the `for _, status := range statuses` loop simply never ran, leaving
// allOperational at its initial true).
func TestBootstrapSchemaRejectsEmptyConstraintsResultSet(t *testing.T) {
	// fakeConn's default constraints() (no constraintsFunc override) already
	// returns a zero-length slice -- exactly the case under test.
	adapter := newFakeAdapter(t, &fakeConn{})
	err := adapter.bootstrapSchema(context.Background(), "test-key")
	if !errors.Is(err, errConstraintBootstrapFailed) {
		t.Fatalf("bootstrapSchema() with zero constraint rows = %v, want errConstraintBootstrapFailed", err)
	}
}

// TestDiscoverContextReportsPartialOnHopWalkNeighborBookkeepingLookupFailure
// is the Codex P2c round-2 probe. resolveEdge's OWN endpoint fetch for
// "work_target" succeeds (so the edge is legitimately admitted -- this is
// NOT the round-1 resolveEdge-failure case), but hopWalk's SEPARATE,
// subsequent neighbor-bookkeeping fetch of the exact same node (used only to
// decide whether the walk should continue one more hop from there) fails.
// Before this fix that second fetch's error was indistinguishable from a
// legitimate "this neighbor no longer exists" (`if err != nil || n == nil {
// continue }`), so a real backend failure reached only through that
// bookkeeping path never surfaced as Coverage.Partial even though it meant a
// further hop was silently never explored.
func TestDiscoverContextReportsPartialOnHopWalkNeighborBookkeepingLookupFailure(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	workTargetCalls := 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "DEPENDS_ON", propRelationshipID: "relationship_1", propEvidenceRefs: []string{"evidence_1"}}},
				"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_target",
			}}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "work_target":
				workTargetCalls++
				if workTargetCalls >= 2 {
					// 1st call: resolveEdge's own `to`-endpoint fetch. 2nd+
					// call: hopWalk's separate neighbor-bookkeeping fetch --
					// the one the round-1 fix left unhandled.
					return nil, errors.New("simulated backend failure on neighbor bookkeeping fetch")
				}
				return []row{fakeSubjectNodeRow("work_item", "work_target", "Target")}, nil
			case "p1":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			default:
				return nil, nil
			}
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v, want a degraded-but-successful result", err)
	}
	if edge := findFakeEdge(result.Paths, "DEPENDS_ON"); edge == nil {
		t.Fatalf("the admitted edge itself must still surface (resolveEdge succeeded) -- got no DEPENDS_ON path: %#v", result.Paths)
	}
	if !result.Coverage.Partial {
		t.Fatalf("Coverage = %#v, want Partial=true -- the hop-walk's own neighbor-bookkeeping fetch failed, so a further hop from work_target was never explored", result.Coverage)
	}
	if len(result.Coverage.DegradedReasons) == 0 {
		t.Fatal("Coverage.DegradedReasons is empty, want a reason recorded for the failed neighbor lookup")
	}
}

// TestDiscoverContextRanksWithinASingleNodesNeighborListBeforeTruncating is
// the Codex P2a round-2 probe. A single node (the origin) has SIX
// neighbors, all returned by ONE edgesOfNode call, in the WORST-first
// arrival order relative to graphrank's UUID tie-break (rel_f..rel_a
// descending, so an unordered/arrival-order collection would keep exactly
// the wrong three). collectLimit (config.MaxResults) is 3, well under the
// six available -- round 1 fixed the OUTER MaxRelationshipPaths-vs-MaxResults
// confusion but left this inner gap: once genuine candidates exceed
// collectLimit itself, the walk still needs to rank before it truncates,
// which is exactly what hopWalk's per-hop rankCandidateEdges call
// now does.
func TestDiscoverContextRanksWithinASingleNodesNeighborListBeforeTruncating(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	relIDs := []string{"rel_f", "rel_e", "rel_d", "rel_c", "rel_b", "rel_a"} // worst-first arrival order
	targets := map[string]string{
		"rel_f": "work_f", "rel_e": "work_e", "rel_d": "work_d",
		"rel_c": "work_c", "rel_b": "work_b", "rel_a": "work_a",
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			rows := make([]row, 0, len(relIDs))
			for _, relID := range relIDs {
				target := targets[relID]
				rows = append(rows, row{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "DEPENDS_ON", propRelationshipID: relID, propEvidenceRefs: []string{"evidence_" + relID}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": target,
				})
			}
			return rows, nil
		default: // nodeByKindID
			if params["id"] == "p1" {
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			}
			for relID, target := range targets {
				if params["id"] == target {
					return []row{fakeSubjectNodeRow("work_item", target, relID)}, nil
				}
			}
			return nil, nil
		}
	}}
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 3, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 3))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(result.Paths) != 3 {
		t.Fatalf("len(Paths) = %d, want exactly 3 (MaxResults=MaxRelationshipPaths=3): %#v", len(result.Paths), result.Paths)
	}
	admitted := make(map[string]bool, 3)
	for _, p := range result.Paths {
		for _, e := range p.Edges {
			admitted[e.To.CanonicalID] = true
		}
	}
	for _, target := range []string{"work_a", "work_b", "work_c"} {
		if !admitted[target] {
			t.Fatalf("admitted targets = %v, want work_a/work_b/work_c (the tie-break winners) -- the collection layer starved the correct edges in favor of whichever arrived first", admitted)
		}
	}
	for _, target := range []string{"work_d", "work_e", "work_f"} {
		if admitted[target] {
			t.Fatalf("admitted targets = %v, must NOT include %q -- it loses the UUID tie-break to work_a/work_b/work_c", admitted, target)
		}
	}
}
