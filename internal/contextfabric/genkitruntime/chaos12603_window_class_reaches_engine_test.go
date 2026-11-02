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
	{"out of vocabulary stays unset", "quarterly", "high", "", contextfabric.WindowConfidenceHigh, true},
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
			// first, whose window_class enum refuses an unknown class.
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
