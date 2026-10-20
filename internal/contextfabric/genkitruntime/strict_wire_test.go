package genkitruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-go/llm"
	"github.com/xeipuuv/gojsonschema"
)

func strictSchemas(t *testing.T) map[string]outputSchema {
	t.Helper()
	out := map[string]outputSchema{}
	for name, value := range map[string]any{"interpret": interpretationOutput{}, "synthesize": synthesisOutput{}, "phrase": phrasingOutput{}} {
		schema, err := newOutputSchema(value, true)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = schema
	}
	return out
}

type wireCounts struct{ optionals, unions, depth int }

func countWire(node any, depth int, counts *wireCounts) {
	if depth > counts.depth {
		counts.depth = depth
	}
	switch typed := node.(type) {
	case map[string]any:
		if properties, ok := typed["properties"].(map[string]any); ok {
			required := requiredSet(typed)
			for name, child := range properties {
				if !required[name] {
					counts.optionals++
				}
				countWire(child, depth+1, counts)
			}
		}
		if _, ok := typed["anyOf"]; ok {
			counts.unions++
		}
		if _, ok := typed["type"].([]any); ok {
			counts.unions++
		}
		for _, key := range []string{"items", "anyOf"} {
			countWire(typed[key], depth+1, counts)
		}
	case []any:
		for _, child := range typed {
			countWire(child, depth+1, counts)
		}
	}
}

func hasKey(node any, keys ...string) bool {
	switch typed := node.(type) {
	case map[string]any:
		for key, child := range typed {
			for _, banned := range keys {
				if key == banned && key != "properties" {
					return true
				}
			}
			if key == "properties" {
				for _, property := range child.(map[string]any) {
					if hasKey(property, keys...) {
						return true
					}
				}
				continue
			}
			if hasKey(child, keys...) {
				return true
			}
		}
	case []any:
		for _, child := range typed {
			if hasKey(child, keys...) {
				return true
			}
		}
	}
	return false
}

// Prod logged format_path=tool for every anthropic turn: open maps, an
// optional-property count of 35 and 39 (limit 24), conditional keywords and an
// untyped property all kept the schemas out of structured outputs. Interpret
// and phrase fit, and synthesize fits since CHAOS-10311 folded its repeated
// structures (its first closed form drew HTTP 400 "The compiled grammar is
// too large", req_011CgCX1m6FWErtNJADGMY22, a 400 that no fallback may answer).
func TestAnthropicWireModePerOperation(t *testing.T) {
	want := map[string]string{"interpret": llm.FormatPathSchema, "phrase": llm.FormatPathSchema, "synthesize": llm.FormatPathSchema}
	for name, schema := range strictSchemas(t) {
		var sentBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{}"},{"type":"tool_use","id":"t","name":"` + outputFormatName + `","input":{}}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		provider, err := llm.New(llm.Config{Kind: llm.KindAnthropic, APIKey: llm.NewSecret("k"), BaseURL: server.URL, Model: "claude-haiku-5-5", HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		result, err := provider.Complete(context.Background(), llm.Request{System: "s", Prompt: "p", Format: &llm.JSONFormat{Name: outputFormatName, Schema: schema.anthropicWire()}})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result.FormatPath != want[name] {
			t.Errorf("%s: format_path = %q (%s), want %q", name, result.FormatPath, result.FormatReason, want[name])
		}
		if _, ok := sentBody["output_config"]; ok != (want[name] == llm.FormatPathSchema) {
			t.Errorf("%s: output_config present = %v", name, ok)
		}
	}
}

