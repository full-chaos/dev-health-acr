package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestRuntimeCapturesWindowClassOnReceipt is the CHAOS-3900 W0 wiring pin:
// a valid closed-vocabulary window_class/window_confidence pick from the
// model reaches ModelExecutionReceipt sanitized and unmodified --
// InterpretedQuestion itself is untouched (design brief §2/F5: the fields
// never ride the wire-visible domain type in W0 -- see
// chaos3900_window_vocab.go's package doc comment in contextfabric).
func TestRuntimeCapturesWindowClassOnReceipt(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.WindowClass = "trend_assessment"
	output.WindowConfidence = "high"
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.WindowClass != contextfabric.WindowClassTrendAssessment {
		t.Fatalf("receipt.WindowClass = %q, want trend_assessment", receipt.WindowClass)
	}
	if receipt.WindowConfidence != contextfabric.WindowConfidenceHigh {
		t.Fatalf("receipt.WindowConfidence = %q, want high", receipt.WindowConfidence)
	}
	if receipt.WindowClassUnrecognized {
		t.Fatal("receipt.WindowClassUnrecognized = true, want false for a valid vocabulary member")
	}
}

// TestRuntimeSanitizesUnrecognizedWindowClassRatherThanFailingValidation is
// the F5 control-flow pin: an out-of-vocab window_class from the model
// must NOT reject the whole interpretation (interpreted.Validate() runs
// strictly before this sanitize step and never sees this field at all) --
// it sanitizes to unset plus a counted unrecognized flag.
func TestRuntimeSanitizesUnrecognizedWindowClassRatherThanFailingValidation(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.WindowClass = "not_a_real_class"
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v, want success -- an out-of-vocab window_class must never fail interpretation", err)
	}
	if receipt.WindowClass != "" {
		t.Fatalf("receipt.WindowClass = %q, want empty (sanitized away)", receipt.WindowClass)
	}
	if !receipt.WindowClassUnrecognized {
		t.Fatal("receipt.WindowClassUnrecognized = false, want true for an out-of-vocab pick")
	}
	if err := interpreted.Validate(); err != nil {
		t.Fatalf("returned InterpretedQuestion failed Validate(): %v", err)
	}
}

// TestRuntimeLeavesWindowFieldsUnsetWhenModelOmitsThem pins the
// backward-compatible default: a model output carrying no window_class/
// window_confidence at all (every pre-CHAOS-3900 fixture, and any real
// model call that chooses to omit them, since both fields are OPTIONAL)
// produces an entirely empty capture, never a false unrecognized flag.
func TestRuntimeLeavesWindowFieldsUnsetWhenModelOmitsThem(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	_, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	if receipt.WindowClass != "" || receipt.WindowConfidence != "" || receipt.WindowClassUnrecognized {
		t.Fatalf("receipt window fields = class=%q confidence=%q unrecognized=%v, want all zero/false", receipt.WindowClass, receipt.WindowConfidence, receipt.WindowClassUnrecognized)
	}
}
