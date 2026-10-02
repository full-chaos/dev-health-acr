package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

const testRequest = `{"question":"How is the Harbor Lights team doing?","time_context":{"axis":"current"}}`

// validTarget is a fictional, complete, strict-clean interpretation.
const validTarget = `{"shape":"single_subject","requested_judgment":"overall status of the team","subject_terms":["Harbor Lights"],"time_context":{"axis":"current"},"fact_requirements":[{"kind":"status"},{"kind":"health"}],"clarification_needed":false,"requested_subject_kind":"team","question_frame":{"goals":["assess_state"],"subject_expression":{"kind":"named_subject","terms":["Harbor Lights"]},"temporal":"current"}}`

// mustValidate uses the default transport (genkit, the ruled serve path).
func mustValidate(t *testing.T, raw, request string) Envelope {
	t.Helper()
	return mustValidateWith(t, raw, request, "")
}

func mustValidateWith(t *testing.T, raw, request, transport string) Envelope {
	t.Helper()
	env, err := validate(raw, json.RawMessage(request), transport)
	if err != nil {
		t.Fatalf("validate returned a helper error: %v", err)
	}
	return env
}

func hasError(env Envelope, stage, code string) bool {
	for _, e := range env.Errors {
		if e.Stage == stage && e.Code == code {
			return true
		}
	}
	return false
}

func TestValidTargetIsStrictClean(t *testing.T) {
	env := mustValidate(t, validTarget, testRequest)
	if !env.StrictOK || !env.ProductionAccepts || !env.TurnAdmitted || len(env.Errors) != 0 {
		t.Fatalf("valid target not clean: strict=%v accepts=%v admitted=%v errors=%v", env.StrictOK, env.ProductionAccepts, env.TurnAdmitted, env.Errors)
	}
	if env.Semantic == nil || env.Semantic.AcceptedFrame == nil {
		t.Fatal("valid target has no accepted frame")
	}
	named := env.Semantic.AcceptedFrame.SubjectExpression.Named
	if named == nil || named.ExpectedKind == nil || string(*named.ExpectedKind) != "team" {
		t.Fatalf("production backfill of expected_kind from requested_subject_kind did not run: %+v", named)
	}
	if env.Family == nil || env.Family.Family == "" {
		t.Fatal("family resolution did not run")
	}
}

func TestDuplicateKeyDetectedWhereProductionAccepts(t *testing.T) {
	raw := strings.Replace(validTarget, `"shape":"single_subject"`, `"shape":"open","shape":"single_subject"`, 1)
	env := mustValidate(t, raw, testRequest)
	if !reflect.DeepEqual(env.DuplicateKeys, []string{"$.shape"}) {
		t.Fatalf("duplicate_keys = %v", env.DuplicateKeys)
	}
	// encoding/json keeps the last duplicate, so production accepts this.
	if !env.ProductionAccepts {
		t.Fatal("expected production to accept a last-wins duplicate")
	}
	if env.StrictOK || env.CanonicalText != nil {
		t.Fatal("a duplicate-key target must not be strict-clean or canonicalized")
	}
}

func TestUnknownFieldReported(t *testing.T) {
	raw := strings.Replace(validTarget, `"clarification_needed":false`, `"clarification_needed":false,"confidence":"high"`, 1)
	env := mustValidate(t, raw, testRequest)
	if !reflect.DeepEqual(env.UnknownFields, []string{"$.confidence"}) {
		t.Fatalf("unknown_fields = %v", env.UnknownFields)
	}
	if env.SchemaOK || env.ProductionAccepts || env.InterpreterOK {
		t.Fatal("the production schema has additionalProperties=false; the genkit runtime rejects this")
	}
	exchange := mustValidateWith(t, raw, testRequest, transportExchange)
	if exchange.ProductionAccepts || !exchange.ExchangeAccepts {
		t.Fatal("the file-exchange path (no schema check) ignores unknown fields; production_accepts still applies the schema")
	}
}

func TestCaseVariantKeyReported(t *testing.T) {
	raw := strings.Replace(validTarget, `"shape":`, `"Shape":`, 1)
	env := mustValidate(t, raw, testRequest)
	if !reflect.DeepEqual(env.CaseVariantKeys, []string{"$.Shape"}) {
		t.Fatalf("case_variant_keys = %v", env.CaseVariantKeys)
	}
	if env.CanonicalText != nil {
		t.Fatal("a case-variant key must block canonicalization")
	}
}

