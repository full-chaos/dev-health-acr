package hosted

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

// The graph sink the API process builds must count a query-embed fallback on
// the exported counter, by closed cause, and still warn on the configured
// logger.
func TestDeployedGraphTelemetryCountsAQueryEmbedFallbackByCause(t *testing.T) {
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, nil))
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})

	sink, ok := falkorGraphTelemetryWithMetrics(logger, instruments).(falkorgraph.QueryEmbedFallbackTelemetry)
	if !ok {
		t.Fatal("the deployed graph sink has no query embed fallback hook")
	}
	sink.RecordQueryEmbedFallback(context.Background(), "org-1", "timeout")
	sink.RecordQueryEmbedFallback(context.Background(), "org-1", "not-a-cause")

	if got := read(); got["acr_query_embed_fallbacks_total{cause=timeout}"] != 1 || got["acr_query_embed_fallbacks_total{cause=other}"] != 1 || len(got) != 2 {
		t.Fatalf("cells %v, want one timeout and one other (an unknown cause never widens the series)", got)
	}
	if !strings.Contains(logged.String(), `"level":"WARN"`) || !strings.Contains(logged.String(), `"cause":"timeout"`) {
		t.Fatalf("the configured logger did not get the WARN: %s", logged.String())
	}
}

func TestDeployedGraphTelemetryWithoutMetricsStaysLogOnly(t *testing.T) {
	var logged bytes.Buffer
	sink := falkorGraphTelemetryWithMetrics(slog.New(slog.NewJSONHandler(&logged, nil)), nil).(falkorgraph.QueryEmbedFallbackTelemetry)
	sink.RecordQueryEmbedFallback(context.Background(), "org-1", "error")
	if !strings.Contains(logged.String(), `"cause":"error"`) {
		t.Fatalf("no WARN without metrics: %s", logged.String())
	}
}