// The closed form of every schema stays inside the library's fit check.
func TestClosedFormOfEverySchemaFitsTheLibraryCheck(t *testing.T) {
	for name, schema := range strictSchemas(t) {
		var sentBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&sentBody)
			w.Header().Set("content-type", "application/json")
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{}"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		}))
		provider, err := llm.New(llm.Config{Kind: llm.KindAnthropic, APIKey: llm.NewSecret("k"), BaseURL: server.URL, Model: "claude-haiku-5-5", HTTPClient: server.Client()})
		if err != nil {
			t.Fatal(err)
		}
		result, err := provider.Complete(context.Background(), llm.Request{System: "s", Prompt: "p", Format: &llm.JSONFormat{Name: outputFormatName, Schema: schema.strict}})
		server.Close()
		if err != nil {
			t.Fatal(err)
		}
		if result.FormatPath != llm.FormatPathSchema {
			t.Errorf("%s: format_path = %q (%s)", name, result.FormatPath, result.FormatReason)
		}
	}
}

func TestStrictWireStaysInsideTheProviderLimits(t *testing.T) {
	for name, schema := range strictSchemas(t) {
		var counts wireCounts
		countWire(schema.strict, 0, &counts)
		t.Logf("%s: optionals=%d unions=%d depth=%d", name, counts.optionals, counts.unions, counts.depth)
		if counts.optionals != 0 {
			t.Errorf("%s: %d optional properties, want 0", name, counts.optionals)
		}
		if counts.unions > 16 {
			t.Errorf("%s: %d union-typed properties, limit 16", name, counts.unions)
		}
		raw, _ := json.Marshal(schema.strict)
		for _, open := range []string{`"additionalProperties":true`, `"additionalProperties":{`} {
			if contains(string(raw), open) {
				t.Errorf("%s: wire holds an open map (%s)", name, open)
			}
		}
		if hasKey(schema.strict, "if", "then", "else", "not", "allOf") {
			t.Errorf("%s: wire holds a conditional keyword", name)
		}
	}
}

func contains(text, part string) bool { return len(part) > 0 && indexOf(text, part) >= 0 }

func indexOf(text, part string) int {
	for i := 0; i+len(part) <= len(text); i++ {
		if text[i:i+len(part)] == part {
			return i
		}
	}
	return -1
}

