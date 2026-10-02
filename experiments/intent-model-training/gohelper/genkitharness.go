package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/firebase/genkit/go/ai"
	"github.com/firebase/genkit/go/core/api"
	"github.com/firebase/genkit/go/genkit"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"go.opentelemetry.io/otel"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

// recorderModelName is an in-process genkit model. It is not a client: it
// records the ModelRequest genkit hands a model plugin and answers with the
// text it is given. Nothing leaves the process.
const recorderModelName = "offline/recorder"

// genkitHarness is one genkit instance with the recorder model and one
// production genkitruntime.Runtime bound to it. The Runtime is the real
// production type (genkitruntime.New), so a saved model answer replayed
// through it is parsed by genkit's own JSON format handler (markdown
// extraction, schema validation, output decode), then by production's
// toDomain, validator and sole-site sanitizers.
//
// Callers hold mu for the whole call: the recorder's answer is shared state.
type genkitHarness struct {
	mu          sync.Mutex
	runtime     *genkitruntime.Runtime
	answer      string
	calls       int
	lastRequest *ai.ModelRequest
}

var (
	harnessOnce     sync.Once
	harnessInstance *genkitHarness
	harnessErr      error
)

func sharedHarness() (*genkitHarness, error) {
	harnessOnce.Do(func() {
		harnessInstance, harnessErr = newGenkitHarness()
	})
	return harnessInstance, harnessErr
}

func newGenkitHarness() (*genkitHarness, error) {
	if api.CurrentEnvironment() == api.EnvironmentDev {
		return nil, errors.New("GENKIT_ENV=dev starts genkit's reflection server; refusing to run")
	}
	// Same suppression production applies before genkit.Init
	// (modelprovider.suppressGenkitTelemetryExport): no exporter is set.
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	otel.SetMeterProvider(metricnoop.NewMeterProvider())

	harness := &genkitHarness{}
	instance := genkit.Init(context.Background())
	// compat_oai.Multimodal is what compat_oai.ResolveAction declares for
	// every BYO OpenAI-compatible model production resolves.
	supports := compat_oai.Multimodal
	genkit.DefineModel(instance, recorderModelName, &ai.ModelOptions{Label: "offline recorder", Supports: &supports},
		func(_ context.Context, req *ai.ModelRequest, _ ai.ModelStreamCallback) (*ai.ModelResponse, error) {
			harness.calls++
			harness.lastRequest = req
			return &ai.ModelResponse{Message: ai.NewModelTextMessage(harness.answer), FinishReason: ai.FinishReasonStop}, nil
		})
	runtime, err := genkitruntime.New(genkitruntime.Config{
		Genkit:   instance,
		Provider: "offline",
		Model:    "recorder",
		ModelRef: recorderModelName,
		// One transport attempt and one draw: a redraw would replay the
		// same saved answer, so it can only repeat the same verdict.
		MaxAttempts:                     1,
		MaxSynthesisResynthesisAttempts: 1,
		Logger:                          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		return nil, fmt.Errorf("construct production runtime: %w", err)
	}
	harness.runtime = runtime
	return harness, nil
}

// begin sets the answer the recorder returns and clears the record of the
// previous call. mu must be held.
func (h *genkitHarness) begin(answer string) {
	h.answer = answer
	h.calls = 0
	h.lastRequest = nil
}

// genkitExtract returns the JSON text genkit's own JSON format handler
// extracts from a model answer: jsonFormatter.Handler(...).ParseMessage
// (ai/format_json.go), which applies base.ExtractJSONFromMarkdown (a
// ```json fence, a bare ``` fence, or an implicit fenced object) and then
// requires valid JSON. The formatter is genkit's registered default, taken
// from ai.DEFAULT_FORMATS, not a reimplementation. With a nil schema the
// handler only extracts; the production schema check is applied by the
// real Runtime call and by checkSchema on this text.
func genkitExtract(raw string) (*string, error) {
	var formatter ai.Formatter
	for _, candidate := range ai.DEFAULT_FORMATS {
		if candidate.Name() == ai.OutputFormatJSON {
			formatter = candidate
		}
	}
	if formatter == nil {
		return nil, errors.New("genkit has no default json formatter")
	}
	handler, err := formatter.Handler(nil)
	if err != nil {
		return nil, err
	}
	message, err := handler.ParseMessage(ai.NewModelTextMessage(raw))
	if err != nil {
		return nil, err
	}
	text := message.Text()
	if text == "" {
		return nil, errors.New("genkit extracted no JSON text")
	}
	return &text, nil
}
