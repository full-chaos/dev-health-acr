package genkitruntime

import (
	"context"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12603: the model's sanitized window_class/window_confidence pick
// reaches the InterpretedQuestion the engine classifies the window from, on
// the server-model path and on the supplied-interpretation path, not only the
// receipt. Without it ClassifyWindow always sees an empty pick.

type windowPickCase struct {
	name                      string
	rawClass, rawConfidence   string
	wantClass                 contextfabric.WindowClass
	wantConfidence            contextfabric.WindowConfidence
	wantUnrecognizedOnReceipt bool
}

var windowPickCases = []windowPickCase{
	{"state_snapshot high", "state_snapshot", "high", contextfabric.WindowClassStateSnapshot, contextfabric.WindowConfidenceHigh, false},
	{"trend low", "trend_assessment", "low", contextfabric.WindowClassTrendAssessment, contextfabric.WindowConfidenceLow, false},
	{"class without confidence", "recent_activity_lookup", "", contextfabric.WindowClassRecentActivityLookup, "", false},
	{"omitted", "", "", "", "", false},
	// An unknown class stays unset with the flag set on every path: the
	// schema gate of the LLM generator and the supplied path drops it before
	// it validates (CHAOS-12617), and the stub and the file-exchange parser
	// have no gate.
	{"out of vocabulary: stays unset", "quarterly", "high", "", contextfabric.WindowConfidenceHigh, true},
}

func TestInterpretedQuestionCarriesTheModelWindowClass(t *testing.T) {
	t.Parallel()
	for _, c := range windowPickCases {
		output := validInterpretationOutput()
		output.WindowClass, output.WindowConfidence = c.rawClass, c.rawConfidence
		runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
		interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
		if err != nil {
			t.Fatalf("%s: InterpretQuestion() error = %v", c.name, err)
		}
		if interpreted.WindowClass != c.wantClass || interpreted.WindowConfidence != c.wantConfidence {
			t.Errorf("%s: interpreted window = %q/%q, want %q/%q", c.name, interpreted.WindowClass, interpreted.WindowConfidence, c.wantClass, c.wantConfidence)
		}
		if interpreted.WindowClass != receipt.WindowClass || interpreted.WindowConfidence != receipt.WindowConfidence {
			t.Errorf("%s: interpreted %q/%q disagrees with the receipt %q/%q", c.name, interpreted.WindowClass, interpreted.WindowConfidence, receipt.WindowClass, receipt.WindowConfidence)
		}
		if receipt.WindowClassUnrecognized != c.wantUnrecognizedOnReceipt {
			t.Errorf("%s: receipt unrecognized = %v, want %v", c.name, receipt.WindowClassUnrecognized, c.wantUnrecognizedOnReceipt)
		}
	}
}

func TestSuppliedInterpretationCarriesTheWindowClass(t *testing.T) {
	t.Parallel()
	for _, c := range windowPickCases {
		output := validInterpretationOutput()
		output.WindowClass, output.WindowConfidence = c.rawClass, c.rawConfidence
		supplied := mustSuppliedInterpreter(t, nil)
		interpreted, receipt, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, suppliedRequestFor(supplied, mustMarshalOutput(t, output)))
		if err != nil {
			t.Fatalf("%s: InterpretSuppliedQuestion() error = %v", c.name, err)
		}
		if interpreted.WindowClass != c.wantClass || interpreted.WindowConfidence != c.wantConfidence {
			t.Errorf("%s: supplied interpreted window = %q/%q, want %q/%q", c.name, interpreted.WindowClass, interpreted.WindowConfidence, c.wantClass, c.wantConfidence)
		}
		if receipt.WindowClassUnrecognized != c.wantUnrecognizedOnReceipt {
			t.Errorf("%s: supplied receipt unrecognized = %v, want %v", c.name, receipt.WindowClassUnrecognized, c.wantUnrecognizedOnReceipt)
		}
	}
}

// The file-exchange transport (the trial and shadow harnesses' interpreter)
// parses the output through ParseInterpretationOutputSignals; its
// interpretation carries the same sanitized pick as its capture.
func TestExchangeParserCarriesTheWindowClass(t *testing.T) {
	t.Parallel()
	for _, c := range windowPickCases {
		output := validInterpretationOutput()
		output.WindowClass, output.WindowConfidence = c.rawClass, c.rawConfidence
		raw := mustMarshalOutput(t, output)
		interpreted, capture, err := ParseInterpretationOutputSignals(raw, validRequest().TimeContext)
		if err != nil {
			t.Fatalf("%s: ParseInterpretationOutputSignals() error = %v", c.name, err)
		}
		if interpreted.WindowClass != c.wantClass || interpreted.WindowConfidence != c.wantConfidence {
			t.Errorf("%s: exchange interpreted window = %q/%q, want %q/%q", c.name, interpreted.WindowClass, interpreted.WindowConfidence, c.wantClass, c.wantConfidence)
		}
		if capture.Window.ClassUnrecognized != c.wantUnrecognizedOnReceipt {
			t.Errorf("%s: exchange capture unrecognized = %v, want %v", c.name, capture.Window.ClassUnrecognized, c.wantUnrecognizedOnReceipt)
		}
	}
}

// The older window-only parser carries the pick the same way (codex r3 P3).
func TestWindowOnlyParserCarriesTheWindowClass(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.WindowClass, output.WindowConfidence = "state_snapshot", "high"
	interpreted, capture, err := ParseInterpretationOutputWindow(mustMarshalOutput(t, output), validRequest().TimeContext)
	if err != nil {
		t.Fatal(err)
	}
	if interpreted.WindowClass != capture.Class || interpreted.WindowConfidence != capture.Confidence || interpreted.WindowClass != contextfabric.WindowClassStateSnapshot {
		t.Fatalf("window-only parser interpretation %q/%q, capture %q/%q: want both state_snapshot/high", interpreted.WindowClass, interpreted.WindowConfidence, capture.Class, capture.Confidence)
	}
}
