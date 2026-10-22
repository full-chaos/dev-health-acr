package hosted

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

// The skip of a service disclosure is reported from a function with no
// telemetry handle, so the deployed wiring must point it at the configured
// logger and the metrics: the Warn must reach the logger the process exports,
// not the default one, and the counter must move.
func TestDeployedTelemetryRoutesASkippedDisclosureToTheConfiguredLoggerAndCounter(t *testing.T) {
	t.Cleanup(contextfabric.SetLimitationDisclosureSkipSink(nil, nil))
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{})

	contextFabricEngineTelemetry(Options{Logger: logger, Metrics: instruments})
	contextfabric.RecordLimitationDisclosureSkipped("Some sentence that is not a recognised disclosure.", 100)

	line := logged.String()
	for _, fragment := range []string{`"msg":"context fabric limitation disclosure skipped"`, `"level":"WARN"`, `"cf_limitations_len":100`} {
		if !strings.Contains(line, fragment) {
			t.Errorf("configured logger lacks %s; got %s", fragment, line)
		}
	}
	if strings.Contains(line, "Some sentence") {
		t.Errorf("the sentence text reached the log: %s", line)
	}
	if got := read(); got["acr_limitation_disclosure_skipped_total{}"] != 1 || len(got) != 1 {
		t.Errorf("cells %v, want exactly acr_limitation_disclosure_skipped_total=1", got)
	}
}

// Without metrics the Warn still goes to the configured logger.
func TestDeployedTelemetryWithoutMetricsStillWarnsOnTheConfiguredLogger(t *testing.T) {
	t.Cleanup(contextfabric.SetLimitationDisclosureSkipSink(nil, nil))
	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo}))

	contextFabricEngineTelemetry(Options{Logger: logger})
	contextfabric.RecordLimitationDisclosureSkipped("Some sentence that is not a recognised disclosure.", 100)

	if !strings.Contains(logged.String(), "context fabric limitation disclosure skipped") {
		t.Errorf("no skip Warn on the configured logger without metrics; got %q", logged.String())
	}
}
