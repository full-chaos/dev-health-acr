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

func findFakeEdge(paths []contextfabric.RelationshipPath, relationType contextfabric.RelationshipType) *contextfabric.RelationshipEdge {
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

// TestBootstrapSchemaPollsThroughUnderConstruction proves "UNDER
// CONSTRUCTION" is a legitimate in-progress status, not an unrecognized one:
// live FalkorDB (confirmed by running a constraint build against a loaded
// graph in a real container -- CHAOS-3752 CI failure) reports it between
// PENDING and OPERATIONAL. Bootstrap must keep polling on it exactly like
// PENDING, then succeed once the status transitions to OPERATIONAL.
func TestBootstrapSchemaPollsThroughUnderConstruction(t *testing.T) {
	calls := 0
	fake := &fakeConn{constraintsFunc: func(ctx context.Context, graphKey string) ([]constraintStatus, error) {
		calls++
		status := "UNDER CONSTRUCTION"
		if calls > 2 {
			status = "OPERATIONAL"
		}
		return []constraintStatus{
			{Type: "UNIQUE", Label: labelSubject, EntityType: "NODE", Status: status},
			{Type: "UNIQUE", Label: labelRelation, EntityType: "RELATIONSHIP", Status: status},
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.bootstrapSchema(context.Background(), "test-key"); err != nil {
		t.Fatalf("bootstrapSchema() with UNDER CONSTRUCTION then OPERATIONAL error = %v, want nil", err)
	}
	if calls < 3 {
		t.Fatalf("bootstrapSchema() called constraints() %d times, want >= 3 (must actually poll through UNDER CONSTRUCTION)", calls)
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
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "relationship_1"}},
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
	if edge := findFakeEdge(result.Paths, "BLOCKS"); edge != nil {
		t.Fatalf("result surfaced a path built from an edge whose endpoint lookup failed: %#v", edge)
	}
}

// TestDiscoverContextFromBlockedSideStillSurfacesBLOCKSNotAnInvertedName is
// CHAOS-3779's direction-safety probe for pruning BLOCKED_BY out of
// graphrank.relationMeaning (team-lead review caution: before pruning the
// inverse-direction recognizer entry, verify nothing in the traversal path
// returns the SAME edge with an inverse name when read from the target
// side -- if it did, pruning BLOCKED_BY would silently drop driver
// standing for exactly half the directions, the H4 failure shape again).
//
// This starts DiscoverContext from work_target -- the BLOCKED work item,
// i.e. the target/dst side of the stored 'blocks' edge -- exactly the
// direction edgesOfNode's UNION second branch
// ((other)-[r]->(n) where n = the queried node) serves. edgesOfNode
// (queries.go) and toCandidateEdge read propRelationType verbatim off the
// stored edge in BOTH UNION branches -- there is no direction-conditional
// rewriting anywhere in this package (grep-verified: toCandidateEdge is
// the only call site in the repository that ever constructs a
// CandidateEdge.Name from a graph-read edge) -- so the relation always
// surfaces as the literal stored value, "BLOCKS", never a synthesized
// "BLOCKED_BY", regardless of which endpoint's traversal found it. This
// test proves that behavior end to end: querying from the blocked side
// still returns a "BLOCKS" edge, correctly oriented From=work_blocker
// To=work_target (never inverted), and still carries DriverPrincipal
// standing -- so pruning the producer-less BLOCKED_BY recognizer entry
// drops nothing, in either direction.
func TestDiscoverContextFromBlockedSideStillSurfacesBLOCKSNotAnInvertedName(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_target", Label: "Blocked Work"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "work_target" {
				return nil, nil
			}
			// L5 (CHAOS-3779 codex round-1): assert the query text itself,
			// not just serve a canned response -- without this, the test
			// would still pass even if edgesOfNode's real Cypher regressed
			// to the outgoing-only branch and dropped the incoming
			// "(other)-[r]->(n)" branch entirely, since the fake never
			// executes real Cypher and would happily serve this row for
			// ANY query containing the literal substring "UNION". This
			// binds the test to the actual query shape, not just its
			// keyword.
			if !strings.Contains(cypher, ")-[r:"+labelRelation+"]->(n:"+labelSubject) {
				t.Fatalf("edgesOfNode's query text does not contain the incoming UNION branch (other)-[r]->(n) -- this test cannot prove direction safety against a query that dropped it: %s", cypher)
			}
			// The real edgesOfNode UNION's second branch --
			// (other)-[r]->(n) where n is the queried node -- reports
			// srcKind/srcId/dstKind/dstId from the edge's TRUE stored
			// direction, not from which side was queried. This fake
			// encodes exactly that: work_target is the query origin, but
			// it is the edge's DESTINATION (the blocked item); work_blocker
			// is the edge's SOURCE (the blocker).
			return []row{{
				"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "relationship_blocked_side", propEvidenceRefs: []string{"evidence_blocked_side"}}},
				"srcKind": "work_item", "srcId": "work_blocker", "dstKind": "work_item", "dstId": "work_target",
			}}, nil
		default: // nodeByKindID (origin resolution + hop-walk neighbor resolution)
			switch params["id"] {
			case "work_target":
				return []row{fakeSubjectNodeRow("work_item", "work_target", "Blocked Work")}, nil
			case "work_blocker":
				return []row{fakeSubjectNodeRow("work_item", "work_blocker", "Blocker Work")}, nil
			default:
				return nil, nil
			}
		}
	}}
	adapter := newFakeAdapter(t, fake)
	principal := storage.Principal{OrgID: "org-1"}

	result, err := adapter.DiscoverContext(context.Background(), principal, fakeDiscoveryRequest(origin, 10))
	if err != nil {
		t.Fatalf("DiscoverContext() error = %v, want a clean result", err)
	}
	edge := findFakeEdge(result.Paths, "BLOCKS")
	if edge == nil {
		t.Fatalf("no BLOCKS edge surfaced when querying from the blocked side: %#v", result.Paths)
	}
	if edge.From.CanonicalID != "work_blocker" || edge.To.CanonicalID != "work_target" {
		t.Fatalf("edge direction = %s -> %s, want work_blocker -> work_target (the edge's true stored direction, unchanged by which side was queried)", edge.From.CanonicalID, edge.To.CanonicalID)
	}
	if len(result.DriverCandidates) != 1 || result.DriverCandidates[0].Standing != contextfabric.DriverPrincipal {
		t.Fatalf("DriverCandidates = %#v, want exactly one principal-standing driver -- BLOCKS must still be recognized when discovered from the blocked side", result.DriverCandidates)
	}
}

