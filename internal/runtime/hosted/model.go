package hosted

import (
	"context"
	"fmt"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// newContextFabricModelRuntime builds the Context Fabric model runtime from
// the environment, or returns (nil, nil) when no model provider is
// configured (CHAOS-3770).
//
// Returning a nil runtime is a supported, deliberate state, not a failure:
// contextfabric.RuntimeQuestionInterpreter and RuntimeAnswerSynthesizer
// both degrade to ErrModelUnavailable per request when their Runtime field
// is nil, so the investigation endpoint stays registered, authorized and
// audited while answering a clean 503 for every request -- the CHAOS-3755
// behavior, preserved bit for bit for any deployment that has not opted
// into a provider. This mirrors the "an unset optional dependency must
// never fail closed" convention the graph and canonical-fact layers use.
//
// A configuration that IS present but invalid (bad URL, missing credential
// on the default endpoint, unreachable secret file) fails composition
// instead: an operator who asked for a model provider and mis-specified it
// must find out at startup, not one 503 at a time.
//
// lookup is injected rather than hard-wired to os.LookupEnv so the
// composition gate is directly testable; production passes os.LookupEnv.
//
// telemetry (CHAOS-4355 follow-up) is threaded onto modelConfig so the
// deployment-default genkitruntime.Runtime this builds can report
// RecordModelRowsStripped through the SAME sink every other engine
// telemetry signal for this investigation uses -- open()'s own
// engineTelemetry, never a second, independently-resolved instance. A nil
// telemetry (every existing caller before this ticket) is exactly
// pre-CHAOS-4355 behavior.
func newContextFabricModelRuntime(ctx context.Context, lookup func(string) (string, bool), telemetry contextfabric.EngineTelemetry) (contextfabric.ModelRuntime, error) {
	if !modelprovider.Configured(lookup) {
		return nil, nil
	}
	modelConfig, err := modelprovider.ConfigFromEnv(lookup)
	if err != nil {
		return nil, fmt.Errorf("load context fabric model configuration: %w", err)
	}
	modelConfig.Telemetry = telemetry
	runtime, err := modelprovider.New(ctx, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("initialize context fabric model runtime: %w", err)
	}
	return runtime, nil
}
