package hosted

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// CHAOS-5380: open.go:705 already hands newContextFabricModelRuntime the
// service's own request.options.Logger -- it was used for one startup WARN
// and then dropped, so nothing it built ever logged through it. This pins
// the whole hop: the config handed to modelprovider.New carries BOTH the
// engine telemetry sink (CHAOS-4355) and that logger.
func TestContextFabricModelConfigCarriesTelemetryAndLogger(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	telemetry := contextfabric.NewSlogEngineTelemetry(logger)
	lookup := envLookup(map[string]string{modelprovider.EnvAPIKey: "sk-test"})

	cfg, err := contextFabricModelConfigFromEnv(lookup, telemetry, logger)
	if err != nil {
		t.Fatalf("contextFabricModelConfigFromEnv() error = %v", err)
	}
	if cfg.Logger != logger {
		t.Fatalf("Config.Logger = %#v, want the service logger open() already holds", cfg.Logger)
	}
	if cfg.Telemetry != telemetry {
		t.Fatalf("Config.Telemetry = %#v, want the shared engine telemetry", cfg.Telemetry)
	}
}
