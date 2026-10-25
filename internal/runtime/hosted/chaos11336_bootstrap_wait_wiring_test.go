package hosted

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

func TestDeployedGraphTelemetryCountsABootstrapWait(t *testing.T) {
	metrics, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})
	sink, ok := falkorGraphTelemetryWithMetrics(slog.New(slog.NewTextHandler(io.Discard, nil)), metrics).(falkorgraph.BootstrapWaitTelemetry)
	if !ok {
		t.Fatal("the deployed graph sink has no bootstrap wait hook")
	}
	sink.RecordBootstrapWait(context.Background(), falkorgraph.BootstrapWaitBootstrapped, time.Second)
	got := read()
	if got["acr_graph_bootstrap_waits_total{outcome=bootstrapped}"] != 1 || got["acr_graph_bootstrap_wait_seconds{outcome=bootstrapped}"] != 1 || len(got) != 2 {
		t.Fatalf("cells %v, want one bootstrapped counter and one histogram sample", got)
	}
}

func TestDeployedGraphTelemetryWithoutMetricsDropsTheBootstrapWait(t *testing.T) {
	sink := falkorGraphTelemetryWithMetrics(slog.New(slog.NewTextHandler(io.Discard, nil)), nil).(falkorgraph.BootstrapWaitTelemetry)
	sink.RecordBootstrapWait(context.Background(), falkorgraph.BootstrapWaitBootstrapped, time.Second)
}
