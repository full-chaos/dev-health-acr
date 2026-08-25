package falkorgraph

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/embedprovider"
)

// This file is the CHAOS-4147 item 3 / CHAOS-4259 proof: embedProjectionBatch's
// embed-call-failure branch (vector_projection.go) previously cleared a
// batch's vectors on ANY Embed() error, including a genuinely TRANSIENT one
// (a network blip, one bad tick), with no retry and no signal distinguishing
// "one bad batch" from a sustained outage. See Config.EmbedFailureMaxRetries/
// EmbedFailureRetryBackoff/EmbedFailureEscalateAfter and
// GraphTelemetry.RecordVectorProjectionEmbedFailuresEscalated.

func realProjectSubjectBatch(orgID string) contextfabric.ProjectionBatch {
	return contextfabric.ProjectionBatch{
		OrgID: orgID,
		Entities: []contextfabric.EntityProjection{{
			Subject:    contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "p1", Label: "Authentication Service"},
			ObservedAt: time.Now().UTC(), SourceVersion: "v1",
		}},
	}
}

// TestEmbedProjectionBatchRetriesTransientFailureThenSucceeds pins the core
// fix: a TRANSIENT embed failure (here, a context-deadline-shaped error) on
// the first attempt must not clear the batch's vector if a bounded retry
// succeeds -- the node ends up embedded, not cleared, and no escalation
// fires for a single recovered blip.
func TestEmbedProjectionBatchRetriesTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	embedder := &stubEmbedder{vector: make([]float32, 4), err: context.DeadlineExceeded, failFirstN: 1}
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), embedder, telemetry, 2, time.Millisecond, 5)

	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	if embedder.calls != 2 {
		t.Fatalf("embedder.calls = %d, want 2 (one failure, one successful retry)", embedder.calls)
	}
	if telemetry.cleared != 0 {
		t.Fatalf("telemetry.cleared = %d, want 0 -- a transient failure that succeeds on retry must never clear the vector", telemetry.cleared)
	}
	if telemetry.embedded != 1 {
		t.Fatalf("telemetry.embedded = %d, want 1", telemetry.embedded)
	}
	if len(telemetry.embedFailureEscalations) != 0 {
		t.Fatalf("a single recovered transient blip must never escalate, got %v", telemetry.embedFailureEscalations)
	}
}

// TestEmbedProjectionBatchClearsAfterExhaustingBoundedRetries pins that the
// retry is BOUNDED: a transient failure that never recovers still ends up
// clearing the batch's vectors (the existing, correct degrade-not-fail
// behavior for a genuine outage), after exactly MaxRetries+1 attempts -- not
// retried forever.
func TestEmbedProjectionBatchClearsAfterExhaustingBoundedRetries(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	embedder := &stubEmbedder{vector: make([]float32, 4), err: context.DeadlineExceeded}
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), embedder, telemetry, 2, time.Millisecond, 5)

	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	if embedder.calls != 3 {
		t.Fatalf("embedder.calls = %d, want 3 (1 initial + 2 bounded retries, never more)", embedder.calls)
	}
	if telemetry.cleared != 1 {
		t.Fatalf("telemetry.cleared = %d, want 1 -- retries exhausted, the batch must still clear (degrade, not fail)", telemetry.cleared)
	}
}

// TestEmbedProjectionBatchNeverRetriesAPersistentFailure pins that a
// PERSISTENT failure (this package's own sentinel, or -- as tested here --
// any error IsTransientEmbedError classifies false) is never retried: an
// identical retry would get an identical answer, so retrying only adds
// latency to every projection tick during an auth outage.
func TestEmbedProjectionBatchNeverRetriesAPersistentFailure(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	embedder := &stubEmbedder{vector: make([]float32, 4), err: embedprovider.ErrModelIdentityMismatch}
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), embedder, telemetry, 3, time.Millisecond, 5)

	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}

	if embedder.calls != 1 {
		t.Fatalf("embedder.calls = %d, want exactly 1 -- a persistent failure must never be retried", embedder.calls)
	}
	if telemetry.cleared != 1 {
		t.Fatalf("telemetry.cleared = %d, want 1", telemetry.cleared)
	}
}

