package genkitruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// synthesisInputLineFor drives SynthesizeAnswer through the real runtime and a
// real slog JSON handler and returns the parsed log plus the receipt.
func synthesisInputLineFor(t *testing.T, gen generator, override Config, input contextfabric.SynthesisInput) (*certify.Log, contextfabric.ModelExecutionReceipt, error) {
	t.Helper()
	var buf bytes.Buffer
	override.Logger = slog.New(slog.NewJSONHandler(&buf, nil))
	runtime := mustRuntime(t, gen, override)
	_, receipt, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, input)
	parsed, parseErr := certify.Parse(buf.Bytes())
	if parseErr != nil {
		t.Fatalf("certify.Parse: %v", parseErr)
	}
	return parsed, receipt, err
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// TestSynthesizeAnswerEmitsDeclaredInputLine drives two draws whose raw
// outputs carry DIFFERENT claim counts (2 then 0), so an arm that reports one
// draw's count for the other, or the served count for both, cannot pass. Every
// value is asserted against an independent source: the digest against the
// prompt the generator actually received, the counts against the fixture.
func TestSynthesizeAnswerEmitsDeclaredInputLine(t *testing.T) {
	t.Parallel()
	rejected := invalidTitleSynthesisOutput()
	rejected.ClaimedFacts = []contextfabric.ClaimedFact{{}, {}}
	valid := validSynthesisOutput()
	gen := &drawSequenceGenerator{outputs: []synthesisOutput{rejected, valid}}
	input := validSynthesisInput()
	// a second fact sharing one ref and adding one, so total and distinct differ
	input.Facts.Facts = append(input.Facts.Facts, contextfabric.CanonicalFact{
		Kind: contextfabric.FactReadiness, Subject: input.Facts.Facts[0].Subject,
		Fields:         input.Facts.Facts[0].Fields,
		EvidenceRefIDs: []string{"evidence_release_1234", "evidence_other_5678"}, SourceState: contextfabric.SourceAvailable,
	})

	parsed, receipt, err := synthesisInputLineFor(t, gen, Config{MaxSynthesisResynthesisAttempts: 2}, input)
	if err != nil {
		t.Fatalf("SynthesizeAnswer error = %v", err)
	}
	if len(gen.requests) != 2 {
		t.Fatalf("generator saw %d requests, want 2", len(gen.requests))
	}
	wantDigest := sha256Hex(gen.requests[0].Prompt)
	rejectedBytes, _ := json.Marshal(rejected)
	validBytes, _ := json.Marshal(valid)
	want := map[string]any{
		"request_id":   "request_12345678",
		"input_digest": wantDigest,
		"model_id":     receipt.Model,

		"org_id_hash":                 decisionOrgIDHash("org_1"),
		"model_version":               receipt.ModelVersion,
		"prompt_version":              "synthesis-v1",
		"input_bytes":                 len(gen.requests[0].Prompt),
		"facts":                       2,
		"fact_evidence_refs":          3,
		"fact_evidence_refs_distinct": 2,
		"paths":                       1,
		"driver_candidates":           0,
		"cohort_members":              0,
		"outcome":                     "success",
		"draws_total":                 2,
		"draw_outcomes":               "1:invalid_output,2:success",
		"draw_claims":                 "1:2,2:0",
		"draw_output_digests":         fmt.Sprintf("1:%s,2:%s", contextfabric.DigestModelValue(rejectedBytes), contextfabric.DigestModelValue(validBytes)),
		"claims":                      0,
		"drivers":                     1,
		"evidence_refs":               1,
	}
	if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.SynthesisInput, Want: want}); err != nil {
		t.Fatalf("certify synthesis input line: %v", err)
	}
	if receipt.Model == "" || receipt.PromptVersion == "" {
		t.Fatalf("fixture must carry non-empty model identity, got %+v", receipt)
	}
}

// TestSynthesizeAnswerEmitsNoInputLineBeforeEncoding: a call refused before it
// encodes an input has none to describe, and must say so by emitting nothing.
func TestSynthesizeAnswerEmitsNoInputLineBeforeEncoding(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	runtime := mustRuntime(t, &drawSequenceGenerator{outputs: []synthesisOutput{validSynthesisOutput()}}, Config{Logger: slog.New(slog.NewJSONHandler(&buf, nil))})
	if _, _, err := runtime.SynthesizeAnswer(context.Background(), storage.Principal{}, validSynthesisInput()); err == nil {
		t.Fatal("SynthesizeAnswer without an organization must fail")
	}
	if strings.Contains(buf.String(), eventspec.SynthesisInput.Msg) {
		t.Fatalf("a call rejected before encoding emitted an input line: %s", buf.String())
	}
}