// TestDiscoverContextMarksCoveragePartialOnUnknownRelationshipType is
// CHAOS-3779 codex round-1 finding H1's regression test: before this,
// AdmitEdges dropped an edge whose Type failed the closed
// ContextFabricRelationshipType vocabulary with the same silent `continue`
// as any other malformed edge -- no metric, no degraded-coverage signal.
// That is the H4 failure shape (silent admission failure) relocated to the
// READ path: a write-path producer bug, a partial rollback leaving legacy
// free-form data, or a future contract downgrade could all put an
// out-of-vocabulary type in the graph, and a caller would have no way to
// tell "this investigation is honestly complete" from "this investigation
// silently lost material."
//
// This plants one edge with relation_type "LEGACY_UNKNOWN_TYPE" (never a
// member of the closed vocabulary) reachable from the origin, alongside no
// other edges, and proves DiscoverContext returns a Partial=true result
// with a named "unknown_relationship_type:1" degraded reason -- not a
// clean, empty-but-successful result indistinguishable from "the graph
// genuinely has nothing to say here."
func TestDiscoverContextMarksCoveragePartialOnUnknownRelationshipType(t *testing.T) {
	origin := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_legacy", Label: "Legacy Work"}
	fake := &fakeConn{queryFunc: func(ctx context.Context, graphKey, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "fulltext"):
			return nil, nil
		case strings.Contains(cypher, "UNION"):
			if params["id"] != "work_legacy" {
				return nil, nil
			}
			return []row{{
				"r": &edge{Properties: map[string]interface{}{
					propRelationType: "LEGACY_UNKNOWN_TYPE", propRelationshipID: "relationship_legacy_1",
					propEvidenceRefs: []string{"evidence_legacy_1"},
				}},
				"srcKind": "work_item", "srcId": "work_legacy", "dstKind": "work_item", "dstId": "work_other",
			}}, nil
		default: // nodeByKindID
			switch params["id"] {
			case "work_legacy":
				return []row{fakeSubjectNodeRow("work_item", "work_legacy", "Legacy Work")}, nil
			case "work_other":
				return []row{fakeSubjectNodeRow("work_item", "work_other", "Other Work")}, nil
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
	if len(result.Paths) != 0 {
		t.Fatalf("result.Paths = %#v, want the out-of-vocabulary edge excluded from the admitted paths", result.Paths)
	}
	if !result.Coverage.Partial {
		t.Fatalf("Coverage = %#v, want Partial=true -- an unknown relationship type must not present as clean, complete coverage", result.Coverage)
	}
	found := false
	for _, reason := range result.Coverage.DegradedReasons {
		if reason == "unknown_relationship_type:1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Coverage.DegradedReasons = %#v, want \"unknown_relationship_type:1\"", result.Coverage.DegradedReasons)
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
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_zzz", propEvidenceRefs: []string{"evidence_zzz"}}},
					"srcKind": "project", "srcId": "p1", "dstKind": "work_item", "dstId": "work_zzz",
				},
				{
					"r":       &edge{Properties: map[string]interface{}{propRelationType: "BLOCKS", propRelationshipID: "rel_aaa", propEvidenceRefs: []string{"evidence_aaa"}}},
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
	edge := findFakeEdge(result.Paths, "BLOCKS")
	if edge == nil || edge.To.CanonicalID != "work_aaa" {
		t.Fatalf("admitted edge = %#v, want the relationship to work_aaa (rel_aaa sorts first; the tight-collection bug would instead have kept rel_zzz, the edge physically encountered first)", edge)
	}
}