// TestEmbedProjectionBatchEscalatesOnlyAfterConsecutiveFailureThreshold pins
// the escalation signal: fewer than EmbedFailureEscalateAfter consecutive
// batch failures for an organization must stay silent (the routine
// per-batch Warn RecordVectorProjection already covers a single bad batch);
// crossing the threshold must fire RecordVectorProjectionEmbedFailuresEscalated,
// and it must keep firing on every subsequent failing batch while the
// streak continues.
func TestEmbedProjectionBatchEscalatesOnlyAfterConsecutiveFailureThreshold(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	embedder := &stubEmbedder{vector: make([]float32, 4), err: embedprovider.ErrModelIdentityMismatch}
	const escalateAfter = 3
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), embedder, telemetry, 0, 0, escalateAfter)

	for i := 1; i <= escalateAfter+1; i++ {
		if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
			t.Fatalf("embedProjectionBatch call %d: %v", i, err)
		}
		wantEscalations := 0
		if i >= escalateAfter {
			wantEscalations = i - escalateAfter + 1
		}
		if got := len(telemetry.embedFailureEscalations); got != wantEscalations {
			t.Fatalf("after %d consecutive failing batches: len(embedFailureEscalations) = %d, want %d", i, got, wantEscalations)
		}
	}
	last := telemetry.embedFailureEscalations[len(telemetry.embedFailureEscalations)-1]
	if last.orgID != "org-1" {
		t.Errorf("escalation orgID = %q, want org-1", last.orgID)
	}
	if last.transient {
		t.Error("this failure is PERSISTENT (ErrModelIdentityMismatch); escalation must report transient=false")
	}
}

// TestEmbedProjectionBatchEscalationResetsOnSuccessfulBatch pins that the
// consecutive-failure streak is PER-ORGANIZATION and resets on a successful
// batch: after crossing the threshold, one successful batch must un-escalate
// so a resolved outage does not keep firing forever, and re-crossing after
// recovery requires reaching the threshold again from zero.
func TestEmbedProjectionBatchEscalationResetsOnSuccessfulBatch(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	failing := &stubEmbedder{vector: make([]float32, 4), err: errors.New("boom")}
	const escalateAfter = 2
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), failing, telemetry, 0, 0, escalateAfter)

	// Cross the threshold.
	for i := 0; i < escalateAfter; i++ {
		if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
			t.Fatalf("embedProjectionBatch: %v", err)
		}
	}
	if len(telemetry.embedFailureEscalations) != 1 {
		t.Fatalf("len(embedFailureEscalations) = %d, want 1 after reaching the threshold", len(telemetry.embedFailureEscalations))
	}

	// A successful batch (swap in a working embedder) must reset the streak.
	adapter.embedder = &stubEmbedder{vector: make([]float32, 4)}
	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
		t.Fatalf("embedProjectionBatch (recovery): %v", err)
	}

	// One more failure alone must NOT re-escalate -- the streak restarted at zero.
	adapter.embedder = failing
	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-1")); err != nil {
		t.Fatalf("embedProjectionBatch: %v", err)
	}
	if len(telemetry.embedFailureEscalations) != 1 {
		t.Fatalf("len(embedFailureEscalations) = %d, want still 1 -- the streak must have reset on the successful batch, not carried over", len(telemetry.embedFailureEscalations))
	}
}

// TestEmbedProjectionBatchEscalationIsPerOrganization pins that the
// consecutive-failure counter is scoped PER ORGANIZATION: a sustained
// failure streak for one org must never contaminate another org's count on
// the SAME shared Adapter instance.
func TestEmbedProjectionBatchEscalationIsPerOrganization(t *testing.T) {
	t.Parallel()
	telemetry := &recordingTelemetry{}
	embedder := &stubEmbedder{vector: make([]float32, 4), err: errors.New("boom")}
	const escalateAfter = 3
	adapter := vectorAdapterWithRetryConfig(t, noopQueryFake(), embedder, telemetry, 0, 0, escalateAfter)

	// org-a fails twice (below threshold); org-b fails once.
	for i := 0; i < 2; i++ {
		if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-a")); err != nil {
			t.Fatalf("embedProjectionBatch(org-a): %v", err)
		}
	}
	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-b")); err != nil {
		t.Fatalf("embedProjectionBatch(org-b): %v", err)
	}
	if len(telemetry.embedFailureEscalations) != 0 {
		t.Fatalf("neither organization reached the threshold yet, got %v", telemetry.embedFailureEscalations)
	}

	// A third org-a failure crosses ITS threshold; org-b's count (1) must be unaffected.
	if err := adapter.embedProjectionBatch(context.Background(), "k", realProjectSubjectBatch("org-a")); err != nil {
		t.Fatalf("embedProjectionBatch(org-a): %v", err)
	}
	if len(telemetry.embedFailureEscalations) != 1 {
		t.Fatalf("len(embedFailureEscalations) = %d, want 1 (org-a only)", len(telemetry.embedFailureEscalations))
	}
	if telemetry.embedFailureEscalations[0].orgID != "org-a" {
		t.Fatalf("escalation orgID = %q, want org-a", telemetry.embedFailureEscalations[0].orgID)
	}
}
