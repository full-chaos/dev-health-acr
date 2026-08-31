package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestDeriveInterpretSeedIsDeterministic is the CHAOS-4631 red-first pin for
// the derivation function itself: chaos4631InterpretSeedFor does not exist
// at all on origin/main (this file fails to compile there), and the design
// (§4.1) requires "an entire turn is reproducible from the question hash
// alone" -- so the SAME (hash, sample) pair must always derive the SAME
// seed, repeatedly, in-process.
func TestDeriveInterpretSeedIsDeterministic(t *testing.T) {
	t.Parallel()
	hash := contextfabric.QuestionHash("Which teams are struggling, and why?")
	first := chaos4631InterpretSeedFor(hash, 2)
	for i := 0; i < 5; i++ {
		if got := chaos4631InterpretSeedFor(hash, 2); got != first {
			t.Fatalf("chaos4631InterpretSeedFor(%q, 2) = %v on repeat %d, want %v (deterministic)", hash, got, i, first)
		}
	}
}

// TestDeriveInterpretSeedVariesAcrossSamples is the design's load-bearing
// property (§4.1: "seed_i = f(stable_question_hash, i)... this is
// load-bearing, not a detail... a shared seed... fakes the very stability
// S2 exists to measure"): for a FIXED question hash, distinct sample
// indices must derive PAIRWISE DISTINCT seeds. A degenerate derivation that
// ignored the sample index entirely (a real, easy-to-introduce bug -- e.g.
// forgetting to write sample into the hash) would pass every other test in
// this file while silently reproducing CHAOS-4622's exact shared-seed flaw.
func TestDeriveInterpretSeedVariesAcrossSamples(t *testing.T) {
	t.Parallel()
	hash := contextfabric.QuestionHash("What are the statuses of the fullchaos team's projects?")
	seen := make(map[int64]int, 5)
	for i := 0; i < 5; i++ {
		seen[chaos4631InterpretSeedFor(hash, i)]++
	}
	if len(seen) != 5 {
		t.Fatalf("chaos4631InterpretSeedFor(hash, 0..4) produced %d distinct seeds, want 5 (all distinct): %#v", len(seen), seen)
	}
}

// TestDeriveInterpretSeedVariesAcrossQuestions guards the OTHER half of the
// same property: two different questions at the same sample index must not
// collide onto the same seed for the corpus's actual acceptance questions
// (a hash-function property, not a mathematical guarantee for arbitrary
// input, but exactly what this ticket's four/five acceptance questions
// need to hold for the measurement to be meaningful).
func TestDeriveInterpretSeedVariesAcrossQuestions(t *testing.T) {
	t.Parallel()
	questions := []string{
		"What is the status of the Dev Health Ops project?",
		"Which teams are struggling, and why?",
		"What's are the project statuses for each team, and what are the main drivers?",
		"What are the project statuses for each team, and what are the main drivers?",
		"What are the statuses of the fullchaos team's projects?",
	}
	seen := make(map[int64]string, len(questions))
	for _, q := range questions {
		seed := chaos4631InterpretSeedFor(contextfabric.QuestionHash(q), 0)
		if prior, ok := seen[seed]; ok {
			t.Fatalf("questions %q and %q collide on seed %v at sample 0", prior, q, seed)
		}
		seen[seed] = q
	}
}

// TestInterpretDecodingConfigCarriesOnlySeed pins chaos4631InterpretDecodingConfig's
// shape directly (temperature is 400-rejected -- see its doc comment for the
// executed repro -- so it must never be added back).
func TestInterpretDecodingConfigCarriesOnlySeed(t *testing.T) {
	t.Parallel()
	config := chaos4631InterpretDecodingConfig(42)
	if len(config) != 1 {
		t.Fatalf("chaos4631InterpretDecodingConfig(42) = %#v, want exactly one key", config)
	}
	if config["seed"] != int64(42) {
		t.Fatalf("chaos4631InterpretDecodingConfig(42)[\"seed\"] = %v, want 42", config["seed"])
	}
}

// TestInterpretQuestionForSampleUsesGivenSampleDerivedSeed is the red-first
// pin for the measurement entry point itself: InterpretQuestionForSample
// does not exist at all on origin/main. Calling it with a non-zero sample
// must derive the SAME seed chaos4631InterpretSeedFor(hash, sample) would
// -- never sample 0's seed, and never a seed that also depends on anything
// but the question hash and the sample index (principal, request ID, etc.
// must NOT perturb it, or "reproducible from the question hash alone"
// would be false).
func TestInterpretQuestionForSampleUsesGivenSampleDerivedSeed(t *testing.T) {
	t.Parallel()
	stub := &generatorStub{interpretation: validInterpretationOutput()}
	runtime := mustRuntime(t, stub, Config{})
	request := validRequest()

	const sample = 3
	if _, _, err := runtime.InterpretQuestionForSample(context.Background(), storage.Principal{OrgID: "org_1"}, request, sample); err != nil {
		t.Fatalf("InterpretQuestionForSample() error = %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %#v, want exactly 1", stub.requests)
	}
	wantSeed := chaos4631InterpretSeedFor(contextfabric.QuestionHash(request.Question), sample)
	config, ok := stub.requests[0].Config.(map[string]any)
	if !ok {
		t.Fatalf("Interpret request Config type = %T, want map[string]any", stub.requests[0].Config)
	}
	if config["seed"] != wantSeed {
		t.Fatalf("Config[seed] = %v, want %v (sample %d's own derived seed, not sample 0's)", config["seed"], wantSeed, sample)
	}

	sampleZeroSeed := chaos4631InterpretSeedFor(contextfabric.QuestionHash(request.Question), 0)
	if wantSeed == sampleZeroSeed {
		t.Fatalf("sample %d derived the SAME seed as sample 0 (%v) -- the corpus fixture question no longer exercises the property under test", sample, wantSeed)
	}
}

// TestInterpretQuestionUsesSampleZero proves InterpretQuestion (the
// production path) is exactly interpretQuestionWithSample at sample=0 --
// not some other constant, and not a value that silently drifts from what
// InterpretQuestionForSample(sample=0) would produce for the identical
// question.
func TestInterpretQuestionUsesSampleZero(t *testing.T) {
	t.Parallel()
	request := validRequest()
	wantSeed := chaos4631InterpretSeedFor(contextfabric.QuestionHash(request.Question), 0)

	viaProduction := &generatorStub{interpretation: validInterpretationOutput()}
	productionRuntime := mustRuntime(t, viaProduction, Config{})
	if _, _, err := productionRuntime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, request); err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}

	viaSample := &generatorStub{interpretation: validInterpretationOutput()}
	sampleRuntime := mustRuntime(t, viaSample, Config{})
	if _, _, err := sampleRuntime.InterpretQuestionForSample(context.Background(), storage.Principal{OrgID: "org_1"}, request, 0); err != nil {
		t.Fatalf("InterpretQuestionForSample(sample=0) error = %v", err)
	}

	productionConfig := viaProduction.requests[0].Config.(map[string]any)
	sampleConfig := viaSample.requests[0].Config.(map[string]any)
	if productionConfig["seed"] != wantSeed || sampleConfig["seed"] != wantSeed {
		t.Fatalf("InterpretQuestion seed = %v, InterpretQuestionForSample(0) seed = %v, want both = %v", productionConfig["seed"], sampleConfig["seed"], wantSeed)
	}
}
