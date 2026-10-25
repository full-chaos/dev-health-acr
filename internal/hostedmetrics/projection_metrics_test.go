package hostedmetrics_test

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

func TestProjectionPairFailureCountsByClassAndBucketsTheRest(t *testing.T) {
	ctx := context.Background()
	in, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	in.ProjectionPairFailure(ctx, "rebuild_required")
	in.ProjectionPairFailure(ctx, "rebuild_required")
	in.ProjectionPairFailure(ctx, "org-123 raw text")
	got := read()
	if got["acr_projection_pair_failures_total{failure_class=rebuild_required}"] != 2 || got["acr_projection_pair_failures_total{failure_class=other}"] != 1 || len(got) != 2 {
		t.Fatalf("cells %v", got)
	}
}

func TestGraphBootstrapWaitCountsAndTimesByOutcome(t *testing.T) {
	ctx := context.Background()
	in, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	in.GraphBootstrapWait(ctx, "bootstrapped", time.Second)
	in.GraphBootstrapWait(ctx, "ctx_done", time.Second)
	got := read()
	for _, cell := range []string{
		"acr_graph_bootstrap_waits_total{outcome=bootstrapped}", "acr_graph_bootstrap_wait_seconds{outcome=bootstrapped}",
		"acr_graph_bootstrap_waits_total{outcome=other}", "acr_graph_bootstrap_wait_seconds{outcome=other}",
	} {
		if got[cell] != 1 {
			t.Fatalf("%s = %d, cells %v", cell, got[cell], got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("cells %v", got)
	}
}

func TestProjectionMetricsAreNilSafe(t *testing.T) {
	var in *hostedmetrics.Instruments
	in.ProjectionPairFailure(context.Background(), "canceled")
	in.GraphBootstrapWait(context.Background(), "took_over", time.Second)
}
