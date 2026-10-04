package genkitruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func newTestSuppliedSynthesizer(t *testing.T, config SuppliedSynthesizerConfig) *SuppliedSynthesizer {
	t.Helper()
	synthesizer, err := NewSuppliedSynthesizer(config)
	if err != nil {
		t.Fatalf("NewSuppliedSynthesizer() error = %v", err)
	}
	return synthesizer
}

func suppliedSynthesisFixture(t *testing.T) map[string]any {
	t.Helper()
	output := validSynthesisOutput()
	output.ClaimedFacts = []contextfabric.ClaimedFact{}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return document
}

func marshalFixture(t *testing.T, document map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func requireClosedRejection(t *testing.T, label string, err error) contextfabric.SynthesisRejectionReason {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: Parse() accepted the output, want a rejection", label)
	}
	var rejection *contextfabric.SynthesisRejection
	if !errors.As(err, &rejection) || !errors.Is(err, contextfabric.ErrSynthesisRejected) || !errors.Is(err, contextfabric.ErrModelOutput) {
		t.Fatalf("%s: error = %v, want a *SynthesisRejection that matches ErrSynthesisRejected and ErrModelOutput", label, err)
	}
	reason := contextfabric.SynthesisRejectionReasonOf(err)
	if reason == contextfabric.RejectionReasonUnclassified || !contextfabric.ValidSynthesisRejectionReason(reason) {
		t.Fatalf("%s: reason = %q, want a named member of the closed vocabulary", label, reason)
	}
	return reason
}

func TestSuppliedSynthesizerAcceptsWhatTheModelPathAccepts(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	raw := marshalFixture(t, suppliedSynthesisFixture(t))
	got, err := synthesizer.Parse(raw)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want, err := ParseSynthesisOutput(raw)
	if err != nil {
		t.Fatalf("ParseSynthesisOutput() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse() = %+v, want the draft ParseSynthesisOutput gives: %+v", got, want)
	}
	if got.Status != contextfabric.InvestigationComplete || len(got.Drivers) != 1 {
		t.Fatalf("draft = %+v, want the fixture's status and driver", got)
	}
}

func TestSuppliedSynthesizerRefusesWhatTheSchemaOrTheTypedDecodeRefuses(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	cases := map[string]func(map[string]any){
		"an unknown top-level field": func(d map[string]any) { d["extra_field"] = "x" },
		"an unknown nested field": func(d map[string]any) {
			d["drivers"].([]any)[0].(map[string]any)["extra_field"] = "x"
		},
		"a wrong-type field":             func(d map[string]any) { d["direct_judgment"] = 17 },
		"a wrong-type nested field":      func(d map[string]any) { d["drivers"].([]any)[0].(map[string]any)["confidence"] = "high" },
		"a status outside the enum":      func(d map[string]any) { d["status"] = "excellent" },
		"a missing required field":       func(d map[string]any) { delete(d, "warnings") },
		"a null for an array":            func(d map[string]any) { d["limitations"] = nil },
		"a missing deterministic answer": func(d map[string]any) { d["deterministic_answer"] = "   " },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := suppliedSynthesisFixture(t)
			mutate(document)
			_, err := synthesizer.Parse(marshalFixture(t, document))
			reason := requireClosedRejection(t, name, err)
			if name == "a missing deterministic answer" {
				if reason != contextfabric.RejectionReasonDeterministicAnswerMissing {
					t.Fatalf("reason = %q, want deterministic_answer_missing", reason)
				}
			} else if reason != contextfabric.RejectionReasonOutputSchemaMismatch {
				t.Fatalf("reason = %q, want output_schema_mismatch", reason)
			}
		})
	}
}

func TestSuppliedSynthesizerRefusesOutputThatIsNotOneObject(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	valid := string(marshalFixture(t, suppliedSynthesisFixture(t)))
	for name, raw := range map[string]string{
		"an array":             "[" + valid + "]",
		"trailing content":     valid + `{"a":1}`,
		"not json":             `{"status":`,
		"empty":                ``,
		"a bare string":        `"complete"`,
		"two objects in a row": valid + valid,
	} {
		_, err := synthesizer.Parse([]byte(raw))
		if reason := requireClosedRejection(t, name, err); reason != contextfabric.RejectionReasonOutputSchemaMismatch {
			t.Fatalf("%s: reason = %q, want output_schema_mismatch", name, reason)
		}
	}
}

