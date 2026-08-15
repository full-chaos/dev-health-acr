package falkorgraph

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is the CHAOS-3835 codex ROUND-4 review finding-1 proof (P2,
// vector_projection.go:148 in the round-3-fixed revision): with no
// embedder configured (e.g. ACR_CONTEXT_FABRIC_EMBED_BASE_URL unset),
// embedProjectionBatch returned before collectEmbedTargets ever ran, so a
// subject that transitions from semantic to id-only WHILE vector retrieval
// is disabled kept whatever vector + embedder-identity stamp an earlier
// (embedder-enabled) batch had written. While disabled, reads are safe
// (ensureVectorReadable/vectorIndexUsable both return false when
// a.embedder is nil -- verified by inspection, nothing is ever served
// meanwhile) -- but the moment the embedder is RE-ENABLED with the SAME
// identity and dimension, verifyStoredEmbedderIdentity's fence compares
// only the stored identity string to the CURRENTLY configured embedder; it
// never asks whether this specific row should still carry a vector. The
// stale vector then passes the fence and gets served against search_text
// it no longer has any relationship to.
//
// Fix: the nil-embedder early return now still collects and clears
// id-only-skipped targets -- collectEmbedTargets is pure batch inspection
// and clearNodeVectors is a plain graph write; neither needs an embedder.
// Only the actual embedding (Embed, writeNodeVector, the index/dimension
// checks) stays gated on a.embedder being configured.

func nilEmbedderAdapterWithTelemetry(t *testing.T, fake *fakeConn, telemetry GraphTelemetry) *Adapter {
	t.Helper()
	adapter, err := newWithAPI(Config{
		Addr: "fake:6379", GraphPrefix: "acr-cf-fake", RequestTimeout: time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true, Telemetry: telemetry,
	}, fake)
	if err != nil {
		t.Fatalf("newWithAPI: %v", err)
	}
	// Deliberately NOT calling attachEmbedder -- a.embedder stays nil, the
	// exact "ACR_CONTEXT_FABRIC_EMBED_BASE_URL unset" state this finding
	// is about.
	return adapter
}

// TestEmbedProjectionBatchClearsIDOnlyTargetsEvenWithNoEmbedderConfigured
// is the finding-1 proof: a batch with no embedder configured, containing
// a subject that is id-only in THIS projection, must still clear any
// stale vector/identity for that subject.
//
// Mutation check: reverting the vector_projection.go fix (restoring the
// bare `if a.embedder == nil { return nil }` early return) makes this test
// fail -- no clear-shaped query is ever issued, and no telemetry fires.
// Verified live against the round-3-only code.
func TestEmbedProjectionBatchClearsIDOnlyTargetsEvenWithNoEmbedderConfigured(t *testing.T) {
	t.Parallel()
	var clearedIDs []string
	fake := &fakeConn{queryFunc: func(_ context.Context, _, cypher string, params map[string]interface{}, _ bool) ([]row, error) {
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
	telemetry := &recordingTelemetry{}
	adapter := nilEmbedderAdapterWithTelemetry(t, fake, telemetry)
	if adapter.embedder != nil {
		t.Fatal("fixture error: adapter must have no embedder configured")
	}

	// This entity's projection this batch carries no pipeline_name/branch
	// -- id-only -- regardless of whatever an EARLIER (embedder-enabled)
	// batch may have written for this exact kind/canonicalID.
	idOnlyNow := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-42", Label: "CI run-42"},
		Properties: map[string]contextfabric.ScalarValue{
			"repo": scalar("example-org/widget-service"),
		},
		ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	batch := contextfabric.ProjectionBatch{OrgID: "org-1", Entities: []contextfabric.EntityProjection{idOnlyNow}}

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
		t.Fatalf("an id-only subject must have its stale vector cleared even with no embedder configured (round-4 finding 1) -- got clears for %v", clearedIDs)
	}
	if telemetry.skippedIDOnly != 1 {
		t.Errorf("telemetry.skippedIDOnly = %d, want 1 -- the skip must still be reported with no embedder configured", telemetry.skippedIDOnly)
	}
	if telemetry.embedded != 0 || telemetry.cleared != 0 {
		t.Errorf("telemetry.embedded=%d cleared=%d, want 0/0 -- nothing was embedded and the id-only clear is routine, not a genuine stale/error clear", telemetry.embedded, telemetry.cleared)
	}
}

// TestEmbedProjectionBatchWithNoEmbedderNeverTouchesANonIDOnlySubject is a
// negative control: with no embedder configured, a batch containing only
// an ordinary (non-id-only) subject must issue no clear query at all --
// the fix must not turn the nil-embedder path into an unconditional clear
// of everything.
func TestEmbedProjectionBatchWithNoEmbedderNeverTouchesANonIDOnlySubject(t *testing.T) {
	t.Parallel()
	queried := false
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		queried = true
		return nil, nil
	}}
	telemetry := &recordingTelemetry{}
	adapter := nilEmbedderAdapterWithTelemetry(t, fake, telemetry)

	realSubject := contextfabric.EntityProjection{
		Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service"},
		ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	batch := contextfabric.ProjectionBatch{OrgID: "org-1", Entities: []contextfabric.EntityProjection{realSubject}}

	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	if queried {
		t.Fatal("no clear query should run when the batch has no id-only-skipped subjects")
	}
	if telemetry.skippedIDOnly != 0 || telemetry.skippedKind != 0 {
		t.Errorf("telemetry skips = kind:%d idOnly:%d, want 0/0 for an ordinary subject", telemetry.skippedKind, telemetry.skippedIDOnly)
	}
}
