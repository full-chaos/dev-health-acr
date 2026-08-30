package genkitruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/genkit"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestInterpretQuestionAppliesDeterministicDecodingConfig is the CHAOS-4622
// red-first pin: on origin/main, generationRequest had no Config field at
// all (this file fails to compile there -- see the handoff for the exact
// red command), so InterpretQuestion could not carry any decoding
// parameters to the generator. On this branch it must forward the named
// interpretDecodingConfig constant on every Interpret call, unchanged by
// question content, principal, or outcome.
func TestInterpretQuestionAppliesDeterministicDecodingConfig(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, stub, Config{})

	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %#v, want exactly 1", stub.requests)
	}
	if !reflect.DeepEqual(stub.requests[0].Config, interpretDecodingConfig) {
		t.Fatalf("Interpret request Config = %#v, want %#v", stub.requests[0].Config, interpretDecodingConfig)
	}
	config, ok := stub.requests[0].Config.(map[string]any)
	if !ok {
		t.Fatalf("Interpret request Config type = %T, want map[string]any", stub.requests[0].Config)
	}
	// No "temperature" key: an EXECUTED repro against the real provider
	// (see interpretDecodingConfig's doc comment) showed the deployed
	// model family rejects a non-default temperature with a 400 -- seed is
	// the only decoding parameter this change applies.
	if len(config) != 1 {
		t.Fatalf("Config = %#v, want exactly the seed key", config)
	}
	if config["seed"] != chaos4622InterpretSeed {
		t.Fatalf("Config[seed] = %v, want %v", config["seed"], chaos4622InterpretSeed)
	}
}

// TestSDKGeneratorInterpretForwardsDecodingConfigToGenkit proves the
// forwarding survives the REAL sdkGenerator/genkit boundary, not just the
// genkitruntime-internal generationRequest struct: a genkit.DefineModel
// handler inspects the *ai.ModelRequest it actually receives, which is
// genkit's own copy built from whatever ai.WithConfig(...) was passed
// (ai/option.go: configOptions.applyGenerate sets opts.Config = o.Config
// verbatim; that becomes ModelRequest.Config). If sdkGenerator.Interpret
// ever stopped passing ai.WithConfig(request.Config), this model would see
// a nil Config and the test would fail -- red on the pre-CHAOS-4622 sdkGenerator.Interpret shown in
// the PR diff (it never called ai.WithConfig at all).
func TestSDKGeneratorInterpretForwardsDecodingConfigToGenkit(t *testing.T) {
	ctx := context.Background()
	g := genkit.Init(ctx)
	modelSupports := &ai.ModelSupports{
		Constrained: ai.ConstrainedSupportAll,
		SystemRole:  true,
		Multiturn:   true,
		Output:      []string{"json"},
	}
	var observedConfig any
	genkit.DefineModel(g, "test/chaos-4622-decoding", &ai.ModelOptions{
		Label:    "CHAOS-4622 decoding config test model",
		Supports: modelSupports,
	}, func(_ context.Context, request *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
		observedConfig = request.Config
		encoded, err := json.Marshal(validInterpretationOutput())
		if err != nil {
			t.Fatal(err)
		}
		return &ai.ModelResponse{
			Message:      &ai.Message{Role: ai.RoleModel, Content: []*ai.Part{ai.NewJSONPart(string(encoded))}},
			FinishReason: ai.FinishReasonStop,
			Usage:        &ai.GenerationUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		}, nil
	})
	runtime, err := New(Config{
		Genkit: g, Provider: "test", Model: "test/chaos-4622-decoding", ModelVersion: "test-v1",
		Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, _, err := runtime.InterpretQuestion(ctx, storage.Principal{OrgID: "org_1"}, validRequest()); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	config, ok := observedConfig.(map[string]any)
	if !ok {
		t.Fatalf("model observed Config type = %T, want map[string]any (got %#v)", observedConfig, observedConfig)
	}
	if len(config) != 1 {
		t.Fatalf("model observed Config = %#v, want exactly the seed key", config)
	}
	if config["seed"] != chaos4622InterpretSeed {
		t.Fatalf("model observed Config[seed] = %v, want %v", config["seed"], chaos4622InterpretSeed)
	}
}
