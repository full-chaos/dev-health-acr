package contextfabric

import (
	"context"
	"errors"
	"reflect"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const callerByteOption = 8192

func investigateWithCallerByteOption(t *testing.T, option int, hook func(InvestigationResult) InvestigationResult) (InvestigationResult, error) {
	t.Helper()
	result, _, _, err := investigateWithCallerByteOptionCounted(t, option, hook)
	return result, err
}

func investigateWithCallerByteOptionCounted(t *testing.T, option int, hook func(InvestigationResult) InvestigationResult) (InvestigationResult, int, *recordingTelemetry, error) {
	t.Helper()
	calls := 0
	telemetry := &recordingTelemetry{}
	engine := chaos6558Engine(t, &calls, telemetry, chaos6558Shape{maxBytes: chaos6558MaxBytes})
	engine.servedLateHook = hook
	request := chaos6558Request()
	request.Options.MaxSerializedBytes = option
	result, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	return result, calls, telemetry, err
}

// The caller's byte option is the target the narrowing levers shed toward, and
// the service ceiling is the only refusal line. Prod's shape cannot be cut to
// the smallest option a caller may ask for, so it is served under the service
// ceiling instead of refused at the option.
func TestAnAnswerThatCannotBeCutToTheCallersByteOptionIsServedUnderTheServiceCeiling(t *testing.T) {
	result, err := investigateWithCallerByteOption(t, callerByteOption, nil)
	if err != nil {
		var refusal AnswerBudgetRefusal
		if errors.As(err, &refusal) {
			t.Fatalf("refused (%s) at %d bytes against %d: the caller's option is not the refusal line", refusal.Overrun, refusal.MeasuredBytes, refusal.MaxSerializedBytes)
		}
		t.Fatalf("Investigate() error = %v", err)
	}
	got := servedBytes(t, result)
	if got <= callerByteOption || got > chaos6558MaxBytes {
		t.Fatalf("served %d bytes; the row needs an answer over the option (%d) and under the service ceiling (%d)", got, callerByteOption, chaos6558MaxBytes)
	}
	if result.AnswerPlan == nil || result.AnswerPlan.Budget.MaxSerializedBytes != chaos6558MaxBytes {
		t.Fatalf("plan budget = %+v, want the service ceiling %d the answer was fitted to", result.AnswerPlan, chaos6558MaxBytes)
	}
}

// Past the service ceiling the refusal stands and names the ceiling.
func TestAnAnswerOverTheServiceCeilingIsRefusedWhateverTheCallersByteOption(t *testing.T) {
	_, err := investigateWithCallerByteOption(t, callerByteOption, lateBytes(60000))
	var refusal AnswerBudgetRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a budget refusal", err)
	}
	if refusal.Overrun != contractsv1.ContextFabricBudgetOverrunBytes || refusal.MaxSerializedBytes != chaos6558MaxBytes {
		t.Fatalf("refusal = %+v, want a bytes refusal naming the service ceiling %d", refusal, chaos6558MaxBytes)
	}
}

// The MCP sidecar asks for the service ceiling as the body bound and names its
// answer budget as the shedding target. For a result that needs shedding the
// stored canonical is the document the same request made with the budget as
// its only knob produced before the split: nothing about MCP shedding moved.
func TestTheBodyBoundAndTheSheddingTargetAsTwoKnobsServeTheCanonicalOneKnobServed(t *testing.T) {
	const target = 40000
	one, err := investigateWithCallerByteOption(t, target, nil)
	if err != nil {
		t.Fatalf("one knob: %v", err)
	}
	calls := 0
	engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, chaos6558Shape{maxBytes: chaos6558MaxBytes})
	request := chaos6558Request()
	request.Options.MaxSerializedBytes = chaos6558MaxBytes
	request.Options.AnswerBudgetBytes = target
	two, err := engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
	if err != nil {
		t.Fatalf("two knobs: %v", err)
	}
	if servedBytes(t, one) > target || len(one.ClaimedFacts) == 0 {
		t.Fatalf("premise: the result must be shed to the %d byte target (served %d bytes)", target, servedBytes(t, one))
	}
	if !reflect.DeepEqual(one, two) {
		t.Fatalf("two knobs served a different canonical result than one knob: %d vs %d bytes, %d vs %d claims", servedBytes(t, one), servedBytes(t, two), len(one.ClaimedFacts), len(two.ClaimedFacts))
	}
}

// The fit against the service ceiling makes no model call of its own: the two
// calls are the first synthesis and the one bounded narrowed retry the shed
// target already costs today, and the answer the retry could not fit is then
// served instead of refused. The artifacts name the chain.
func TestAnAnswerServedUnderTheServiceCeilingAddsNoModelCallAndNamesTheChain(t *testing.T) {
	result, calls, telemetry, err := investigateWithCallerByteOptionCounted(t, callerByteOption, nil)
	if err != nil {
		t.Fatalf("Investigate() error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("synthesis calls = %d, want 2 (first pass and the one bounded retry); the service-ceiling fit must add none", calls)
	}
	var served *PlanNarrowingEvent
	for index := range telemetry.planNarrowings {
		event := telemetry.planNarrowings[index]
		if event.Overrun == contractsv1.ContextFabricBudgetOverrunBytes && !event.RefusalPlanned && !event.RetryAttempted {
			served = &event
		}
		if event.RefusalPlanned {
			t.Fatalf("a refusal was planned on a path that served: %+v", event)
		}
	}
	if served == nil {
		t.Fatalf("no plan-narrowing event records the byte overrun served without a refusal: %+v", telemetry.planNarrowings)
	}
	if len(telemetry.budgetAssertions) == 0 {
		t.Fatal("no budget assertion recorded")
	}
	last := telemetry.budgetAssertions[len(telemetry.budgetAssertions)-1]
	if !last.Fits || last.MaxSerializedBytes != chaos6558MaxBytes {
		t.Fatalf("final assertion = fits %v against %d, want fits against the service ceiling %d", last.Fits, last.MaxSerializedBytes, chaos6558MaxBytes)
	}
	if got := servedBytes(t, result); got > chaos6558MaxBytes {
		t.Fatalf("served %d bytes over the service ceiling", got)
	}
}
