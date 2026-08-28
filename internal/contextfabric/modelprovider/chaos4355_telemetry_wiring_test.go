package modelprovider

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// TestRuntimeConfigPropagatesTelemetry is the CHAOS-4355 follow-up wiring
// proof: RecordModelRowsStripped is useless in production if
// genkitruntime.Config.Telemetry stays nil no matter what a caller sets on
// modelprovider.Config -- the strip itself would still run (it is
// unconditional), but the operator-visible counter this ticket's standing
// telemetry order exists for would never fire. This pins that both
// runtimeConfig (the fallback runtime) and runtimeConfigWithPhrasing (the
// primary runtime) copy Config.Telemetry straight through, unconditionally,
// exactly like every other tuning field on the same struct literal.
func TestRuntimeConfigPropagatesTelemetry(t *testing.T) {
	telemetry := contextfabric.NewSlogEngineTelemetry(nil)
	cfg := Config{Provider: "test-provider", Model: "test-model", Telemetry: telemetry}

	if got := runtimeConfig(nil, cfg, cfg.Model, nil); got.Telemetry != telemetry {
		t.Fatalf("runtimeConfig().Telemetry = %#v, want %#v", got.Telemetry, telemetry)
	}
	if got := runtimeConfigWithPhrasing(nil, cfg, cfg.Model, nil); got.Telemetry != telemetry {
		t.Fatalf("runtimeConfigWithPhrasing().Telemetry = %#v, want %#v", got.Telemetry, telemetry)
	}
}

// TestRuntimeConfigLeavesTelemetryNilWhenConfigDoesNotSetIt is the negative
// half: an existing caller that never sets Config.Telemetry (every call
// site before this ticket) must keep getting a nil genkitruntime.Config.Telemetry
// -- exactly pre-CHAOS-4355-follow-up behavior, never a package-internal
// default silently substituted in.
func TestRuntimeConfigLeavesTelemetryNilWhenConfigDoesNotSetIt(t *testing.T) {
	cfg := Config{Provider: "test-provider", Model: "test-model"}
	if got := runtimeConfig(nil, cfg, cfg.Model, nil); got.Telemetry != nil {
		t.Fatalf("runtimeConfig().Telemetry = %#v, want nil", got.Telemetry)
	}
}