func decodeRestored(t *testing.T, schema outputSchema, reply string, into any) {
	t.Helper()
	restored := restoreStrictReply(schema.wire, reply)
	result, err := schema.compiled.Validate(gojsonschema.NewStringLoader(restored))
	if err != nil || !result.Valid() {
		t.Fatalf("restored reply fails the original schema: %v %v\n%s", err, result.Errors(), restored)
	}
	if err := json.Unmarshal([]byte(restored), into); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreOpenMapsReturnsTheOriginalShape(t *testing.T) {
	schema := strictSchemas(t)["interpret"]
	reply := `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current"},"clarification_needed":false,"fact_requirements":[{"kind":"work","parameters":[{"key":"a","value":"b"}]}]}`
	restored := restoreStrictReply(schema.wire, reply)
	var got struct {
		FactRequirements []factRequirementOutput `json:"fact_requirements"`
	}
	if err := json.Unmarshal([]byte(restored), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.FactRequirements) != 1 || got.FactRequirements[0].Parameters["a"] != "b" {
		t.Fatalf("restored = %+v", got)
	}
	repeated := `{"fact_requirements":[{"kind":"work","parameters":[{"key":"a","value":"b"},{"key":"a","value":"c"}]}]}`
	if restoreStrictReply(schema.wire, repeated) != repeated {
		t.Fatal("a repeated key must be left for the local check to refuse")
	}
	if got := restoreStrictReply(schema.wire, "not json"); got != "not json" {
		t.Fatalf("prose changed: %q", got)
	}
}

// Every optional string and array of the interpretation output is a plain Go
// value with omitempty, so "" and [] decode to what an absent property
// decodes to; the nullable ones (time, question_frame, subject_expression)
// are pointers, where null is absent. This is the round trip for all of them
// at once: the wire reply with every optional empty equals the reply with
// every optional absent, after restore and after toDomain.
func TestEmptyWireDefaultsEqualAbsentInterpretation(t *testing.T) {
	schema := strictSchemas(t)["interpret"]
	wireReply := `{"shape":"open","requested_judgment":"j","subject_terms":[],"comparison_terms":[],
	 "time_context":{"axis":"current","as_of":null,"start":null,"end":null},
	 "fact_requirements":[{"kind":"work","parameters":[]}],"clarification_needed":false,"clarification_reason":"",
	 "window_class":"","window_confidence":"","requested_judgment_kind":"","question_family":"","group_kind":"",
	 "scope_anchor_term":"","scope_anchor_kind":"","requested_subject_kind":"","question_frame":null}`
	absentReply := `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current"},"fact_requirements":[{"kind":"work"}],"clarification_needed":false}`
	var fromWire, fromAbsent interpretationOutput
	decodeRestored(t, schema, wireReply, &fromWire)
	if err := json.Unmarshal([]byte(absentReply), &fromAbsent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromWire, fromAbsent) {
		t.Fatalf("wire defaults decode differently from absent:\n%+v\n%+v", fromWire, fromAbsent)
	}
}

func TestEmptyWireDefaultsEqualAbsentSynthesis(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	wireReply := `{"status":"complete","direct_judgment":"j","current_state":"c","strongest_pressures":[],"drivers":[],
	 "remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],
	 "claimed_facts":[{"claim_id":"c1","kind":"work","subject":{"kind":"team","canonical_id":"t","label":"T"},"field":"f",
	   "value":{"kind":"integer","string":"","integer":4,"number":0,"boolean":false},
	   "fact_id":"","derived_from":[],"operation":"","rows":[],"table":null,"time_series_rows":[],"time_series_table":null}],
	 "deterministic_answer":"a","warnings":[],"coverage_disclosures":[]}`
	absentReply := `{"status":"complete","direct_judgment":"j","current_state":"c","strongest_pressures":[],"drivers":[],
	 "remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],
	 "claimed_facts":[{"claim_id":"c1","kind":"work","subject":{"kind":"team","canonical_id":"t","label":"T"},"field":"f","value":{"integer":4}}],
	 "deterministic_answer":"a","warnings":[]}`
	var fromWire, fromAbsent synthesisOutput
	decodeRestored(t, schema, wireReply, &fromWire)
	if err := json.Unmarshal([]byte(absentReply), &fromAbsent); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromWire, fromAbsent) {
		t.Fatalf("wire defaults decode differently from absent:\n%+v\n%+v", fromWire, fromAbsent)
	}
}

// A scalar is where empty is not absent: "" is a valid string answer and 0 and
// false are valid values, so the wire tags the member that counts.
func TestScalarValuesKeepTheirKindOnTheWire(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	cases := map[string]string{
		"empty string": `{"kind":"string","string":"","integer":0,"number":0,"boolean":false}`,
		"zero":         `{"kind":"integer","string":"","integer":0,"number":0,"boolean":false}`,
		"zero number":  `{"kind":"number","string":"","integer":0,"number":0,"boolean":false}`,
		"false":        `{"kind":"boolean","string":"","integer":0,"number":0,"boolean":false}`,
		"null":         `{"kind":"null","string":"","integer":0,"number":0,"boolean":false}`,
	}
	want := map[string]string{
		"empty string": `{"string":""}`, "zero": `{"integer":0}`, "zero number": `{"number":0}`,
		"false": `{"boolean":false}`, "null": `{"null":true}`,
	}
	for name, scalar := range cases {
		reply := `{"status":"complete","direct_judgment":"j","current_state":"c","strongest_pressures":[],"drivers":[],"remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],"deterministic_answer":"a","warnings":[],` +
			`"claimed_facts":[{"claim_id":"c1","kind":"work","subject":{"kind":"team","canonical_id":"t","label":"T"},"field":"f","value":` + scalar + `,"rows":[{"fields":[{"key":"x","value":` + scalar + `}]}]}]}`
		var got struct {
			ClaimedFacts []struct {
				Value json.RawMessage `json:"value"`
				Rows  []struct {
					Fields map[string]json.RawMessage `json:"fields"`
				} `json:"rows"`
			} `json:"claimed_facts"`
		}
		decodeRestored(t, schema, reply, &got)
		if string(got.ClaimedFacts[0].Value) != want[name] || string(got.ClaimedFacts[0].Rows[0].Fields["x"]) != want[name] {
			t.Errorf("%s: value=%s row=%s, want %s", name, got.ClaimedFacts[0].Value, got.ClaimedFacts[0].Rows[0].Fields["x"], want[name])
		}
	}
}