// TestNamedFrameWithoutTermsFailsRealInvariant plants the defect the I3
// invariant exists to catch and observes the verdict flip on the SAME
// target: with terms the turn is admitted, without them production refuses.
func TestNamedFrameWithoutTermsFailsRealInvariant(t *testing.T) {
	good := mustValidate(t, validTarget, testRequest)
	if !good.TurnAdmitted || good.Frame.Failure != nil {
		t.Fatalf("control target not admitted: %+v", good.Frame)
	}
	planted := strings.Replace(validTarget, `"subject_expression":{"kind":"named_subject","terms":["Harbor Lights"]}`, `"subject_expression":{"kind":"named_subject"}`, 1)
	if planted == validTarget {
		t.Fatal("defect was not planted")
	}
	bad := mustValidate(t, planted, testRequest)
	if bad.TurnAdmitted || bad.StrictOK {
		t.Fatal("frame with no terms was admitted")
	}
	if bad.Frame == nil || bad.Frame.Failure == nil || bad.Frame.Failure.Invariant != "i3" || bad.Frame.Failure.Detail != "no_terms" {
		t.Fatalf("expected real invariant i3/no_terms, got %+v", bad.Frame)
	}
	if !bad.Frame.Gate.Refuses || bad.Semantic.AcceptedFrame != nil {
		t.Fatal("refused frame must refuse the gate and carry no accepted frame")
	}
	if !bad.ProductionAccepts {
		t.Fatal("the flat interpretation is still accepted; only the turn is refused")
	}
}

func TestDomainAndDecodeFailuresAreData(t *testing.T) {
	missingReason := `{"shape":"open","requested_judgment":"status","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":true}`
	env := mustValidate(t, missingReason, testRequest)
	if !env.DecodeOK || env.DomainOK || env.RejectionReason != "clarification_reason_missing" {
		t.Fatalf("decode=%v domain=%v reason=%q", env.DecodeOK, env.DomainOK, env.RejectionReason)
	}
	wrongType := `{"shape":"open","requested_judgment":"status","subject_terms":"Harbor Lights","time_context":{"axis":"current"},"fact_requirements":[],"clarification_needed":false}`
	env = mustValidate(t, wrongType, testRequest)
	if env.DecodeOK || !hasError(env, stageDecode, "decode_error") {
		t.Fatalf("wrong-typed field should fail decode: %+v", env.Errors)
	}
	env = mustValidate(t, "not json", testRequest)
	if env.JSONOK || env.Semantic != nil || env.ProductionAccepts || !hasError(env, stageJSON, "genkit_no_json") {
		t.Fatalf("non-JSON should fail at json stage (genkit): %+v", env.Errors)
	}
	env = mustValidateWith(t, "not json", testRequest, transportExchange)
	if env.JSONOK || env.Semantic != nil || env.ProductionAccepts || !hasError(env, stageJSON, "json_invalid") {
		t.Fatalf("non-JSON should fail at json stage (exchange): %+v", env.Errors)
	}
	env = mustValidateWith(t, "```json\n"+validTarget+"\n```", testRequest, transportExchange)
	if env.JSONOK || !env.MarkdownFenced || !hasError(env, stageJSON, "json_markdown_fenced") {
		t.Fatal("the exchange transport must report a fenced answer, not repair it")
	}
}

func TestSilentNormalizationReported(t *testing.T) {
	raw := strings.Replace(validTarget, `"subject_terms":["Harbor Lights"]`, `"subject_terms":["Harbor Lights","Harbor Lights"],"requested_judgment_kind":"vibes"`, 1)
	env := mustValidate(t, raw, testRequest)
	if !env.ProductionAccepts {
		t.Fatal("production accepts duplicate terms and an out-of-set judgment kind")
	}
	if env.SanitizeClean || env.StrictOK {
		t.Fatal("silent normalization must make the target not strict-clean")
	}
	if !hasError(env, stageSanitize, "subject_terms_normalized") || !hasError(env, stageSanitize, "requested_judgment_kind_unrecognized") {
		t.Fatalf("missing normalization findings: %+v", env.Errors)
	}
}

