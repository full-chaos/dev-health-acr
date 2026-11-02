package genkitruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-12595 (codex r1 P2): the window classification Info line carries the
// model's own pick from the server-model path, not an empty one. The pick now
// reaches the interpretation (CHAOS-12603, carryWindowPick); this pins it end
// to end, from the runtime's returned interpretation to the line.
func TestModelWindowPickReachesTheClassificationLine(t *testing.T) {
	t.Parallel()
	output := validInterpretationOutput()
	output.WindowClass, output.WindowConfidence = "trend_assessment", "high"
	runtime := mustRuntime(t, &generatorStub{interpretation: output}, Config{})
	interpreted, receipt, err := runtime.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
	if err != nil {
		t.Fatalf("InterpretQuestion() error = %v", err)
	}
	var buf bytes.Buffer
	sink := contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	sink.RecordWindowClassification(context.Background(), storage.Principal{OrgID: "org_1"}, interpreted.WindowClass, contextfabric.ClassifyWindow(interpreted, interpreted.WindowClass, interpreted.WindowConfidence))
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("decode line %q: %v", buf.String(), err)
	}
	if line["model_class"] != string(receipt.WindowClass) || line["model_class"] != "trend_assessment" {
		t.Fatalf("line model_class = %v, receipt %q: want the model's trend_assessment on both", line["model_class"], receipt.WindowClass)
	}
}
