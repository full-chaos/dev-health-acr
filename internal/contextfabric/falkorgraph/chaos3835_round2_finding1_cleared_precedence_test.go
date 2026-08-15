package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// This file is the CHAOS-3835 codex ROUND-2 review finding-1 proof (P2,
// vector_projection.go:286 in the round-1-fixed revision): the round-1 fix
// for stale-vector survival folded every id-only-skip clear ATTEMPT into
// the SAME `cleared` count as genuine stale/error clears. Since
// RecordVectorProjection's telemetry precedence fires Warn ("cleared stale
// vectors") whenever cleared>0, a routine id-only skip -- ~22% of a live
// ci_pipeline_run corpus, per spec -- started masquerading as a mass
// stale-vector event on every batch that touched one, drowning out the
// genuine signal the Warn path exists for.
//
// The fix: `cleared` now counts ONLY genuine stale/error clears (dimension
// mismatch, embed failure, mid-batch write failure). The id-only clear
// still fires (finding 1's correctness requirement is unchanged -- proven
// in chaos3835_finding1_stale_vector_clear_test.go), it just isn't double
// -reported as a Warn-worthy event on top of the Info skip signal
// (skippedIDOnly) that already covers it.

func adapterWithSlogTelemetryToBuffer(t *testing.T, fake *fakeConn, embedder contextfabric.Embedder, buf *bytes.Buffer) *Adapter {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return vectorAdapterWithTelemetry(t, fake, embedder, SlogTelemetry{Logger: logger})
}

func noopQueryFake() *fakeConn {
	fake := &fakeConn{queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
		return nil, nil
	}}
	fake.indexesFunc = func(context.Context, string) ([]indexStatus, error) {
		return []indexStatus{operationalVectorIndex(4)}, nil
	}
	return fake
}

// TestEmbedProjectionBatchPureIDOnlySkipEmitsInfoNeverWarn is half of the
// round-2 finding-1 proof: a batch whose ONLY event is an id-only skip
// (nothing was ever embedded for this row, so its clear is a no-op) must
// surface as the Info skip signal and must NEVER emit the Warn
// mass-stale-clear signal.
//
// Mutation check: reverting the round-2 fix (folding idOnlyTargets' length
// back into the `cleared` argument) makes this test fail -- the Warn
// "cleared stale vectors" line reappears. Verified live against the
// round-1-only code.
func TestEmbedProjectionBatchPureIDOnlySkipEmitsInfoNeverWarn(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	adapter := adapterWithSlogTelemetryToBuffer(t, noopQueryFake(), &stubEmbedder{vector: make([]float32, 4)}, &buf)

	idOnlyNow := contextfabric.EntityProjection{
		Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-9", Label: "CI run-9"},
		Properties: map[string]contextfabric.ScalarValue{
			"repo": scalar("example-org/widget-service"),
		},
		ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	batch := contextfabric.ProjectionBatch{OrgID: "org-1", Entities: []contextfabric.EntityProjection{idOnlyNow}}

	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "projection batch skipped subjects") {
		t.Fatalf("expected the Info skip signal, got %q", out)
	}
	if strings.Contains(out, "projection batch cleared stale vectors") {
		t.Fatalf("a routine id-only skip must NOT emit the Warn mass-clear signal (round-2 finding 1), got %q", out)
	}
}

// TestEmbedProjectionBatchGenuineEmbedFailureStillWarns is the other half:
// a genuine stale/error clear (here, an embed failure on a real,
// non-id-only target) must still emit the Warn signal -- round-2's fix must
// not have accidentally silenced the ACTUAL degradation signal while fixing
// the false one.
func TestEmbedProjectionBatchGenuineEmbedFailureStillWarns(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	adapter := adapterWithSlogTelemetryToBuffer(t, noopQueryFake(),
		&stubEmbedder{vector: make([]float32, 4), err: errors.New("embed failed")}, &buf)

	realSubject := contextfabric.EntityProjection{
		Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service"},
		ObservedAt: time.Now().UTC(), SourceVersion: "v1",
	}
	batch := contextfabric.ProjectionBatch{OrgID: "org-1", Entities: []contextfabric.EntityProjection{realSubject}}

	if err := adapter.embedProjectionBatch(context.Background(), "k", batch); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "projection batch cleared stale vectors") {
		t.Fatalf("a genuine embed failure must still emit the Warn mass-clear signal, got %q", out)
	}
}

// TestEmbedProjectionBatchMixedBatchClearedCountsOnlyGenuineFailure is the
// adversarial combination: an id-only-skipped CI run AND a real target that
// fails to embed, in the SAME batch. `cleared` must count only the real
// target's genuine failure (1), never the id-only row's routine clear on
// top of it (which would read 2) -- the exact conflation this finding
// closes.
func TestEmbedProjectionBatchMixedBatchClearedCountsOnlyGenuineFailure(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	adapter := vectorAdapterWithTelemetry(t, noopQueryFake(),
		&stubEmbedder{vector: make([]float32, 4), err: errors.New("embed failed")}, telemetry)

	observed := time.Now().UTC()
	batch := contextfabric.ProjectionBatch{
		OrgID: "org-1",
		Entities: []contextfabric.EntityProjection{
			{
				Subject: contextfabric.SubjectRef{Kind: contractsv1.ContextFabricSubjectCIRun, CanonicalID: "ci_pipeline_run:run-9", Label: "CI run-9"},
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

	if telemetry.cleared != 1 {
		t.Errorf("telemetry.cleared = %d, want 1 -- only the genuine embed-failure clear (p1), not the id-only row's routine clear too (round-2 finding 1)", telemetry.cleared)
	}
	if telemetry.skippedIDOnly != 1 {
		t.Errorf("telemetry.skippedIDOnly = %d, want 1", telemetry.skippedIDOnly)
	}
}