func TestRenderEqualsProductionPayload(t *testing.T) {
	request := `{"question":"Why is Harbor Lights slower?","conversation":[{"turn_id":"t1","role":"user","content":"How is Harbor Lights doing?","created_at":"2026-09-01T10:00:00Z"}],"requested_scope":{"subject_hints":[{"kind":"team","label":"Harbor Lights","source":"ui_selection"}]},"time_context":{"axis":"current"},"prior_subject_receipts":[{"result_id":"res_0000demo01","receipt_id":"rcpt_0000demo01"}]}`
	out, err := render(json.RawMessage(request))
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRequest(json.RawMessage(request))
	if err != nil {
		t.Fatal(err)
	}
	want, err := genkitruntime.BuildInterpretationPrompt(decoded.Request, genkitruntime.DefaultExchangeMaxInputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if out.UserPayload != want || !out.RequestOK {
		t.Fatalf("render mismatch or invalid request: ok=%v err=%q", out.RequestOK, out.RequestError)
	}
	if !strings.Contains(out.UserPayload, `"subject_hints":[`) {
		t.Fatal("production renders subject_hints at the top level too")
	}
}

func TestRenderRejectsFieldsTheModelNeverSees(t *testing.T) {
	for _, request := range []string{
		`{"question":"q","time_context":{"axis":"current"},"expected_kinds":["team"]}`,
		`{"question":"q","question":"r","time_context":{"axis":"current"}}`,
		`{"question":"q","time_context":{"axis":"current"},"requested_scope":{"subject_hints":[{"kind":"team","label":"x","source":"s","extra":1}]}}`,
	} {
		if _, err := render(json.RawMessage(request)); err == nil {
			t.Errorf("render accepted %s", request)
		}
	}
	out, err := render(json.RawMessage(`{"question":"q","time_context":{"axis":"range"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.RequestOK || out.RequestError == "" {
		t.Fatal("a request production would refuse before the model call must be reported")
	}
}

func TestSystemPromptMatchesGolden(t *testing.T) {
	golden, err := os.ReadFile("../../../internal/contextfabric/genkitruntime/testdata/interpretation_system_prompt.golden")
	if err != nil {
		t.Fatal(err)
	}
	info := systemPromptInfo()
	if info.Text != string(golden) || info.SHA256 != sha256Hex(golden) {
		t.Fatalf("system prompt differs from the production golden (%d vs %d bytes)", info.Bytes, len(golden))
	}
}

func semanticJSON(t *testing.T, env Envelope) string {
	t.Helper()
	encoded, err := json.Marshal(env.Semantic)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestCanonicalRoundTrip checks that canonical_text decodes to the same
// semantic projection as the original, that it is a fixed point, and that
// equivalent spellings canonicalize identically.
func TestCanonicalRoundTrip(t *testing.T) {
	rangeRequest := `{"question":"What changed for Harbor Lights in July?","time_context":{"axis":"current"}}`
	variants := [][2]string{
		{validTarget, testRequest},
		// Same meaning, different spelling: key order, whitespace, empty
		// omitempty fields and an explicit empty parameters map.
		{`{ "question_frame": {"temporal":"current","subject_expression":{"terms":["Harbor Lights"],"kind":"named_subject"},"goals":["assess_state"],"emphasis":[]},
		   "clarification_needed": false, "fact_requirements": [{"kind":"status","parameters":{}},{"kind":"health"}],
		   "time_context": {"axis":"current"}, "subject_terms": ["Harbor Lights"], "comparison_terms": [], "group_kind": "",
		   "requested_judgment": "overall status of the team", "shape": "single_subject", "requested_subject_kind": "team" }`, testRequest},
		{`{"shape":"single_subject","requested_judgment":"change over the period","subject_terms":["Harbor Lights"],"time_context":{"axis":"range","start":"2026-07-01T00:00:00+02:00","end":"2026-07-31T23:59:59Z"},"fact_requirements":[{"kind":"flow"}],"clarification_needed":false,"question_frame":{"goals":["explain_change"],"subject_expression":{"kind":"named_subject","terms":["Harbor Lights"]},"temporal":"bounded_window"}}`, rangeRequest},
	}
	canonicalOfValid := ""
	for i, variant := range variants {
		env := mustValidate(t, variant[0], variant[1])
		if env.CanonicalText == nil {
			t.Fatalf("variant %d: no canonical text: %+v", i, env.Errors)
		}
		again := mustValidate(t, *env.CanonicalText, variant[1])
		if again.CanonicalText == nil || *again.CanonicalText != *env.CanonicalText {
			t.Fatalf("variant %d: canonical text is not a fixed point", i)
		}
		if semanticJSON(t, env) != semanticJSON(t, again) {
			t.Fatalf("variant %d: canonical text changed the semantic projection\n before %s\n after  %s", i, semanticJSON(t, env), semanticJSON(t, again))
		}
		if i == 0 {
			canonicalOfValid = *env.CanonicalText
		}
		if i == 1 && *env.CanonicalText != canonicalOfValid {
			t.Fatalf("equivalent spellings canonicalized differently\n %s\n %s", canonicalOfValid, *env.CanonicalText)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(*env.CanonicalText)); err != nil || compact.String() != *env.CanonicalText {
			t.Fatalf("variant %d: canonical text is not compact JSON", i)
		}
	}
	if validTarget != canonicalOfValid {
		t.Fatalf("the declaration-ordered compact target is already canonical; got\n %s", canonicalOfValid)
	}
}

func TestSemanticTimeDefaultFlag(t *testing.T) {
	raw := strings.Replace(validTarget, `"time_context":{"axis":"current"}`, `"time_context":{"axis":""}`, 1)
	env := mustValidate(t, raw, testRequest)
	if env.Semantic == nil || env.Semantic.Flat == nil || !env.Semantic.Flat.TimeContextDefaulted {
		t.Fatal("empty axis must be reported as defaulted from the request")
	}
	if env.Semantic.Flat.TimeContext.Axis != "current" {
		t.Fatalf("toDomain default-fill not reflected: %+v", env.Semantic.Flat.TimeContext)
	}
	control := mustValidate(t, validTarget, testRequest)
	if control.Semantic.Flat.TimeContextDefaulted {
		t.Fatal("an explicit axis is not defaulted")
	}
}
