package hosted

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
)

// EnvSynthesisResynthesisAttempts (CHAOS-5655) bounds how many times
// SynthesizeAnswer re-samples the SAME synthesis prompt after the validator
// rejects a draft, before falling through to the fallback leg (if any) and
// the existing fail-closed 422 -- see
// genkitruntime.Config.MaxSynthesisResynthesisAttempts's own doc comment for
// the mechanism.
//
// It lives here, in hosted composition, rather than as an
// ACR_CONTEXT_FABRIC_MODEL_* variable read by modelprovider.ConfigFromEnv,
// and deliberately does NOT follow that function's fail-composition-on-
// malformed contract (see envInt in modelprovider/config.go): this knob
// tunes SynthesizeAnswer's OWN re-sampling policy, not which provider or
// model composition talks to, so a malformed value should never be able to
// take the whole model runtime down at startup. A missing or malformed
// value instead falls back to the documented default and (for a malformed
// one) logs exactly one startup WARN naming the bad value, mirroring the
// "an operator who mis-set a tuning-only variable still finds out" posture
// newContextFabricModelRuntime already applies to an unconfigured provider.
const EnvSynthesisResynthesisAttempts = "ACR_CONTEXT_FABRIC_SYNTHESIS_MAX_RESYNTHESIS_ATTEMPTS"

// synthesisResynthesisAttemptsFromEnv reads EnvSynthesisResynthesisAttempts.
// Absent or blank returns the default silently (an operator who never
// touched this knob gets no line about it, matching every other
// zero-value-defaults-silently knob in this composition). Present but not a
// positive integer within genkitruntime's own construction-time ceiling
// returns the default too, but logs one WARN -- an operator who set the
// variable and got the value wrong must find out, even though the service
// still starts.
func synthesisResynthesisAttemptsFromEnv(lookup func(string) (string, bool), logger *slog.Logger) int {
	raw, ok := lookup(EnvSynthesisResynthesisAttempts)
	value := strings.TrimSpace(raw)
	if !ok || value == "" {
		return genkitruntime.DefaultMaxSynthesisResynthesisAttempts
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 || parsed > genkitruntime.MaxSynthesisResynthesisAttemptsCeiling {
		logger.Warn("context fabric synthesis resynthesis attempts malformed, using default",
			"variable", EnvSynthesisResynthesisAttempts,
			"value", contextfabric.SanitizeLogAttr(value),
			"default", genkitruntime.DefaultMaxSynthesisResynthesisAttempts)
		return genkitruntime.DefaultMaxSynthesisResynthesisAttempts
	}
	return parsed
}

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
	// CHAOS-5655: read here rather than inside modelprovider.ConfigFromEnv --
	// see EnvSynthesisResynthesisAttempts's own doc comment for why this
	// knob's malformed-value policy deliberately differs from every
	// ACR_CONTEXT_FABRIC_MODEL_* variable that function owns.
	modelConfig.MaxSynthesisResynthesisAttempts = synthesisResynthesisAttemptsFromEnv(lookup, logger)
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