func TestTimeContextWireCarriesTheAxisRulesAsText(t *testing.T) {
	schema := strictSchemas(t)["interpret"]
	properties := schema.strict["properties"].(map[string]any)
	description, _ := properties["time_context"].(map[string]any)["description"].(string)
	if description == "" {
		t.Fatal("the conditional keywords were dropped without their rules reaching the model as text")
	}
	for name, reply := range map[string]string{
		"current with as_of":   `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current","as_of":"2026-01-01T00:00:00Z","start":null,"end":null},"fact_requirements":[],"clarification_needed":false}`,
		"range without bounds": `{"shape":"open","requested_judgment":"j","time_context":{"axis":"range","as_of":null,"start":null,"end":null},"fact_requirements":[],"clarification_needed":false}`,
	} {
		restored := restoreStrictReply(schema.wire, reply)
		result, err := schema.compiled.Validate(gojsonschema.NewStringLoader(restored))
		if err != nil || result.Valid() {
			t.Errorf("%s: the local check must still refuse it", name)
		}
	}
}

// Empty equals absent for every optional property the wire sends as "" or []:
// the Go field is a plain string, slice or map (a decode of "" or [] and of an
// absent property differ only by nil versus empty, which every consumer reads
// through len or range), and every field the wire sends as null is a pointer
// (null decodes to nil, as absent does). A new optional property of another
// Go kind fails here until it is given its own wire form.
func TestEveryOptionalPropertyHasAGoKindThatMakesEmptyEqualAbsent(t *testing.T) {
	visited := 0
	var walk func(path string, goType reflect.Type, node map[string]any)
	walk = func(path string, goType reflect.Type, node map[string]any) {
		for goType.Kind() == reflect.Ptr {
			goType = goType.Elem()
		}
		if isScalarValue(node) {
			return
		}
		switch goType.Kind() {
		case reflect.Slice:
			if items, ok := node["items"].(map[string]any); ok {
				walk(path+"[]", goType.Elem(), items)
			}
		case reflect.Map:
			if value, open := isOpenMap(node); open {
				walk(path+"{}", goType.Elem(), value)
			}
		case reflect.Struct:
			properties, _ := node["properties"].(map[string]any)
			required := requiredSet(node)
			fields := map[string]reflect.StructField{}
			for i := 0; i < goType.NumField(); i++ {
				field := goType.Field(i)
				name := field.Tag.Get("json")
				if comma := indexOf(name, ","); comma >= 0 {
					name = name[:comma]
				}
				fields[name] = field
			}
			for name, child := range properties {
				childNode, _ := child.(map[string]any)
				field, ok := fields[name]
				if !ok {
					continue
				}
				if !required[name] {
					visited++
					kind := field.Type.Kind()
					plain := kind == reflect.String || kind == reflect.Slice || kind == reflect.Map
					if emptyDefault(childNode) || (childNode["type"] == nil && kind == reflect.Slice) || (childNode["type"] == "object" && kind == reflect.Map) {
						if !plain {
							t.Errorf("%s.%s: sent as an empty default but Go kind is %s", path, name, kind)
						}
					} else if kind != reflect.Ptr && kind != reflect.Struct {
						t.Errorf("%s.%s: sent as null but Go kind is %s", path, name, kind)
					} else if kind == reflect.Struct && goType.Name() != "" && field.Type.PkgPath() != "time" {
						t.Errorf("%s.%s: sent as null but Go kind is a non-pointer struct", path, name)
					}
				}
				walk(path+"."+name, field.Type, childNode)
			}
		}
	}
	schemas := strictSchemas(t)
	walk("interpret", reflect.TypeOf(interpretationOutput{}), schemas["interpret"].wire)
	walk("synthesize", reflect.TypeOf(synthesisOutput{}), schemas["synthesize"].wire)
	if visited < 55 {
		t.Fatalf("walked %d optional properties, the schemas hold more", visited)
	}
}

