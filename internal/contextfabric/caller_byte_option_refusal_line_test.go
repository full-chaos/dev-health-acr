package contextfabric

import (
	"context"
	"errors"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

const callerByteOption = 8192

func investigateWithCallerByteOption(t *testing.T, option int, hook func(InvestigationResult) InvestigationResult) (InvestigationResult, error) {
	t.Helper()
	calls := 0
	engine := chaos6558Engine(t, &calls, &recordingTelemetry{}, chaos6558Shape{maxBytes: chaos6558MaxBytes})
	engine.servedLateHook = hook
	request := chaos6558Request()
	request.Options.MaxSerializedBytes = option
	return engine.Investigate(context.Background(), storage.Principal{OrgID: "org_1"}, request)
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
