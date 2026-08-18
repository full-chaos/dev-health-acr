package falkorgraph

import (
	"context"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDiscoverContextReportsEdgesFilteredByAuthzReasonThroughTelemetry
// (CHAOS-3888) proves the edges_filtered_by_reason{authz} signal: a
// candidate edge whose own attributes AuthorizedAttributes denies must be
// excluded from the result exactly as before (Codex P1's pre-existing
// behavior, unchanged), AND now also reported through
// GraphTelemetry.RecordEdgesFilteredByReason's authz count -- while an
// authorized sibling edge from the SAME origin must still be admitted and
// must not inflate that count.
func TestDiscoverContextReportsEdgesFilteredByAuthzReasonThroughTelemetry(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{
				{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "BLOCKS", propRelationshipID: "relationship_authorized",
						propEvidenceRefs:             []string{"evidence_authorized_1234"},
						"authorization_repositories": []string{"full-chaos/dev-health-acr"},
					}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_target",
				},
				{
					"r": &edge{Properties: map[string]interface{}{
						propRelationType: "RELATES_TO", propRelationshipID: "relationship_denied",
						"authorization_repositories": []string{"other/private"},
					}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_denied",
				},
			}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "p1":
				originRow := fakeSubjectNodeRow("project", "p1", "Origin")
				originRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				return []row{originRow}, nil
			case "work_target":
				targetRow := fakeSubjectNodeRow("work_item", "work_target", "Target")
				targetRow["n"].(*node).Properties["authorization_repositories"] = []string{"full-chaos/dev-health-acr"}
				return []row{targetRow}, nil
			case "work_denied":
				t.Fatal("nodeByKindID must never be called for work_denied -- resolveEdge's edge-attribute authz check must reject it BEFORE any endpoint fetch")
				return nil, nil
			default:
				return nil, nil
			}
		}
	}}
	telemetry := &recordingTelemetry{}
	adapter := newFakeAdapterWithTelemetry(t, fake, telemetry)
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if edge := findFakeEdge(result.Paths, "BLOCKS"); edge == nil {
		t.Fatalf("result.Paths = %#v, want the authorized BLOCKS edge admitted", result.Paths)
	}
	if edge := findFakeEdge(result.Paths, "RELATES_TO"); edge != nil {
		t.Fatalf("result surfaced a path built from an edge AuthorizedAttributes denied: %#v", edge)
	}
	// An authorization exclusion is ordinary, expected behavior -- it must
	// NOT mark Coverage.Partial (that stays reserved for genuine backend
	// failures, Codex P2c).
	if result.Coverage.Partial {
		t.Fatalf("Coverage.Partial = true, want false: an authorization-filtered edge is not degradation")
	}
	if telemetry.edgesFilteredAuthz != 1 {
		t.Fatalf("edgesFilteredAuthz telemetry = %d, want exactly 1 (the authorized sibling must not inflate it)", telemetry.edgesFilteredAuthz)
	}
	if telemetry.edgesFilteredTemporalWindow != 0 || telemetry.edgesFilteredSelfLoop != 0 {
		t.Fatalf("edgesFilteredTemporalWindow/SelfLoop = %d/%d, want both 0", telemetry.edgesFilteredTemporalWindow, telemetry.edgesFilteredSelfLoop)
	}
}