// The Anthropic call of each operation sends the schema chosen for it up
// front: interpretation and synthesis closed, with no retry on the other form.
func TestAnthropicCallsSendTheChosenSchemaPerOperation(t *testing.T) {
	schemas := strictSchemas(t)
	cases := map[string]func(*Runtime) error{
		"interpret": func(r *Runtime) error {
			_, _, err := r.InterpretQuestion(context.Background(), storage.Principal{OrgID: "org_1"}, validRequest())
			return err
		},
		"synthesize": func(r *Runtime) error {
			_, _, err := r.SynthesizeAnswer(context.Background(), storage.Principal{OrgID: "org_1"}, validSynthesisInput())
			return err
		},
	}
	for name, call := range cases {
		client := &scriptedLLM{model: "claude-haiku-5-5", kind: llm.KindAnthropic, replies: []llmReply{{result: llm.Result{Text: "not json"}}}}
		runtime, err := NewWithLLM(Config{
			Provider: "anthropic", Model: "claude-haiku-5-5", InterpretationPromptVersion: "interpret-v1", SynthesisPromptVersion: "synthesis-v1",
			SchemaVersion: "schema-v1", EvaluatorVersion: "eval-v1", Timeout: time.Second, MaxAttempts: 1, MaxInputBytes: 128 << 10,
		}, LLMClients{"claude-haiku-5-5": client})
		if err != nil {
			t.Fatal(err)
		}
		_ = call(runtime)
		sent := client.request(0).Format.Schema
		want := schemas[name].strict
		if !reflect.DeepEqual(sent, want) {
			t.Errorf("%s: the Anthropic call sent a different schema than the one chosen for it", name)
		}
	}
}

// A reply that leaves the closed wire shape is not rewritten into one the
// original schema accepts: the local check must still see the violation.
func TestRestoreLeavesWireShapeViolationsForTheLocalCheck(t *testing.T) {
	interpret := strictSchemas(t)["interpret"]
	extraEntryField := `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current"},"clarification_needed":false,"fact_requirements":[{"kind":"work","parameters":[{"key":"a","value":"b","unexpected":"x"}]}]}`
	missingEntryValue := `{"shape":"open","requested_judgment":"j","time_context":{"axis":"current"},"clarification_needed":false,"fact_requirements":[{"kind":"work","parameters":[{"key":"a"}]}]}`
	for name, reply := range map[string]string{"extra entry field": extraEntryField, "missing entry value": missingEntryValue} {
		if got := restoreStrictReply(interpret.wire, reply); got != reply {
			t.Errorf("%s: reply was rewritten to %s", name, got)
		}
	}
	synthesize := strictSchemas(t)["synthesize"]
	claim := func(scalar string) string {
		return `{"status":"complete","direct_judgment":"j","current_state":"c","strongest_pressures":[],"drivers":[],"remaining_work":[],"readiness_gaps":[],"conflicts":[],"limitations":[],"evidence_ref_ids":[],"deterministic_answer":"a","warnings":[],` +
			`"claimed_facts":[{"claim_id":"c1","kind":"work","subject":{"kind":"team","canonical_id":"t","label":"T"},"field":"f","value":` + scalar + `,"rows":[]}]}`
	}
	for name, scalar := range map[string]string{
		"missing member": `{"kind":"integer","integer":4}`,
		"extra member":   `{"kind":"integer","string":"","integer":4,"number":0,"boolean":false,"extra":1}`,
	} {
		reply := claim(scalar)
		if got := restoreStrictReply(synthesize.wire, reply); got != reply {
			t.Errorf("%s: reply was rewritten to %s", name, got)
		}
	}
}