func TestSuppliedSynthesizerRefusesAnOversizeOutput(t *testing.T) {
	t.Parallel()
	raw := marshalFixture(t, suppliedSynthesisFixture(t))
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{MaxOutputBytes: len(raw) - 1})
	_, err := synthesizer.Parse(raw)
	if reason := requireClosedRejection(t, "over the bound", err); reason != contextfabric.RejectionReasonOutputSchemaMismatch {
		t.Fatalf("reason = %q, want output_schema_mismatch", reason)
	}
	if _, err := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{MaxOutputBytes: len(raw)}).Parse(raw); err != nil {
		t.Fatalf("an output at the bound: Parse() error = %v, want it accepted", err)
	}
	defaulted := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	if defaulted.maxBytes != contractsv1.ContextFabricSuppliedSynthesisMaxBytes {
		t.Fatalf("default bound = %d, want the contract's %d", defaulted.maxBytes, contractsv1.ContextFabricSuppliedSynthesisMaxBytes)
	}
	big := suppliedSynthesisFixture(t)
	big["direct_judgment"] = strings.Repeat("x", contractsv1.ContextFabricSuppliedSynthesisMaxBytes)
	if _, err := defaulted.Parse(marshalFixture(t, big)); err == nil {
		t.Fatal("an output over the contract bound was accepted")
	}
}

func TestSuppliedSynthesizerRefusesAMalformedCoverageDisclosures(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	valid := map[string]any{"detail_id": "cov-01", "text": "x"}
	cases := map[string]any{
		"an unknown member":          []any{map[string]any{"detail_id": "cov-01", "text": "x", "extra": "y"}},
		"a missing detail_id":        []any{map[string]any{"text": "x"}},
		"a missing text":             []any{map[string]any{"detail_id": "cov-01"}},
		"a non-string text":          []any{map[string]any{"detail_id": "cov-01", "text": 17}},
		"a non-string detail_id":     []any{map[string]any{"detail_id": 17, "text": "x"}},
		"a null text":                []any{map[string]any{"detail_id": "cov-01", "text": nil}},
		"a wrongly cased member":     []any{map[string]any{"Detail_ID": "cov-01", "text": "x"}},
		"a non-array":                map[string]any{"detail_id": "cov-01", "text": "x"},
		"a string":                   "cov-01",
		"a non-object element":       []any{"cov-01"},
		"a null element":             []any{nil},
		"a bad element after a good": []any{valid, map[string]any{"detail_id": "cov-02"}},
	}
	for name, value := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := suppliedSynthesisFixture(t)
			document["coverage_disclosures"] = value
			_, err := synthesizer.Parse(marshalFixture(t, document))
			if reason := requireClosedRejection(t, name, err); reason != contextfabric.RejectionReasonOutputSchemaMismatch {
				t.Fatalf("reason = %q, want output_schema_mismatch", reason)
			}
			if !errors.Is(err, errSuppliedSynthesisSchema) {
				t.Fatalf("error = %v, want the fixed schema error", err)
			}
		})
	}
}

func TestSuppliedSynthesizerAcceptsWellFormedCoverageDisclosures(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	cases := map[string]struct {
		set  bool
		the  any
		want int
	}{
		"absent":      {},
		"null":        {set: true, the: nil},
		"empty array": {set: true, the: []any{}},
		"one entry":   {set: true, the: []any{map[string]any{"detail_id": "cov-01", "text": "x"}}, want: 1},
	}
	for name, c := range cases {
		document := suppliedSynthesisFixture(t)
		delete(document, "coverage_disclosures")
		if c.set {
			document["coverage_disclosures"] = c.the
		}
		draft, err := synthesizer.Parse(marshalFixture(t, document))
		if err != nil {
			t.Fatalf("%s: Parse() error = %v", name, err)
		}
		if len(draft.CoverageDisclosures) != c.want || draft.CoverageDisclosuresUndecodable {
			t.Fatalf("%s: disclosures = %+v undecodable = %v, want %d entries, decodable", name, draft.CoverageDisclosures, draft.CoverageDisclosuresUndecodable, c.want)
		}
	}
}

func TestParseSynthesisOutputStaysLenientWhereTheSuppliedParserRefuses(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]any{
		"an unknown member": []any{map[string]any{"detail_id": "cov-01", "text": "x", "extra": "y"}},
		"a non-string text": []any{map[string]any{"detail_id": "cov-01", "text": 17}},
		"a non-array":       "cov-01",
	} {
		document := suppliedSynthesisFixture(t)
		document["coverage_disclosures"] = value
		raw := marshalFixture(t, document)
		if _, err := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{}).Parse(raw); err == nil {
			t.Fatalf("%s: the supplied parser accepted it", name)
		}
		if _, err := ParseSynthesisOutput(raw); err != nil {
			t.Fatalf("%s: ParseSynthesisOutput() error = %v, want the model path to stay lenient", name, err)
		}
	}
}

func TestSuppliedSynthesizerRejectionTextCarriesNothingTheCallerWrote(t *testing.T) {
	t.Parallel()
	synthesizer := newTestSuppliedSynthesizer(t, SuppliedSynthesizerConfig{})
	const marker = "zx-marker-7f3a91"
	document := suppliedSynthesisFixture(t)
	document[marker] = marker
	document["direct_judgment"] = marker
	_, err := synthesizer.Parse(marshalFixture(t, document))
	if err == nil || bytes.Contains([]byte(err.Error()), []byte(marker)) {
		t.Fatalf("error = %v, want a rejection whose text carries none of the caller's text", err)
	}
}
