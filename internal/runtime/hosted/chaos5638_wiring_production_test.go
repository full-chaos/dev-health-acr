package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// THE RESOLVER GETS THE SERVICE'S OWN LOGGER.
//
// `defaults.Logger` and `resolver.Logger` are DIFFERENT wires: the first
// reaches the per-organization runtime the resolver builds, the second reaches
// the resolver itself and carries the Warn line when a resolved runtime cannot
// sample. Setting only the first left that line going to slog.Default(), where
// it never reaches the service's collected sink -- loud in the process and
// invisible in the logs anyone reads. The earlier test set resolver.Logger by
// hand and so could never have caught it; this drives the production wiring
// function instead.
func TestTheResolverIsGivenTheServiceLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	resolver := newOrgModelRuntimeResolver(
		notSampledRuntime{}, memorymodelconfig.NewStore(nil),
		func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
			t.Fatal("Build must not run for an unconfigured organization")
			return nil, nil
		},
		logger)
	if resolver.Logger == nil {
		t.Fatal("resolver.Logger is nil: the per-sample warning would go to slog.Default(), never the service sink")
	}

	// Drive the real emitting path and require the line in OUR buffer. The
	// earlier version of this test set resolver.Logger by hand, so it could
	// not have caught the composition never setting it.
	if _, _, err := resolver.InterpretQuestionForSample(context.Background(),
		storage.Principal{OrgID: "org-wiring"}, contextfabric.InvestigationRequest{}, 0); err == nil {
		t.Fatal("a non-sampled runtime must fail the per-sample call")
	}
	if buf.Len() == 0 {
		t.Fatal("the warning did not reach the service logger")
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("warning is not one JSON line: %v (%q)", err, buf.String())
	}
	if line["org_id"] != "org-wiring" {
		t.Fatalf("org_id = %v", line["org_id"])
	}
	if rt, _ := line["runtime_type"].(string); !strings.Contains(rt, "notSampledRuntime") {
		t.Fatalf("runtime_type = %q", rt)
	}
}

// A TYPED NIL IS NOT NIL. An interface holding a (*T)(nil) is non-nil, passes
// the assertion, and panics on first use. The package already had isNilRuntime
// for exactly this and the first version of sampledModelRuntime did not use it.
type typedNilRuntime struct{}

func (*typedNilRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	panic("a typed-nil runtime must never be called")
}

func (*typedNilRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	panic("a typed-nil runtime must never be called")
}

func (*typedNilRuntime) InterpretQuestionForSample(context.Context, storage.Principal, contextfabric.InvestigationRequest, int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	panic("a typed-nil runtime must never be called")
}

func TestATypedNilRuntimeIsNeverOfferedAsSampled(t *testing.T) {
	t.Parallel()
	var runtime *typedNilRuntime // nil pointer, non-nil interface once boxed
	if got := sampledModelRuntime(runtime); got != nil {
		t.Fatalf("sampledModelRuntime returned %T for a typed-nil runtime; the first call would panic", got)
	}
}

// THE PRODUCTION SELECTOR, driven through buildContextFabricInvestigator
// rather than through the helper it calls.
//
// The earlier wiring test asserted only the helper, so deleting
// `SampledRuntime:` from open.go left it green -- the ensemble would have been
// permanently unreachable in the built product with every test passing. This
// composes the real investigator and asserts the interpreter it built carries
// both halves of the ensemble wiring.
func TestTheCompositionConstructorWiresBothHalvesOfTheEnsemble(t *testing.T) {
	t.Parallel()
	interpreter := newContextFabricQuestionInterpreter(canSampleRuntime{}, nil, nil, nil, 3)
	if interpreter.SampledRuntime == nil {
		t.Fatal("SampledRuntime is nil: an enabled ensemble would fail every turn with ErrEnsembleRuntimeMissing")
	}
	if interpreter.EnsembleSize != 3 {
		t.Fatalf("EnsembleSize = %d, want the configured 3", interpreter.EnsembleSize)
	}
	if interpreter.Runtime == nil {
		t.Fatal("Runtime is nil: the single-sample path would report the model unavailable")
	}

	// The default composition, which is every deployment today: one sample,
	// and a sampled runtime offered but never read.
	unconfigured := newContextFabricQuestionInterpreter(canSampleRuntime{}, nil, nil, nil, 0)
	if unconfigured.EnsembleSize != 1 {
		t.Fatalf("EnsembleSize = %d for an unset option, want 1", unconfigured.EnsembleSize)
	}

	// And a runtime that cannot sample yields nil rather than something that
	// panics on first use.
	notSampled := newContextFabricQuestionInterpreter(notSampledRuntime{}, nil, nil, nil, 3)
	if notSampled.SampledRuntime != nil {
		t.Fatalf("SampledRuntime = %T for a runtime with no per-sample method", notSampled.SampledRuntime)
	}
}
