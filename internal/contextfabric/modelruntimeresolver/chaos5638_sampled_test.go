package modelruntimeresolver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/memorymodelconfig"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelruntimeresolver"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// sampledFakeRuntime is a fakeRuntime that CAN answer a per-sample call, so
// the two arms below differ in exactly one property: whether the resolved
// runtime implements contextfabric.SampledModelRuntime.
type sampledFakeRuntime struct {
	name string
}

func (f *sampledFakeRuntime) InterpretQuestion(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("fake runtime %s answered", f.name)
}

func (f *sampledFakeRuntime) SynthesizeAnswer(context.Context, storage.Principal, contextfabric.SynthesisInput) (contextfabric.SynthesisDraft, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.SynthesisDraft{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("fake runtime %s answered", f.name)
}

func (f *sampledFakeRuntime) InterpretQuestionForSample(_ context.Context, _ storage.Principal, _ contextfabric.InvestigationRequest, sample int) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
	return contextfabric.InterpretedQuestion{}, contextfabric.ModelExecutionReceipt{}, fmt.Errorf("fake runtime %s answered sample %d", f.name, sample)
}

// A RUNTIME THAT CANNOT SAMPLE FAILS TYPED AND LOUD.
//
// The failure this pins is not the error -- it is the SILENCE that an
// unchecked optional-interface assertion would produce. So both halves are
// asserted: the typed error the caller must handle, and the Warn line that
// tells an operator which organization and which concrete runtime need
// attention. A build that returned the error and logged nothing would leave
// the ensemble degrading invisibly, which is the exact shape this package's
// siblings record as having cost them a vanished telemetry event.
func TestAResolvedRuntimeThatCannotSampleFailsTypedAndLogsWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	configs := memorymodelconfig.NewStore(nil)
	resolver := modelruntimeresolver.New(&fakeRuntime{name: "not-sampled"}, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Fatal("Build should not be called for an unconfigured organization")
		return nil, nil
	})
	resolver.Logger = logger

	_, _, err := resolver.InterpretQuestionForSample(context.Background(), storage.Principal{OrgID: "org-unsampled"}, contextfabric.InvestigationRequest{}, 0)
	if !errors.Is(err, modelruntimeresolver.ErrRuntimeNotSampled) {
		t.Fatalf("err = %v, want ErrRuntimeNotSampled", err)
	}
	if !errors.Is(err, modelruntimeresolver.ErrRuntimeNotSampled) || !strings.Contains(err.Error(), "org-unsampled") {
		t.Fatalf("err = %v, want it to name the organization", err)
	}
	// NOT ErrModelUnavailable: the runtime is present and can answer an
	// ordinary interpret call. Conflating the two sends an operator looking
	// for an outage instead of for the wiring.
	if errors.Is(err, contextfabric.ErrModelUnavailable) {
		t.Fatal("a not-sampled runtime reported itself as unavailable")
	}

	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("no Warn line was emitted (buffer %q): %v", buf.String(), err)
	}
	if line["level"] != "WARN" {
		t.Fatalf("level = %v, want WARN", line["level"])
	}
	if line["org_id"] != "org-unsampled" {
		t.Fatalf("org_id = %v, want the organization whose runtime failed", line["org_id"])
	}
	runtimeType, _ := line["runtime_type"].(string)
	if !strings.Contains(runtimeType, "fakeRuntime") {
		t.Fatalf("runtime_type = %q, want the concrete type that failed the assertion", runtimeType)
	}
	// The TYPE, never the value: a runtime holds credentials and clients.
	if strings.Contains(runtimeType, "not-sampled") {
		t.Fatalf("runtime_type = %q leaked the runtime's field values, not just its type", runtimeType)
	}
}

// THE CONTROL. A runtime that CAN sample is delegated to, with its own index,
// and emits no warning -- without this the arm above passes just as well
// against a Resolver that refuses every per-sample call.
func TestAResolvedRuntimeThatCanSampleIsDelegatedTo(t *testing.T) {
	var buf bytes.Buffer
	configs := memorymodelconfig.NewStore(nil)
	resolver := modelruntimeresolver.New(&sampledFakeRuntime{name: "default"}, configs, func(context.Context, contextfabric.ResolvedOrgModelConfig) (contextfabric.ModelRuntime, error) {
		t.Fatal("Build should not be called for an unconfigured organization")
		return nil, nil
	})
	resolver.Logger = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	_, _, err := resolver.InterpretQuestionForSample(context.Background(), storage.Principal{OrgID: "org-sampled"}, contextfabric.InvestigationRequest{}, 2)
	if err == nil || !strings.Contains(err.Error(), "answered sample 2") {
		t.Fatalf("err = %v, want the sampled runtime to have answered with its own index", err)
	}
	if errors.Is(err, modelruntimeresolver.ErrRuntimeNotSampled) {
		t.Fatalf("err = %v, want the delegation to have happened", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a successful delegation emitted a warning: %s", buf.String())
	}
}

// The Resolver satisfies the port at compile time, so a composition can wire
// it straight into RuntimeQuestionInterpreter.SampledRuntime.
func TestTheResolverSatisfiesTheSampledPort(t *testing.T) {
	var _ contextfabric.SampledModelRuntime = (*modelruntimeresolver.Resolver)(nil)
}
