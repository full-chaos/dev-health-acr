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

// This file holds the Codex round-1 (commits 9172e11..ccfef34) probe tests
// whose property under test is the exact ORDER/COUNT of internal query
// calls, or a failure injected into one specific call -- something a real
// FalkorDB server's own enumeration order (unordered without an explicit
// ORDER BY, confirmed live -- see queries.go's fulltextSearchNodes doc
// comment) cannot reliably reproduce on demand. fakeConn is a minimal,
// fully in-process conn double used only here; pure_test.go's own doc
// comment explains why the rest of this package's tests prefer a real
// server instead.

type fakeConn struct {
	queryFunc       func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error)
	constraintsFunc func(ctx context.Context, graphKey string) ([]constraintStatus, error)
}

func (f *fakeConn) query(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
	if f.queryFunc != nil {
		return f.queryFunc(ctx, graphKey, cypher, params, readOnly)
	}
	return nil, nil
}
func (f *fakeConn) deleteGraph(ctx context.Context, graphKey string) error { return nil }
func (f *fakeConn) listGraphs(ctx context.Context) ([]string, error)       { return nil, nil }
func (f *fakeConn) constraints(ctx context.Context, graphKey string) ([]constraintStatus, error) {
	if f.constraintsFunc != nil {
		return f.constraintsFunc(ctx, graphKey)
	}
	return []constraintStatus{}, nil
}
func (f *fakeConn) indexes(ctx context.Context, graphKey string) ([]indexStatus, error) {
	return nil, nil
}
func (f *fakeConn) createIndex(ctx context.Context, graphKey, label string, properties []string, relationship bool) error {
	return nil
}
func (f *fakeConn) createConstraint(ctx context.Context, graphKey string, unique bool, entityType, label string, properties []string) error {
	return nil
}

func newFakeAdapter(t *testing.T, fake *fakeConn) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI() error = %v", err)
	}
	return adapter
}

func fakeSubjectNodeRow(kind, canonicalID, label string) row {
	return row{"n": &node{Properties: map[string]interface{}{propKind: kind, propCanonicalID: canonicalID, propLabel: label}}}
}

func findFakeEdge(paths []contextfabric.RelationshipPath, relationType string) *contextfabric.RelationshipEdge {
	for pathIndex := range paths {
		for edgeIndex := range paths[pathIndex].Edges {
			if paths[pathIndex].Edges[edgeIndex].Type == relationType {
				return &paths[pathIndex].Edges[edgeIndex]
			}
		}
	}
	return nil
}

func fakeDiscoveryRequest(origin contextfabric.SubjectRef, maxRelationshipPaths int) contextfabric.GraphDiscoveryRequest {
	return contextfabric.GraphDiscoveryRequest{
		Request: contextfabric.InvestigationRequest{
			Question: "what does it depend on",
			Options: contextfabric.InvestigationOptions{
				MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: maxRelationshipPaths,
				MaxDrivers: 10, MaxEvidenceRefs: 50, MaxSerializedBytes: 262144,
			},
		},
		Interpretation: contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeSingleSubject},
		Resolution:     contextfabric.SubjectResolution{Committed: []contextfabric.SubjectRef{origin}},
	}
}

