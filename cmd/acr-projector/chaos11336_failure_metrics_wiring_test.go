package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

// The projector's coordinator observer must count a failed tick on the
// exported counter by class, and count nothing for a successful one.
func TestProjectorObserverCountsAFailedTickByClass(t *testing.T) {
	t.Parallel()
	metrics, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	observer := projectionObserver(discardLogger(), metrics)

	observer.ObserveProjectionOutcome(projectionrun.Outcome{OrgID: "org-1", Source: "s"})
	if got := read(); len(got) != 0 {
		t.Fatalf("a successful tick moved %v", got)
	}
	observer.ObserveProjectionOutcome(projectionrun.Outcome{OrgID: "org-1", Source: "s", Err: fmt.Errorf("x: %w", contextfabric.ErrUnavailable)})
	if got := read(); got["acr_projection_pair_failures_total{failure_class=dependency_unavailable}"] != 1 || len(got) != 1 {
		t.Fatalf("cells %v, want exactly one dependency_unavailable", got)
	}
}

func TestProjectorObserverWithoutMetricsIsLogOnly(t *testing.T) {
	t.Parallel()
	projectionObserver(discardLogger(), nil).ObserveProjectionOutcome(projectionrun.Outcome{Err: fmt.Errorf("x")})
}

// The graph sink the projector builds must count a bootstrap wait by outcome
// and its duration on the histogram.
func TestProjectorGraphTelemetryCountsABootstrapWait(t *testing.T) {
	t.Parallel()
	metrics, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	sink, ok := falkorGraphTelemetryWithMetrics(discardLogger(), metrics).(falkorgraph.BootstrapWaitTelemetry)
	if !ok {
		t.Fatal("the projector graph sink has no bootstrap wait hook")
	}
	sink.RecordBootstrapWait(context.Background(), falkorgraph.BootstrapWaitTookOver, time.Second)
	got := read()
	if got["acr_graph_bootstrap_waits_total{outcome=took_over}"] != 1 || got["acr_graph_bootstrap_wait_seconds{outcome=took_over}"] != 1 || len(got) != 2 {
		t.Fatalf("cells %v, want one took_over counter and one histogram sample", got)
	}
}
