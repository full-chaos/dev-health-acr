package hosted

import (
	"context"
	"fmt"
	"log/slog"

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
//
// logger must be non-nil (production passes request.options.Logger,
// validated non-nil by validateBuildRequest before this runs). When no
// provider is configured, it records one startup WARN naming any
// tuning-only variable (ACR_CONTEXT_FABRIC_MODEL_TIMEOUT and siblings)
// that was set anyway, so an operator who set only a tuning knob still
// finds out it had no effect (CHAOS-4986; see modelprovider.Configured's
// doc comment).
// contextFabricModelConfigFromEnv assembles the deployment-default model
// configuration: the environment-derived knobs, plus the two dependencies
// open() already resolved and shares across every model runtime it builds.
//
// Split out as its own pure function (the pattern
// modelruntimeresolver.orgModelProviderConfig documents for the same reason)
// so the field mapping is directly unit-testable without constructing a real
// genkit.Genkit instance -- which is the only way to pin, ahead of a live
// provider, that a dependency actually crosses this hop.
//
// CHAOS-5380: `logger` was already a parameter of the caller and was already
// the service's own request.options.Logger -- it was used for one startup
// WARN and then dropped, so genkitruntime fell back to slog.Default() and
// the decision event (with its attempt fields) missed the collected sink.
func contextFabricModelConfigFromEnv(lookup func(string) (string, bool), telemetry contextfabric.EngineTelemetry, logger *slog.Logger) (modelprovider.Config, error) {
	modelConfig, err := modelprovider.ConfigFromEnv(lookup)
	if err != nil {
		return modelprovider.Config{}, fmt.Errorf("load context fabric model configuration: %w", err)
	}
	modelConfig.Telemetry = telemetry
	modelConfig.Logger = logger
	return modelConfig, nil
}

func newContextFabricModelRuntime(ctx context.Context, lookup func(string) (string, bool), telemetry contextfabric.EngineTelemetry, logger *slog.Logger) (contextfabric.ModelRuntime, error) {
	if !modelprovider.Configured(lookup) {
		if ignored := modelprovider.IgnoredTuningVariables(lookup); len(ignored) > 0 {
			logger.Warn("context fabric model tuning variable set without a provider; ignoring",
				"variables", ignored)
		}
		return nil, nil
	}
	modelConfig, err := contextFabricModelConfigFromEnv(lookup, telemetry, logger)
	if err != nil {
		return nil, err
	}
	runtime, err := modelprovider.New(ctx, modelConfig)
	if err != nil {
		return nil, fmt.Errorf("initialize context fabric model runtime: %w", err)
	}
	return runtime, nil
}
