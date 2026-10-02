package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/modelprovider"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

func assertNoDivergence(t *testing.T, name string, env Envelope) {
	t.Helper()
	for _, e := range env.Errors {
		if e.Stage == stageTransport {
			t.Fatalf("%s: helper analysis disagrees with the production call: %+v", name, e)
		}
	}
}

func TestFencedTargetAcceptedByGenkitRejectedByExchange(t *testing.T) {
	fenced := "```json\n" + validTarget + "\n```"
	genkit := mustValidateWith(t, fenced, testRequest, transportGenkit)
	if genkit.Transport != transportGenkit || genkit.ExtractedText == nil || *genkit.ExtractedText != validTarget {
		t.Fatalf("genkit did not extract the fenced JSON: transport=%q extracted=%v", genkit.Transport, genkit.ExtractedText)
	}
	if !genkit.JSONOK || !genkit.ProductionAccepts || !genkit.TurnAdmitted || len(genkit.Errors) != 0 {
		t.Fatalf("genkit must accept a ```json fence: accepts=%v admitted=%v errors=%+v", genkit.ProductionAccepts, genkit.TurnAdmitted, genkit.Errors)
	}
	if genkit.CanonicalText == nil || *genkit.CanonicalText != validTarget {
		t.Fatal("strict analysis must run on the extracted text")
	}
	if !genkit.MarkdownFenced || genkit.StrictOK {
		t.Fatal("a fenced answer is accepted but is not a clean training target")
	}
	exchange := mustValidateWith(t, fenced, testRequest, transportExchange)
	if exchange.Transport != transportExchange || exchange.ExtractedText != nil {
		t.Fatal("exchange transport extracts nothing")
	}
	if exchange.JSONOK || exchange.InterpreterOK || exchange.ProductionAccepts {
		t.Fatal("the exchange transport must reject a fenced answer")
	}
	assertNoDivergence(t, "genkit", genkit)
	assertNoDivergence(t, "exchange", exchange)
}

// verdictView is what must not depend on the transport for an unfenced
// answer. When the answer fails the output schema, the genkit runtime
// rejects the draw before the interpreter runs, while the exchange
// transport (no schema check) still runs frame validation and family
// resolution; those downstream reports are compared only when schema_ok.
func verdictView(t *testing.T, env Envelope) string {
	t.Helper()
	view := map[string]any{
		"json_ok": env.JSONOK, "decode_ok": env.DecodeOK, "domain_ok": env.DomainOK, "schema_ok": env.SchemaOK,
		"production_accepts": env.ProductionAccepts, "turn_admitted": env.TurnAdmitted,
		"strict_ok": env.StrictOK, "sanitize_clean": env.SanitizeClean, "canonical_text": env.CanonicalText,
		"duplicate_keys": env.DuplicateKeys, "unknown_fields": env.UnknownFields, "case_variant_keys": env.CaseVariantKeys,
		"errors": env.Errors, "rejection_reason": env.RejectionReason,
	}
	if env.SchemaOK {
		view["interpreter_ok"] = env.InterpreterOK
		view["semantic"] = env.Semantic
		view["frame"] = env.Frame
		view["family"] = env.Family
		view["interpreted"] = env.Interpreted
	} else if env.Semantic != nil {
		view["semantic_flat"] = env.Semantic.Flat
		view["semantic_hints"] = env.Semantic.Hints
		view["semantic_frame_proposal"] = env.Semantic.FrameProposal
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestPlainTargetsSameVerdictInBothTransports: without a fence, genkit's
// extraction is the identity, and every verdict matches the exchange
// transport. exchange_accepts is excluded: it is the transport's own call,
// which applies genkit's schema check only on the genkit transport.
func TestPlainTargetsSameVerdictInBothTransports(t *testing.T) {
	cases := map[string]string{
		"valid":          validTarget,
		"no_terms_frame": strings.Replace(validTarget, `"subject_expression":{"kind":"named_subject","terms":["Harbor Lights"]}`, `"subject_expression":{"kind":"named_subject"}`, 1),
		"duplicate_key":  strings.Replace(validTarget, `"shape":"single_subject"`, `"shape":"open","shape":"single_subject"`, 1),
		"unknown_field":  strings.Replace(validTarget, `"clarification_needed":false`, `"clarification_needed":false,"confidence":"high"`, 1),
		"case_variant":   strings.Replace(validTarget, `"shape":`, `"Shape":`, 1),
		"domain_invalid": `{"shape":"open","requested_judgment":"status","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":true}`,
		"schema_invalid": `{"shape":"single_subject","requested_judgment":"status","time_context":{"axis":"range"},"fact_requirements":[],"clarification_needed":false}`,
		"normalized":     strings.Replace(validTarget, `"subject_terms":["Harbor Lights"]`, `"subject_terms":["Harbor Lights","Harbor Lights"],"requested_judgment_kind":"vibes"`, 1),
		"whitespace":     "\n  " + validTarget + "\n",
	}
	for name, raw := range cases {
		genkit := mustValidateWith(t, raw, testRequest, transportGenkit)
		exchange := mustValidateWith(t, raw, testRequest, transportExchange)
		if got, want := verdictView(t, genkit), verdictView(t, exchange); got != want {
			t.Errorf("%s: transports disagree\n genkit:   %s\n exchange: %s", name, got, want)
		}
		if genkit.MarkdownFenced {
			t.Errorf("%s: no fence, but flagged fenced", name)
		}
		assertNoDivergence(t, name+"/genkit", genkit)
		assertNoDivergence(t, name+"/exchange", exchange)
	}
}

// TestGenkitProseHandlingPinned pins genkit v1.11.0's behaviour on prose
// around the JSON, whatever it is: a fenced block inside prose is
// extracted; bare prose around unfenced JSON is not.
func TestGenkitProseHandlingPinned(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		accepted  bool
		extracted *string
	}{
		{"prose_around_json_fence", "Sure, here it is.\n```json\n" + validTarget + "\n```\nLet me know.", true, ptr(validTarget)},
		{"bare_fence", "```\n" + validTarget + "\n```", true, ptr(validTarget)},
		{"uppercase_json_fence", "```JSON\n" + validTarget + "\n```", true, ptr(validTarget)},
		{"prose_around_unfenced_json", "Here is the interpretation: " + validTarget + " Hope this helps.", false, nil},
		{"trailing_prose_unfenced", validTarget + "\nThat is my answer.", false, nil},
		{"empty", "", false, nil},
	}
	for _, c := range cases {
		env := mustValidateWith(t, c.raw, testRequest, transportGenkit)
		if env.ProductionAccepts != c.accepted {
			t.Errorf("%s: production_accepts=%v, pinned %v (errors %+v)", c.name, env.ProductionAccepts, c.accepted, env.Errors)
		}
		if !reflect.DeepEqual(env.ExtractedText, c.extracted) {
			t.Errorf("%s: extracted_text=%v, pinned %v", c.name, deref(env.ExtractedText), deref(c.extracted))
		}
		assertNoDivergence(t, c.name, env)
	}
}

func ptr(s string) *string { return &s }

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%q", *s)
}

