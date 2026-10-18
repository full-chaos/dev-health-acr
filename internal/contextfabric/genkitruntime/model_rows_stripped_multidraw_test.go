package genkitruntime

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/observability"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// TestModelRowsStrippedLinesOfAllDrawsCertifyThroughTheRealRuntime serves an
// answer after two rejected draws that each authored table content, and
// requires the three lines the strip writes (one per draw, at the resynthesis
// ceiling) to certify under the declared bound.
func TestModelRowsStrippedLinesOfAllDrawsCertifyThroughTheRealRuntime(t *testing.T) {
	t.Parallel()
	input := validSynthesisInput()
	withRows := func(output synthesisOutput) synthesisOutput {
		claim := groundedReadinessClaim(input)
		claim.Rows = []contractsv1.ContextFabricClaimedFactRow{{}}
		output.ClaimedFacts = []contextfabric.ClaimedFact{claim}
		return output
	}
	var buf bytes.Buffer
	const requestID = "req_0123456789abcdef0123456789abcdef"
	ctx := observability.WithRequestID(context.Background(), requestID)
	sink := contextfabric.NewSlogEngineTelemetry(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{
		withRows(invalidTitleSynthesisOutput()),
		withRows(invalidEvidenceSynthesisOutput()),
		withRows(validSynthesisOutput()),
	}}
	_, logger := newCaptureLogger()
	runtime := mustRuntime(t, gen, Config{Logger: logger, Telemetry: sink, MaxSynthesisResynthesisAttempts: DefaultMaxSynthesisResynthesisAttempts})
	if _, _, err := runtime.SynthesizeAnswer(ctx, storage.Principal{OrgID: "org_1"}, input); err != nil {
		t.Fatalf("SynthesizeAnswer() error = %v, want the third draw served", err)
	}
	if gen.calls != 3 {
		t.Fatalf("draws = %d, want 3 (the resynthesis ceiling)", gen.calls)
	}
	if got := bytes.Count(buf.Bytes(), []byte(eventspec.ModelRowsStripped.Msg)); got != 3 {
		t.Fatalf("%d stripped lines for 3 draws that authored table content: %s", got, buf.String())
	}
	log, err := certify.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certify.Certify(log, certify.Assertion{Event: eventspec.ModelRowsStripped, Want: map[string]any{"request_id": requestID, "org_id": "org_1", "cf_model_rows_stripped": 1}}); err != nil {
		t.Fatalf("the stripped lines of a three draw call failed certification: %v\n%s", err, buf.String())
	}
}
