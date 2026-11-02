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
	// Reachable only where no output schema gates the reply first: the
	// generator stub here and the file-exchange parser. The LLM generator
	// and the supplied path validate against the schema, whose window_class
	// enum refuses this value before sanitization (unchanged by CHAOS-12603).
	{"out of vocabulary, no schema gate: stays unset", "quarterly", "high", "", contextfabric.WindowConfidenceHigh, true},
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
		if c.wantUnrecognizedOnReceipt {
			// The supplied path validates the output against the schema
			// first, whose window_class enum refuses an unknown class; that
			// refusal predates CHAOS-12603 and is pinned below.
			continue
		}
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

// The supplied path's schema refuses an out-of-vocabulary window class before
// sanitization, as it did before CHAOS-12603: the carry never sees a raw
// model string, and this change does not alter what the schema admits.
func TestSuppliedSchemaStillRefusesAnUnknownWindowClass(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.WindowClass = "quarterly"
	supplied := mustSuppliedInterpreter(t, nil)
	if _, _, err := supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, suppliedRequestFor(supplied, mustMarshalOutput(t, output))); err == nil {
		t.Fatal("an out-of-vocabulary window_class was admitted; the schema enum is the gate in front of the carry")
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