// TestGenkitTransportMatchesLoopbackProduction is the differential oracle
// for the genkit transport: the same answers go through the production
// constructor (modelprovider.NewGenkitRuntime: genkit + compat_oai +
// openai-go) against a loopback httptest server, and the accept/reject
// verdict and decoded interpretation must equal the helper's. This test
// constructs a real model client, pointed only at loopback.
func TestGenkitTransportMatchesLoopbackProduction(t *testing.T) {
	var mu sync.Mutex
	current := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		answer, _ := json.Marshal(current)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-offline","object":"chat.completion","created":1767225600,"model":"offline-model","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`, answer)
	}))
	defer server.Close()
	ctx := context.Background()
	runtime, err := modelprovider.NewGenkitRuntime(ctx, modelprovider.Config{
		Provider: modelprovider.DefaultProvider, BaseURL: server.URL + "/v1/", Model: "offline-model", APIKey: "loopback-test-key",
		Timeout: 10 * time.Second, MaxAttempts: 1, MaxTransportRetries: 0, AllowInsecureBaseURL: true,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRequest(json.RawMessage(testRequest))
	if err != nil {
		t.Fatal(err)
	}
	principal := storage.Principal{OrgID: "offline-test", Subject: "offline-test"}
	answers := map[string]string{
		"plain":                      validTarget,
		"json_fence":                 "```json\n" + validTarget + "\n```",
		"prose_around_json_fence":    "Sure.\n```json\n" + validTarget + "\n```\nDone.",
		"prose_around_unfenced_json": "Here is the interpretation: " + validTarget,
		"fenced_schema_invalid":      "```json\n" + `{"shape":"single_subject","requested_judgment":"status","time_context":{"axis":"range"},"fact_requirements":[],"clarification_needed":false}` + "\n```",
		"fenced_domain_invalid":      "```json\n" + `{"shape":"open","requested_judgment":"status","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":true}` + "\n```",
		"fenced_unknown_field":       "```json\n" + strings.Replace(validTarget, `"clarification_needed":false`, `"clarification_needed":false,"confidence":"high"`, 1) + "\n```",
	}
	for name, answer := range answers {
		mu.Lock()
		current = answer
		mu.Unlock()
		loopbackInterpreted, _, loopbackErr := runtime.InterpretQuestion(ctx, principal, decoded.Request)
		env := mustValidateWith(t, answer, testRequest, transportGenkit)
		if (loopbackErr == nil) != env.ProductionAccepts {
			t.Errorf("%s: loopback production accepts=%v (err %v), helper genkit production_accepts=%v", name, loopbackErr == nil, loopbackErr, env.ProductionAccepts)
			continue
		}
		if loopbackErr == nil && (env.Interpreted == nil || !reflect.DeepEqual(loopbackInterpreted, *env.Interpreted)) {
			t.Errorf("%s: loopback and helper decoded different interpretations", name)
		}
		t.Logf("%s: accepted=%v", name, loopbackErr == nil)
	}
}
