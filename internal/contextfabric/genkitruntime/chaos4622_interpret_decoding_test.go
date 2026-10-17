package genkitruntime

import (
	"context"
	"reflect"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestInterpretQuestionAppliesDeterministicDecodingConfig is the CHAOS-4622
// red-first pin, RE-POINTED for CHAOS-4631 (design doc §9 slice table: S1
// "supersedes CHAOS-4622's hotfix constant with the derived-seed scheme,
// re-point its tests rather than deleting"): on origin/main before
// CHAOS-4622, generationRequest had no Config field at all, so
// InterpretQuestion could not carry any decoding parameters to the
// generator. CHAOS-4622 fixed that with ONE constant seed for every
// question; this test now pins CHAOS-4631's replacement -- the seed must be
// the one chaos4631InterpretSeedFor derives from THIS question's own hash
// at sample 0, not a fixed constant -- unchanged by principal or outcome.
func TestInterpretQuestionAppliesDeterministicDecodingConfig(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, stub, Config{})
	request := validRequest()

	if _, _, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %#v, want exactly 1", stub.requests)
	}
	wantSeed := chaos4631InterpretSeedFor(contextfabric.QuestionHash(request.Question), 0)
	wantConfig := chaos4631InterpretDecodingConfig(wantSeed)
	if !reflect.DeepEqual(stub.requests[0].Config, wantConfig) {
		t.Fatalf("Interpret request Config = %#v, want %#v", stub.requests[0].Config, wantConfig)
	}
	config, ok := stub.requests[0].Config.(map[string]any)
	if !ok {
		t.Fatalf("Interpret request Config type = %T, want map[string]any", stub.requests[0].Config)
	}
	// No "temperature" key: an EXECUTED repro against the real provider
	// (see chaos4631InterpretDecodingConfig's doc comment) showed the
	// deployed model family rejects a non-default temperature with a 400 --
	// seed is the only decoding parameter this change applies.
	if len(config) != 1 {
		t.Fatalf("Config = %#v, want exactly the seed key", config)
	}
	if config["seed"] != wantSeed {
		t.Fatalf("Config[seed] = %v, want %v", config["seed"], wantSeed)
	}
}
