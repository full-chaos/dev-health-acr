package genkitruntime

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-go/llm"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12617: window_class and window_confidence are optional picks the
// engine can do without (CHAOS-3900 F5: an out-of-vocabulary pick is never a
// validation failure). The schema gate in front of the sanitizer refused the
// whole otherwise-valid interpretation for one unknown pick, on the server
// path and on the supplied path. An unknown pick is now dropped and logged:
// the class stays unset and window_class_unrecognized is set, as on a
// transport with no schema gate. Every other violation still refuses.

const unknownWindowPickLogSubstring = "unknown window pick dropped"

// windowPickReply is a valid interpretation output with its window picks set
// to the raw JSON values given; an empty raw value leaves the pick out.
func windowPickReply(t *testing.T, class, confidence string, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(mustMarshalOutput(t, validInterpretationOutput()), &object); err != nil {
		t.Fatal(err)
	}
	delete(object, "window_class")
	delete(object, "window_confidence")
	if class != "" {
		object["window_class"] = json.RawMessage(class)
	}
	if confidence != "" {
		object["window_confidence"] = json.RawMessage(confidence)
	}
	if edit != nil {
		edit(object)
	}
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type windowPickPath struct {
	name      string
	interpret func(t *testing.T, reply []byte, logs *strings.Builder) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error)
}

var windowPickPaths = []windowPickPath{
	{"server (LLM generator)", func(t *testing.T, reply []byte, logs *strings.Builder) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
		t.Helper()
		client := &scriptedLLM{model: "m", replies: []llmReply{{result: llm.Result{Text: string(reply), Model: "m"}}}}
		runtime, err := NewWithLLM(Config{
			Provider: "openai", Model: "m", Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
			InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1", SchemaVersion: "schema-v1",
			EvaluatorVersion: "eval-v1", Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
		}, LLMClients{"m": client})
		if err != nil {
			t.Fatal(err)
		}
		return runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	}},
	{"supplied", func(t *testing.T, reply []byte, logs *strings.Builder) (contextfabric.InterpretedQuestion, contextfabric.ModelExecutionReceipt, error) {
		t.Helper()
		supplied := mustSuppliedInterpreter(t, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
		return supplied.InterpretSuppliedQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, suppliedRequestFor(supplied, reply))
	}},
}

func TestAnUnknownWindowPickIsDroppedNotRefused(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name                string
		class, confidence   string
		wantClass           contextfabric.WindowClass
		wantConfidence      contextfabric.WindowConfidence
		wantUnrecognized    bool
		wantDroppedInTheLog []string
	}{
		{"both unknown", `"quarterly"`, `"maybe"`, "", "", true, []string{"window_class", "window_confidence"}},
		{"an unknown class beside a known confidence", `"quarterly"`, `"high"`, "", contextfabric.WindowConfidenceHigh, true, []string{"window_class"}},
		{"an unknown confidence beside a known class", `"state_snapshot"`, `"maybe"`, contextfabric.WindowClassStateSnapshot, "", false, []string{"window_confidence"}},
	} {
		for _, path := range windowPickPaths {
			t.Run(c.name+"/"+path.name, func(t *testing.T) {
				t.Parallel()
				var logs strings.Builder
				interpreted, receipt, err := path.interpret(t, windowPickReply(t, c.class, c.confidence, nil), &logs)
				if err != nil {
					t.Fatalf("an otherwise-valid interpretation was refused for an unknown optional pick: %v", err)
				}
				if receipt.Outcome != "success" {
					t.Fatalf("receipt outcome = %q, want success", receipt.Outcome)
				}
				if receipt.WindowClass != c.wantClass || receipt.WindowConfidence != c.wantConfidence || receipt.WindowClassUnrecognized != c.wantUnrecognized {
					t.Fatalf("receipt window = %q/%q unrecognized=%v, want %q/%q unrecognized=%v", receipt.WindowClass, receipt.WindowConfidence, receipt.WindowClassUnrecognized, c.wantClass, c.wantConfidence, c.wantUnrecognized)
				}
				if interpreted.WindowClass != c.wantClass || interpreted.WindowConfidence != c.wantConfidence {
					t.Fatalf("interpreted window = %q/%q, want %q/%q", interpreted.WindowClass, interpreted.WindowConfidence, c.wantClass, c.wantConfidence)
				}
				line := logLineWith(logs.String(), unknownWindowPickLogSubstring)
				if line == "" || !strings.Contains(line, "level=INFO") {
					t.Fatalf("no Info line says the pick was dropped:\n%s", logs.String())
				}
				for _, field := range c.wantDroppedInTheLog {
					if !strings.Contains(line, field) {
						t.Fatalf("the drop line does not name %s: %s", field, line)
					}
				}
				for _, value := range []string{"quarterly", "maybe"} {
					if strings.Contains(logs.String(), value) {
						t.Fatalf("the model's value %q reached the log:\n%s", value, logs.String())
					}
				}
			})
		}
	}
}

