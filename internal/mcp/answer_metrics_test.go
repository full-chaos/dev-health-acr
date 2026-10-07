package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics"
	"github.com/full-chaos/dev-health-acr/internal/hostedmetrics/hostedmetricstest"
)

// A delivered investigation answer is counted by its status and the tool that
// delivered it, and its query version is named on the request's span.
func TestInvestigateQuestionCountsTheAnswerAndNamesTheQueryVersion(t *testing.T) {
	body := envelopeBody(nil)
	boot := synthesisFixtureBootstrap(t, body, nil, nil)
	instruments, read := hostedmetricstest.New(t, hostedmetrics.Vocabularies{Tools: HTTPToolVocabulary()})
	spans := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	ctx, span := provider.Tracer("test").Start(context.Background(), "request")

	cfg, callerCtx := callerContextFor(ctx, boot)
	cfg.metrics = instruments
	encoded, _ := json.Marshal(map[string]any{"question": "q"})
	result, err := handleInvestigateQuestion(callerCtx, cfg, &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Arguments: encoded}})
	if err != nil || result.IsError {
		t.Fatalf("call failed: %v %+v", err, result)
	}
	span.End()

	want := "acr_answers_total{status=" + string(body.Status) + ",tool=investigate_question}"
	if got := read(); len(got) != 1 || got[want] != 1 {
		t.Fatalf("cells %v, want exactly %s = 1", got, want)
	}
	var version string
	for _, stub := range spans.GetSpans() {
		for _, kv := range stub.Attributes {
			if string(kv.Key) == SpanAttributeQueryVersion {
				version = kv.Value.AsString()
			}
		}
	}
	if version == "" || version != body.Versions.QueryVersion {
		t.Fatalf("span %s = %q, want the answer's %q", SpanAttributeQueryVersion, version, body.Versions.QueryVersion)
	}
}
