package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is the CHAOS-3835 codex review finding-1 proof (P1,
// vector_projection.go:130-131 in the pre-fix revision): stale-vector
// survival when a previously-named CI run's projection later carries an
// empty/id-only name and branch.

// TestEmbedProjectionBatchClearsStaleVectorWhenSubjectBecomesIDOnly is the
// finding-1 proof: a CI run whose name/branch made it embeddable in a PRIOR
// batch (and so may already carry a current-tag vector + embedder identity
// on the node) loses its name/branch in THIS batch, becoming id-only. The
// batch must not just record the skip -- it must clear any stale vector and
// identity through the same clearNodeVectors mechanism every other
// stale-vector path already uses, so ANN retrieval can never serve a vector
// derived from search_text that no longer matches the node.
//
// Mutation check: reverting the vector_projection.go fix (dropping the
// `a.clearNodeVectors(ctx, key, batch.OrgID, idOnlyTargets)` call this
// finding adds to the len(targets)==0 commit path) makes this test fail --
// no clear-shaped query would ever be issued for the id-only row's
// canonical id. Verified live against the pre-fix code.
func TestEmbedProjectionBatchClearsStaleVectorWhenSubjectBecomesIDOnly(t *testing.T) {
	t.Parallel()
	var clearedIDs []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		// clearNodeVectors' cypher shape: UNWIND $targets AS t MATCH (...)
		// SET n.embedding = NULL, n.embedder_identity = NULL, n.embedder_dimension = NULL.
		if strings.Contains(cypher, "n."+propEmbedding+" = NULL") && strings.Contains(cypher, "n."+propEmbedderIdentity+" = NULL") {
			if targets, ok := params["targets"].([]interface{}); ok {
				for _, raw := range targets {
					if m, ok := raw.(map[string]interface{}); ok {
						if id, ok := m["id"].(string); ok {
							clearedIDs = append(clearedIDs, id)
						}
					}
				}
			}
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	telemetry := &recordingTelemetry{}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 4)}, telemetry)

	observed := time.Now().UTC()
	// THIS batch's projection carries no pipeline_name/branch -- id-only --
	// even though a prior batch (not modeled here; embedProjectionBatch
	// never reads the graph back) may have embedded this exact
	// kind/canonicalID under a real name that has since gone empty.
	idOnlyNow := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-42", Label: "CI run-42"},
		Properties: map[string]contextfabric.ScalarValue{
			"repo": scalar("example-org/widget-service"),
		},
		ObservedAt: observed, SourceVersion: "v1",
	}
	batch := contextfabric.ProjectionBatch{
		OrgID:    "org-1",
		Entities: []contextfabric.EntityProjection{idOnlyNow},
	}

	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	found := false
	for _, id := range clearedIDs {
		if id == "ci_pipeline_run:run-42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a subject that just became id-only must have its stale vector AND identity cleared -- no clear query named ci_pipeline_run:run-42, got clears for %v", clearedIDs)
	}
	// Round-2 finding 1: the id-only clear ATTEMPT must not be reported
	// under `cleared` -- that field means "genuine stale/error clear" and
	// this is a routine, deterministic consequence of the id-only skip
	// itself, already visible via skippedIDOnly. The clear still fires
	// (proven above); it just doesn't masquerade as a Warn-worthy event.
	if telemetry.cleared != 0 {
		t.Errorf("telemetry.cleared = %d, want 0 -- an id-only clear is routine, not a genuine stale/error clear (round-2 finding 1)", telemetry.cleared)
	}
	if telemetry.skippedIDOnly != 1 {
		t.Errorf("telemetry.skippedIDOnly = %d, want 1", telemetry.skippedIDOnly)
	}
}

// TestEmbedProjectionBatchClearsIDOnlySubjectAlongsideASuccessfulEmbed
// exercises the SAME finding-1 gap on the ordinary full-success commit path
// (a batch that also embeds a real subject), rather than the
// len(targets)==0 early return -- the success path was the path the
// original code never cleared idOnlyTargets on at all.
func TestEmbedProjectionBatchClearsIDOnlySubjectAlongsideASuccessfulEmbed(t *testing.T) {
	t.Parallel()
	var writtenIDs, clearedIDs []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
		switch {
		case strings.Contains(cypher, "vecf32($vec)") && strings.Contains(cypher, "SET n."):
			if id, ok := params["id"].(string); ok {
				writtenIDs = append(writtenIDs, id)
			}
		case strings.Contains(cypher, "n."+propEmbedding+" = NULL"):
			if targets, ok := params["targets"].([]interface{}); ok {
				for _, raw := range targets {
					if m, ok := raw.(map[string]interface{}); ok {
						if id, ok := m["id"].(string); ok {
							clearedIDs = append(clearedIDs, id)
						}
					}
				}
			}
		}
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	telemetry := &recordingTelemetry{}
	adapter := vectorAdapterWithTelemetry(t, fake, &stubEmbedder{vector: make([]float32, 4)}, telemetry)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org-1",
		Entities: []contextfabric.EntityProjection{
			{
				Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-42", Label: "CI run-42"},
				Properties: map[string]contextfabric.ScalarValue{
					"repo": scalar("example-org/widget-service"),
				},
				ObservedAt: observed, SourceVersion: "v1",
			},
			{
				Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service"},
				ObservedAt: observed, SourceVersion: "v1",
			},
		},
	}

	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	if len(writtenIDs) != 1 || writtenIDs[0] != "p1" {
		t.Fatalf("expected exactly one vector write, for p1 only, got %v", writtenIDs)
	}
	found := false
	for _, id := range clearedIDs {
		if id == "ci_pipeline_run:run-42" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a full-success batch must still clear a stale vector for an id-only-skipped subject, got clears for %v", clearedIDs)
	}
	if telemetry.embedded != 1 {
		t.Errorf("telemetry.embedded = %d, want 1", telemetry.embedded)
	}
	// Round-2 finding 1: same reasoning as the len(targets)==0 test above --
	// the id-only clear is routine and must not inflate `cleared`, which
	// reports only genuine stale/error clears.
	if telemetry.cleared != 0 {
		t.Errorf("telemetry.cleared = %d, want 0 -- an id-only clear is routine, not a genuine stale/error clear (round-2 finding 1)", telemetry.cleared)
	}
}