func TestAnotherViolationBesideAnUnknownWindowPickStillRefuses(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		reply func(t *testing.T) []byte
	}{
		{"an unknown shape beside an unknown class", func(t *testing.T) []byte {
			return windowPickReply(t, `"quarterly"`, "", func(o map[string]json.RawMessage) { o["shape"] = json.RawMessage(`"bogus"`) })
		}},
		{"an undeclared property beside an unknown class", func(t *testing.T) []byte {
			return windowPickReply(t, `"quarterly"`, "", func(o map[string]json.RawMessage) { o["extra"] = json.RawMessage(`1`) })
		}},
		{"a missing required field beside an unknown confidence", func(t *testing.T) []byte {
			return windowPickReply(t, "", `"maybe"`, func(o map[string]json.RawMessage) { delete(o, "requested_judgment") })
		}},
		{"a class that is not a string", func(t *testing.T) []byte { return windowPickReply(t, `5`, "", nil) }},
		{"a class that is null", func(t *testing.T) []byte { return windowPickReply(t, `null`, "", nil) }},
		{"a confidence that is null", func(t *testing.T) []byte { return windowPickReply(t, "", `null`, nil) }},
		{"a confidence that is not a string", func(t *testing.T) []byte { return windowPickReply(t, "", `["high"]`, nil) }},
	} {
		for _, path := range windowPickPaths {
			t.Run(c.name+"/"+path.name, func(t *testing.T) {
				t.Parallel()
				var logs strings.Builder
				_, receipt, err := path.interpret(t, c.reply(t), &logs)
				if err == nil {
					t.Fatalf("admitted; receipt %+v", receipt)
				}
				if receipt.Outcome != "invalid_output" {
					t.Fatalf("receipt outcome = %q, want invalid_output", receipt.Outcome)
				}
			})
		}
	}
}

func TestAKnownWindowPickReplyIsCheckedAsWritten(t *testing.T) {
	t.Parallel()
	var logs strings.Builder
	gen := llmGenerator{logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	var err error
	if gen.interpretation, err = newOutputSchema(interpretationOutput{}, true); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{
		string(windowPickReply(t, `"state_snapshot"`, `"high"`, nil)),
		string(windowPickReply(t, "", "", nil)),
		"```json\n" + string(windowPickReply(t, `"trend_assessment"`, `"low"`, nil)) + "\n```",
	} {
		candidate, err := gen.check(context.Background(), "interpret", reply, gen.interpretation)
		if err != nil {
			t.Fatalf("check(%q) = %v", reply, err)
		}
		if want := extractGenkitJSON(reply); candidate != want {
			t.Fatalf("check rewrote a reply with no unknown pick: got %q, want %q", candidate, want)
		}
	}
	if strings.Contains(logs.String(), unknownWindowPickLogSubstring) {
		t.Fatalf("a known pick was logged as dropped:\n%s", logs.String())
	}
	// An unknown pick is removed only from the copy the schema reads: the
	// reply the decode reads keeps it, so the sanitizer sees the raw pick.
	reply := string(windowPickReply(t, `"quarterly"`, "", nil))
	candidate, err := gen.check(context.Background(), "interpret", reply, gen.interpretation)
	if err != nil {
		t.Fatalf("check = %v", err)
	}
	if candidate != reply {
		t.Fatalf("check returned %q, want the reply as written", candidate)
	}
}

// The drop reads the enum from the schema it is given: a pick the schema
// declares no enum for is the schema's to judge, and a reply that is not an
// object is returned as written.
func TestWithoutUnknownWindowPicksDropsOnlyAgainstADeclaredEnum(t *testing.T) {
	t.Parallel()
	reply := string(windowPickReply(t, `"quarterly"`, `"maybe"`, nil))
	noEnum := map[string]any{"properties": map[string]any{
		"window_class":      map[string]any{"type": "string"},
		"window_confidence": map[string]any{"type": "string"},
	}}
	if got, dropped := withoutUnknownWindowPicks(reply, noEnum); got != reply || dropped != nil {
		t.Fatalf("a pick with no declared enum was dropped: %v", dropped)
	}
	enum := map[string]any{"properties": map[string]any{
		"window_class":      map[string]any{"type": "string", "enum": []any{"state_snapshot"}},
		"window_confidence": map[string]any{"type": "string", "enum": []any{"high"}},
	}}
	got, dropped := withoutUnknownWindowPicks(reply, enum)
	if strings.Join(dropped, ",") != "window_class,window_confidence" || strings.Contains(got, "window_class") || strings.Contains(got, "window_confidence") {
		t.Fatalf("dropped %v, copy %s", dropped, got)
	}
	for _, notObject := range []string{`[1]`, `"x"`, `null`, `{`} {
		if got, dropped := withoutUnknownWindowPicks(notObject, enum); got != notObject || dropped != nil {
			t.Fatalf("%s changed to %s (%v)", notObject, got, dropped)
		}
	}
}

func logLineWith(logs, substring string) string {
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, substring) {
			return line
		}
	}
	return ""
}
