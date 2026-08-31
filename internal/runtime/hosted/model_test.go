package hosted

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

// TestNewContextFabricModelRuntime_keepsTheCleanFiveOhThreeWithoutACredential
// is the CHAOS-3770 regression guard for the CHAOS-3755 behavior: wiring a
// model provider in must not change what a deployment that did not
// configure one does. Composition yields no model runtime, which
// RuntimeQuestionInterpreter and RuntimeAnswerSynthesizer turn into
// ErrModelUnavailable per request -- and internal/api maps that to a clean
// 503 (see context_fabric_routes_test.go's status table).
func TestNewContextFabricModelRuntime_keepsTheCleanFiveOhThreeWithoutACredential(t *testing.T) {
	// Given a Context Fabric deployment with reads enabled and a graph
	// backend configured, but no model provider -- and an ambient OpenAI
	// credential that must not be treated as opting in.
	lookup := envLookup(map[string]string{
		"ACR_CONTEXT_FABRIC_GRAPH_READS_ENABLED": "true",
		"ACR_CONTEXT_FABRIC_FALKOR_ADDR":         "falkordb:6379",
		"OPENAI_API_KEY":                         "sk-ambient-must-not-opt-in",
	})

	// When
	modelRuntime, err := newContextFabricModelRuntime(context.Background(), lookup, nil)

	// Then
	if err != nil {
		t.Fatalf("newContextFabricModelRuntime() = %v, want no failure over an unconfigured optional dependency", err)
	}
	if modelRuntime != nil {
		t.Fatalf("model runtime = %#v, want nil so the investigation endpoint keeps its clean 503", modelRuntime)
	}
	// And the nil runtime still produces the classification the endpoint
	// turns into 503, rather than a panic.
	_, _, interpretErr := contextfabric.RuntimeQuestionInterpreter{Runtime: modelRuntime}.
		Interpret(context.Background(), storage.Principal{OrgID: "org_test"}, contextfabric.InvestigationRequest{})
	if !errors.Is(interpretErr, contextfabric.ErrModelUnavailable) {
		t.Fatalf("Interpret() = %v, want ErrModelUnavailable", interpretErr)
	}
	_, synthesizeErr := contextfabric.RuntimeAnswerSynthesizer{Runtime: modelRuntime}.
		Synthesize(context.Background(), storage.Principal{OrgID: "org_test"}, contextfabric.SynthesisInput{})
	if !errors.Is(synthesizeErr, contextfabric.ErrModelUnavailable) {
		t.Fatalf("Synthesize() = %v, want ErrModelUnavailable", synthesizeErr)
	}
}

func TestNewContextFabricModelRuntime_buildsAUsableRuntimeWhenConfigured(t *testing.T) {
	// Given a configured provider.
	lookup := envLookup(map[string]string{modelprovider.EnvAPIKey: "sk-test"})

	// When
	modelRuntime, err := newContextFabricModelRuntime(context.Background(), lookup, nil)

	// Then the returned interface must be genuinely non-nil, not a typed
	// nil: RuntimeQuestionInterpreter only degrades on a nil interface, so a
	// typed nil would panic on the first request instead of 503ing.
	if err != nil {
		t.Fatal(err)
	}
	if modelRuntime == nil {
		t.Fatal("model runtime = nil for a configured provider")
	}
}

// TestNewContextFabricModelRuntime_failsCompositionOnAModelOnlyPartialConfig
// is the CHAOS-3770 F5 probe at the composition boundary: an operator who
// sets ONLY the model name (no credential, no base URL) must get a startup
// failure naming the missing credential, not the CHAOS-3755 clean 503 --
// that behavior is reserved for a deployment that configured no model
// provider at all.
func TestNewContextFabricModelRuntime_failsCompositionOnAModelOnlyPartialConfig(t *testing.T) {
	// Given an operator who set only the model name.
	lookup := envLookup(map[string]string{modelprovider.EnvModel: "gpt-5-mini"})

	// When
	modelRuntime, err := newContextFabricModelRuntime(context.Background(), lookup, nil)

	// Then startup fails loudly, naming the missing credential variable --
	// not a silent nil runtime that 503s forever without ever telling the
	// operator their ACR_CONTEXT_FABRIC_MODEL setting was ignored.
	if err == nil {
		t.Fatal("newContextFabricModelRuntime() = nil error for a model-only partial configuration")
	}
	if modelRuntime != nil {
		t.Fatal("newContextFabricModelRuntime() returned a runtime alongside an error")
	}
	if !strings.Contains(err.Error(), modelprovider.EnvAPIKey) {
		t.Fatalf("err = %q, want it to name the missing credential variable %s", err, modelprovider.EnvAPIKey)
	}
}

func TestNewContextFabricModelRuntime_failsCompositionOnAMisconfiguredProvider(t *testing.T) {
	// Given an operator who asked for a model provider and mis-specified it.
	lookup := envLookup(map[string]string{
		modelprovider.EnvAPIKey:  "sk-test",
		modelprovider.EnvTimeout: "10m",
	})

	// When
	modelRuntime, err := newContextFabricModelRuntime(context.Background(), lookup, nil)

	// Then startup fails loudly rather than degrading silently to 503 --
	// the opposite of the unconfigured case above.
	if err == nil {
		t.Fatal("newContextFabricModelRuntime() = nil error for an invalid model configuration")
	}
	if modelRuntime != nil {
		t.Fatal("newContextFabricModelRuntime() returned a runtime alongside an error")
	}
	if !strings.Contains(err.Error(), modelprovider.EnvTimeout) {
		t.Fatalf("err = %q, want it to name the offending environment variable", err)
	}
}