// TestBootstrapSchemaRejectsUnknownConstraintStatus is the Codex P2e probe:
// pollConstraintsOperational must treat status as a strict allowlist
// (OPERATIONAL passes, PENDING keeps polling, everything else -- including a
// status value this code has never seen before -- fails bootstrap
// immediately). The original code defaulted allOperational=true and only
// flipped it for the two known-bad statuses, so any OTHER value silently
// fell through as "operational".
func TestBootstrapSchemaRejectsUnknownConstraintStatus(t *testing.T) {
	fake := &fakeConn{constraintsFunc: func(ctx context.Context, graphKey string) ([]constraintStatus, error) {
		return []constraintStatus{{Type: "UNIQUE", Label: labelSubject, EntityType: "NODE", Status: "SOME_FUTURE_STATUS_THIS_CODE_DOES_NOT_KNOW"}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	err := adapter.bootstrapSchema(context.Background(), "test-key")
	if !errors.Is(err, errConstraintBootstrapFailed) {
		t.Fatalf("bootstrapSchema() with an unrecognized constraint status = %v, want errConstraintBootstrapFailed", err)
	}
}

// TestBootstrapSchemaRejectsEmptyConstraintStatus is the same probe for the
// other half of the P2e finding: an empty status string (a malformed/partial
// response) must not be treated as operational either.
func TestBootstrapSchemaRejectsEmptyConstraintStatus(t *testing.T) {
	fake := &fakeConn{constraintsFunc: func(ctx context.Context, graphKey string) ([]constraintStatus, error) {
		return []constraintStatus{{Type: "UNIQUE", Label: labelSubject, EntityType: "NODE", Status: ""}}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	err := adapter.bootstrapSchema(context.Background(), "test-key")
	if !errors.Is(err, errConstraintBootstrapFailed) {
		t.Fatalf("bootstrapSchema() with an empty constraint status = %v, want errConstraintBootstrapFailed", err)
	}
}

// TestBootstrapSchemaAcceptsAllOperationalConstraints is the control case:
// the strict allowlist must still let a genuinely healthy bootstrap through.
func TestBootstrapSchemaAcceptsAllOperationalConstraints(t *testing.T) {
	fake := &fakeConn{constraintsFunc: func(ctx context.Context, graphKey string) ([]constraintStatus, error) {
		return []constraintStatus{
			{Type: "UNIQUE", Label: labelSubject, EntityType: "NODE", Status: "OPERATIONAL"},
			{Type: "UNIQUE", Label: labelRelation, EntityType: "RELATIONSHIP", Status: "OPERATIONAL"},
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.bootstrapSchema(context.Background(), "test-key"); err != nil {
		t.Fatalf("bootstrapSchema() with all-OPERATIONAL statuses error = %v, want nil", err)
	}
}

// TestDiscoverContextReportsPartialOnEndpointLookupFailure is the Codex P2c
// probe: a genuine backend failure resolving an edge endpoint (as opposed to
// a legitimate "not found" or authorization filter) must never silently
// produce clean, complete-looking coverage.
func TestDiscoverContextReportsPartialOnEndpointLookupFailure(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Origin"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "p1" {
				return nil, nil
			}
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "DEPENDS_ON", propRelationshipID: "relationship_1"}},
				"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_target",
			}}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "work_target":
				return nil, errors.New("simulated backend failure")
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
	if !result.Coverage.Partial {
		t.Fatalf("Coverage = %#v, want Partial=true for an injected endpoint-lookup failure", result.Coverage)
	}
	if len(result.Coverage.DegradedReasons) == 0 {
		t.Fatal("Coverage.DegradedReasons is empty, want a reason recorded for the failed lookup")
	}
	if edge := findFakeEdge(result.Paths, "DEPENDS_ON"); edge != nil {
		t.Fatalf("result surfaced a path built from an edge whose endpoint lookup failed: %#v", edge)
	}
}

// TestDiscoverContextRanksBeforeTruncatingCollectedEdges is the Codex P2a
// probe. Two edges are reachable from the origin in a single edgesOfNode
// call, returned in this exact order: "rel_zzz" is encountered FIRST during
// collection, "rel_aaa" second. Neither carries a real relevance/score
// (this is the hop-walk path, not full-text search), so
// graphrank.SortEdgesByRelevance ties both at ResultConfidence=0 and falls
// back to its documented, deterministic tie-break: ascending relationship
// UUID. "rel_aaa" is therefore the one and only correct admission when
// MaxRelationshipPaths=1 -- proving admission order comes from
// SortEdgesByRelevance applied to the FULL collected set, not from whichever
// edge collection happened to reach first. The former bug truncated hop-walk
// collection to len(edges)>=MaxRelationshipPaths DURING the walk itself, so
// it would have kept only "rel_zzz" (the first one physically encountered)
// and never even seen "rel_aaa".
func TestDiscoverContextRanksBeforeTruncatingCollectedEdges(t *testing.T) {
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
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "DEPENDS_ON", propRelationshipID: "rel_zzz", propEvidenceRefs: []string{"evidence_zzz"}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_zzz",
				},
				{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "DEPENDS_ON", propRelationshipID: "rel_aaa", propEvidenceRefs: []string{"evidence_aaa"}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_aaa",
				},
			}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "p1":
				return []row{fakeSubjectNodeRow("project", "p1", "Origin")}, nil
			case "work_zzz":
				return []row{fakeSubjectNodeRow("work_item", "work_zzz", "Zzz")}, nil
			case "work_aaa":
				return []row{fakeSubjectNodeRow("work_item", "work_aaa", "Aaa")}, nil
			default:
				return nil, nil
			}
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 1))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v", err)
	}
	if len(result.Paths) != 1 {
		t.Fatalf("len(Paths) = %d, want exactly 1 (MaxRelationshipPaths=1): %#v", len(result.Paths), result.Paths)
	}
	edge := findFakeEdge(result.Paths, "DEPENDS_ON")
	if edge == nil || edge.To.CanonicalID != "work_aaa" {
		t.Fatalf("admitted edge = %#v, want the relationship to work_aaa (rel_aaa sorts first; the tight-collection bug would instead have kept rel_zzz, the edge physically encountered first)", edge)
	}
}
