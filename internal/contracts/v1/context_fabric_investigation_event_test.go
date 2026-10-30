package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/repopath"
)

// eventAsMap is the event as a generic JSON object, so a mutation can add,
// drop or replace any wire key.
func eventAsMap(t *testing.T, event ContextFabricInvestigationEvent) map[string]any {
	t.Helper()
	data, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	return object
}

// verdicts reports whether the published schema and the Go side each accept
// one serialized event. The Go side is the consumer decode,
// ParseContextFabricInvestigationEventData: a strict decode that also refuses
// a key the decoded event does not carry, Validate, and the result body in
// full.
func verdicts(t *testing.T, object map[string]any) (schemaAccepts, goAccepts bool, goErr error) {
	t.Helper()
	data, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	schemaAccepts = contractcheck.ValidateSerializedStrict("", investigationEventSchemaFile, data) == nil
	_, goErr = ParseContextFabricInvestigationEventData(data)
	return schemaAccepts, goErr == nil, goErr
}

// eventBases is one golden event per type.
func eventBases(goldens investigationEventGoldens) map[ContextFabricInvestigationEventType]ContextFabricInvestigationEvent {
	return map[ContextFabricInvestigationEventType]ContextFabricInvestigationEvent{
		ContextFabricInvestigationEventAccepted:  goldens.single["context_fabric_investigation_event_accepted.v1.json"],
		ContextFabricInvestigationEventStage:     goldens.single["context_fabric_investigation_event_stage_started.v1.json"],
		ContextFabricInvestigationEventHeartbeat: goldens.single["context_fabric_investigation_event_heartbeat.v1.json"],
		ContextFabricInvestigationEventResult:    goldens.single["context_fabric_investigation_event_result.v1.json"],
		ContextFabricInvestigationEventError:     goldens.single["context_fabric_investigation_event_error_model_deadline.v1.json"],
	}
}

// TestInvestigationEventExplicitEmptyFieldsAreRefusedOnBothSides: a
// type-dependent field PRESENT with null, an empty string or (where 0 is not
// a value of the field) zero decodes to the Go zero value, which the Go type
// reads as absent. The schema refuses every such payload, so the Go side must
// too -- for every type and every field, generated.
func TestInvestigationEventExplicitEmptyFieldsAreRefusedOnBothSides(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	empties := map[contextFabricEventField][]any{
		eventFieldReservedResultID: {nil, ""},
		eventFieldSubmissionID:     {nil, ""},
		eventFieldThreadID:         {nil, ""},
		eventFieldStartedAt:        {nil, ""},
		eventFieldBudgetSeconds:    {nil, json.Number("0")},
		eventFieldStage:            {nil, ""},
		eventFieldState:            {nil, ""},
		eventFieldElapsedMS:        {nil},
		eventFieldResult:           {nil},
		eventFieldError:            {nil},
	}
	cases := 0
	for kind, base := range eventBases(goldens) {
		for _, field := range contextFabricEventFields {
			for _, value := range empties[field] {
				object := eventAsMap(t, base)
				object[string(field)] = value
				schemaOK, goOK, err := verdicts(t, object)
				if schemaOK || goOK {
					t.Errorf("%s with %s=%#v: want refused; schema accepted=%t, Go accepted=%t (%v)", kind, field, value, schemaOK, goOK, err)
				}
				cases++
			}
		}
	}
	if cases != 5*17 {
		t.Fatalf("checked %d cases", cases)
	}
}

// TestInvestigationEventValuesBothSidesAccept are the edge values the schema
// accepts; Go must accept each too.
func TestInvestigationEventValuesBothSidesAccept(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	bases := eventBases(goldens)
	cases := []struct {
		name  string
		kind  ContextFabricInvestigationEventType
		key   string
		value any
	}{
		{"started_at at the zero instant", ContextFabricInvestigationEventAccepted, "started_at", "0001-01-01T00:00:00Z"},
		{"started_at with an offset", ContextFabricInvestigationEventAccepted, "started_at", "2026-08-11T18:00:00+02:00"},
		{"elapsed_ms zero", ContextFabricInvestigationEventStage, "elapsed_ms", json.Number("0")},
		{"seq at 2^53-1", ContextFabricInvestigationEventStage, "seq", json.Number("9007199254740991")},
		{"budget_seconds 1", ContextFabricInvestigationEventAccepted, "budget_seconds", json.Number("1")},
		{"a UUID submission_id", ContextFabricInvestigationEventAccepted, "submission_id", "8f14e45f-ceea-467a-9575-0a1b2c3d4e5f"},
	}
	// A reused answer is served exactly as stored (AC-3782-2): its body names
	// the request that first produced it, not this one, and the stream must
	// carry it as the JSON body does.
	reused := eventAsMap(t, bases[ContextFabricInvestigationEventResult])
	reused["result"].(map[string]any)["request_id"] = "request_earlier01"
	if schemaOK, goOK, err := verdicts(t, reused); !schemaOK || !goOK {
		t.Errorf("a reused body naming an earlier request: want accepted; schema accepted=%t, Go accepted=%t (%v)", schemaOK, goOK, err)
	}
	for _, test := range cases {
		object := eventAsMap(t, bases[test.kind])
		object[test.key] = test.value
		schemaOK, goOK, err := verdicts(t, object)
		if !schemaOK || !goOK {
			t.Errorf("%s: want accepted; schema accepted=%t, Go accepted=%t (%v)", test.name, schemaOK, goOK, err)
		}
	}
	// error.v1 details: every typed key at a valid value, and a key error.v1
	// does not name (additionalProperties: true), at any value.
	for name, details := range map[string]map[string]any{
		"every typed key valid": {"reason": "schema_violation", "field": "options.max_items", "limit": "capabilities_budget", "deadline": "request"},
		"reason at 64":          {"reason": strings.Repeat("r", 64)},
		"an untyped key":        {"measured_items": json.Number("41"), "retry_attempted": true, "note": nil},
	} {
		object := eventAsMap(t, bases[ContextFabricInvestigationEventError])
		object["error"].(map[string]any)["error"].(map[string]any)["details"] = details
		if schemaOK, goOK, err := verdicts(t, object); !schemaOK || !goOK {
			t.Errorf("error details %s: want accepted; schema accepted=%t, Go accepted=%t (%v)", name, schemaOK, goOK, err)
		}
	}
}

// donorValues holds one valid wire value for every type-dependent field,
// taken from the goldens, so a mutation that ADDS a field adds a value that
// is valid on its own: the only thing under test is whether the field may be
// there at all.
func donorValues(t *testing.T, goldens investigationEventGoldens) map[contextFabricEventField]any {
	t.Helper()
	donors := map[contextFabricEventField]any{}
	for _, event := range goldens.single {
		for key, value := range eventAsMap(t, event) {
			field := contextFabricEventField(key)
			if slices.Contains(contextFabricEventFields, field) {
				if _, taken := donors[field]; !taken {
					donors[field] = value
				}
			}
		}
	}
	accepted := eventAsMap(t, goldens.single["context_fabric_investigation_event_accepted.v1.json"])
	donors[eventFieldSubmissionID] = "8f14e45f-ceea-467a-9575-0a1b2c3d4e5f"
	donors[eventFieldThreadID] = "thread-0001-ask-dev"
	if _, ok := accepted["started_at"]; !ok {
		t.Fatal("accepted golden has no started_at")
	}
	for _, field := range contextFabricEventFields {
		if _, ok := donors[field]; !ok {
			t.Fatalf("no golden carries %s, so no mutation can add it", field)
		}
	}
	return donors
}

// TestInvestigationEventSchemaAndGoAgreeOnEveryField is the per-type field
// table, checked from BOTH sides and generated rather than hand-picked: for
// every event type and every type-dependent field, a missing required field
// and a field the type may not carry must be rejected by the schema AND by
// Go, and an optional field must be accepted by both. A rule present on one
// side only fails here.
func TestInvestigationEventSchemaAndGoAgreeOnEveryField(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	donors := donorValues(t, goldens)
	bases := eventBases(goldens)
	if len(bases) != len(ContextFabricInvestigationEventTypes()) {
		t.Fatal("a base event is missing for an event type")
	}
	cases := 0
	for _, kind := range ContextFabricInvestigationEventTypes() {
		shape := contextFabricEventShapes[kind]
		base := bases[kind]
		if schemaOK, goOK, err := verdicts(t, eventAsMap(t, base)); !schemaOK || !goOK {
			t.Fatalf("%s base: schema accepts %t, Go accepts %t (%v)", kind, schemaOK, goOK, err)
		}
		for _, field := range contextFabricEventFields {
			object := eventAsMap(t, base)
			_, present := object[string(field)]
			wantAccept := true
			var label string
			switch {
			case slices.Contains(shape.required, field):
				if !present {
					t.Fatalf("%s base lacks required %s", kind, field)
				}
				delete(object, string(field))
				label, wantAccept = "drop required", false
			case slices.Contains(shape.optional, field):
				object[string(field)] = donors[field]
				label = "add optional"
			default:
				if present {
					t.Fatalf("%s base carries forbidden %s", kind, field)
				}
				object[string(field)] = donors[field]
				label, wantAccept = "add forbidden", false
			}
			schemaOK, goOK, err := verdicts(t, object)
			if schemaOK != wantAccept || goOK != wantAccept {
				t.Errorf("%s %s %s: want accepted=%t; schema accepted=%t, Go accepted=%t (%v)", kind, label, field, wantAccept, schemaOK, goOK, err)
			}
			cases++
		}
	}
	if cases != len(ContextFabricInvestigationEventTypes())*len(contextFabricEventFields) {
		t.Fatalf("checked %d cases", cases)
	}
}

// TestInvestigationEventSchemaAndGoAgreeOnValues covers the value rules the
// field table does not: bounds, closed vocabularies, the envelope, and the
// body. Each case is rejected by the schema AND by Go.
func TestInvestigationEventSchemaAndGoAgreeOnValues(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	byType := func(name string) ContextFabricInvestigationEvent { return goldens.single[name] }
	accepted := byType("context_fabric_investigation_event_accepted.v1.json")
	stage := byType("context_fabric_investigation_event_stage_started.v1.json")
	heartbeat := byType("context_fabric_investigation_event_heartbeat.v1.json")
	result := byType("context_fabric_investigation_event_result.v1.json")
	failure := byType("context_fabric_investigation_event_error_model_deadline.v1.json")

	set := func(key string, value any) func(map[string]any) {
		return func(object map[string]any) { object[key] = value }
	}
	nested := func(path []string, value any) func(map[string]any) {
		return func(object map[string]any) {
			node := object
			for _, key := range path[:len(path)-1] {
				node = node[key].(map[string]any)
			}
			node[path[len(path)-1]] = value
		}
	}
	drop := func(path []string) func(map[string]any) {
		return func(object map[string]any) {
			node := object
			for _, key := range path[:len(path)-1] {
				node = node[key].(map[string]any)
			}
			delete(node, path[len(path)-1])
		}
	}
	cases := []struct {
		name   string
		base   ContextFabricInvestigationEvent
		mutate func(map[string]any)
	}{
		{"error envelope without retryable", failure, drop([]string{"error", "error", "retryable"})},
		{"error envelope with retryable null", failure, nested([]string{"error", "error", "retryable"}, nil)},
		{"error envelope with details null", failure, nested([]string{"error", "error", "details"}, nil)},
		{"error envelope without request_id", failure, drop([]string{"error", "request_id"})},
		{"error envelope with error null", failure, nested([]string{"error", "error"}, nil)},
		{"schema_version", stage, set("schema_version", "context_fabric_investigation_event.v2")},
		{"unknown type", stage, set("type", "progress")},
		{"unknown top-level key", stage, set("partial_answer", "x")},
		{"empty request_id", stage, set("request_id", "")},
		{"request_id over 256", stage, set("request_id", strings.Repeat("r", 257))},
		{"seq 0", stage, set("seq", json.Number("0"))},
		{"seq above 2^53-1", stage, set("seq", json.Number("9007199254740992"))},
		{"seq not an integer", stage, set("seq", json.Number("1.5"))},
		{"reserved_result_id under 8", accepted, set("reserved_result_id", "r_12345")},
		{"submission_id under 8", accepted, set("submission_id", "s_12345")},
		{"submission_id with a slash", accepted, set("submission_id", "sub/12345678")},
		{"thread_id with a space", accepted, set("thread_id", "thread 12345678")},
		{"thread_id over 128", accepted, set("thread_id", strings.Repeat("t", 129))},
		{"started_at not a time", accepted, set("started_at", "yesterday")},
		{"budget_seconds 0", accepted, set("budget_seconds", json.Number("0"))},
		{"budget_seconds negative", accepted, set("budget_seconds", json.Number("-5"))},
		{"unknown stage", stage, set("stage", "ranking")},
		{"unknown state", stage, set("state", "running")},
		{"elapsed_ms negative", heartbeat, set("elapsed_ms", json.Number("-1"))},
		{"heartbeat unknown stage", heartbeat, set("stage", "thinking")},
		{"error stage unknown", failure, set("stage", "persistence")},
		{"result body not an object", result, set("result", "answer")},
		{"result body of another schema", result, nested([]string{"result", "schema_version"}, "context_packet.v1")},
		{"result body that names only its identity", result, set("result", map[string]any{"schema_version": ContextFabricInvestigationResultSchema, "request_id": "request_12345678", "result_id": "result_12345678"})},
		{"result body with an unknown key", result, nested([]string{"result", "answer_draft"}, "x")},
		{"result body with a status outside its vocabulary", result, nested([]string{"result", "status"}, "finished")},
		{"error envelope schema_version", failure, nested([]string{"error", "schema_version"}, "error.v2")},
		{"error code outside the vocabulary", failure, nested([]string{"error", "error", "code"}, "stream_lost")},
		{"error message empty", failure, nested([]string{"error", "error", "message"}, "")},
		{"error http_status 200", failure, nested([]string{"error", "error", "http_status"}, json.Number("200"))},
		// error.v1's typed details keys (round-3 F1): each refused value, on
		// both sides.
		{"error details.deadline outside its enum", failure, nested([]string{"error", "error", "details"}, map[string]any{"deadline": "not-a-deadline"})},
		{"error details.deadline null", failure, nested([]string{"error", "error", "details"}, map[string]any{"deadline": nil})},
		{"error details.limit outside its enum", failure, nested([]string{"error", "error", "details"}, map[string]any{"limit": "org_budget"})},
		{"error details.limit not a string", failure, nested([]string{"error", "error", "details"}, map[string]any{"limit": json.Number("3")})},
		{"error details.reason with an upper-case letter", failure, nested([]string{"error", "error", "details"}, map[string]any{"reason": "Schema_violation"})},
		{"error details.reason over 64", failure, nested([]string{"error", "error", "details"}, map[string]any{"reason": strings.Repeat("r", 65)})},
		{"error details.reason not a string", failure, nested([]string{"error", "error", "details"}, map[string]any{"reason": true})},
		{"error details.reason empty", failure, nested([]string{"error", "error", "details"}, map[string]any{"reason": ""})},
		{"error details.field with a slash", failure, nested([]string{"error", "error", "details"}, map[string]any{"field": "options/max_items"})},
		{"error details.field over 64", failure, nested([]string{"error", "error", "details"}, map[string]any{"field": strings.Repeat("f", 65)})},
		{"error details.field null", failure, nested([]string{"error", "error", "details"}, map[string]any{"field": nil})},
	}
	for _, test := range cases {
		object := eventAsMap(t, test.base)
		test.mutate(object)
		schemaOK, goOK, err := verdicts(t, object)
		if schemaOK || goOK {
			t.Errorf("%s: want rejected; schema accepted=%t, Go accepted=%t (%v)", test.name, schemaOK, goOK, err)
		}
	}
}

// TestInvestigationEventCrossFieldRulesAreGoOnly names the rules a JSON
// Schema cannot state, so the asymmetry is recorded rather than discovered:
// the schema accepts each case, Go rejects it.
func TestInvestigationEventCrossFieldRulesAreGoOnly(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	result := goldens.single["context_fabric_investigation_event_result.v1.json"]
	failure := goldens.single["context_fabric_investigation_event_error_model_deadline.v1.json"]

	pretty := result
	var indented bytes.Buffer
	if err := json.Indent(&indented, result.Result, "", "  "); err != nil {
		t.Fatal(err)
	}
	pretty.Result = indented.Bytes()
	if err := pretty.Validate(); err == nil || !strings.Contains(err.Error(), "compact") {
		t.Fatalf("an indented body must be refused for not travelling unchanged, got %v", err)
	}

	cases := []struct {
		name   string
		base   ContextFabricInvestigationEvent
		mutate func(map[string]any)
	}{
		{"envelope request_id differs from the event's", failure, func(object map[string]any) {
			object["error"].(map[string]any)["request_id"] = "req_other_one"
		}},
	}
	for _, test := range cases {
		object := eventAsMap(t, test.base)
		test.mutate(object)
		schemaOK, goOK, err := verdicts(t, object)
		if !schemaOK {
			t.Errorf("%s: the schema rejects it; move the rule to the schema-and-Go table", test.name)
		}
		if goOK {
			t.Errorf("%s: Go accepts it (%v)", test.name, err)
		}
	}
}

// TestErrorCodeVocabularyMatchesTheErrorSchema holds the Go error-code list,
// stream_unavailable included, equal to the published error.v1 enum.
func TestErrorCodeVocabularyMatchesTheErrorSchema(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "jsonschema", "v1", "error.v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties struct {
			Error struct {
				Properties struct {
					Code struct {
						Enum []string `json:"enum"`
					} `json:"code"`
				} `json:"properties"`
			} `json:"error"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	published := slices.Clone(schema.Properties.Error.Properties.Code.Enum)
	vocabulary := slices.Clone(errorCodes)
	slices.Sort(published)
	slices.Sort(vocabulary)
	if !slices.Equal(published, vocabulary) {
		t.Fatalf("error.v1 code enum %v and Go vocabulary %v differ", published, vocabulary)
	}
	if !slices.Contains(published, ErrorCodeStreamUnavailable) {
		t.Fatalf("error.v1 does not publish %q", ErrorCodeStreamUnavailable)
	}
}

// TestInvestigationEventStreamIsPublishedOnTheCreateRoute is the OpenAPI half
// of the parity: the create route publishes the stream media type against
// the event schema, and the 406 refusal against stream_unavailable.
func TestInvestigationEventStreamIsPublishedOnTheCreateRoute(t *testing.T) {
	raw, err := os.ReadFile(repopath.Path(t, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	type schemaRef struct {
		Ref string `json:"$ref"`
	}
	var document struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Ref     string `json:"$ref"`
				Content map[string]struct {
					Schema schemaRef `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
		Components struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						AllOf []struct {
							Ref        string `json:"$ref"`
							Properties struct {
								Error struct {
									Properties struct {
										Code       struct{ Const string } `json:"code"`
										HTTPStatus struct{ Const int }    `json:"http_status"`
										Retryable  struct{ Const *bool }  `json:"retryable"`
									} `json:"properties"`
								} `json:"error"`
							} `json:"properties"`
						} `json:"allOf"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	operation := document.Paths["/api/v1/context-fabric/investigations"]["post"]
	stream, ok := operation.Responses["200"].Content[ContextFabricInvestigationEventStreamMediaType]
	if !ok {
		t.Fatalf("200 does not publish %s", ContextFabricInvestigationEventStreamMediaType)
	}
	if !strings.HasSuffix(stream.Schema.Ref, "/"+investigationEventSchemaFile) {
		t.Fatalf("200 %s schema is %q, want the event schema", ContextFabricInvestigationEventStreamMediaType, stream.Schema.Ref)
	}
	if _, ok := operation.Responses["200"].Content["application/json"]; !ok {
		t.Fatal("200 lost its application/json body")
	}
	const component = "#/components/responses/ContextFabricInvestigationStreamUnavailable"
	if got := operation.Responses["406"].Ref; got != component {
		t.Fatalf("406 is %q, want %q", got, component)
	}
	refusal := document.Components.Responses["ContextFabricInvestigationStreamUnavailable"].Content["application/json"].Schema.AllOf
	if len(refusal) != 2 || !strings.HasSuffix(refusal[0].Ref, "/error.v1.schema.json") {
		t.Fatalf("406 schema is not error.v1 narrowed: %+v", refusal)
	}
	narrowed := refusal[1].Properties.Error.Properties
	if narrowed.Code.Const != ErrorCodeStreamUnavailable || narrowed.HTTPStatus.Const != ContextFabricStreamUnavailableHTTPStatus || narrowed.Retryable.Const == nil || *narrowed.Retryable.Const {
		t.Fatalf("406 schema narrows to %+v, want code %s, status %d, retryable false", narrowed, ErrorCodeStreamUnavailable, ContextFabricStreamUnavailableHTTPStatus)
	}
}

// --- stream grammar ---------------------------------------------------------

func TestInvestigationEventStreamGrammar(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	for _, stream := range [][]ContextFabricInvestigationEvent{goldens.resultStream, goldens.errorStream} {
		if err := ValidateContextFabricInvestigationEventStream(stream); err != nil {
			t.Fatalf("golden stream: %v", err)
		}
	}

	// renumber gives a hand-built stream contiguous seq values, so a case
	// breaks only the rule it names.
	renumber := func(events []ContextFabricInvestigationEvent) []ContextFabricInvestigationEvent {
		out := slices.Clone(events)
		for index := range out {
			out[index].Seq = int64(index + 1)
		}
		return out
	}
	result := goldens.resultStream
	failure := goldens.errorStream
	at := func(events []ContextFabricInvestigationEvent, index int) ContextFabricInvestigationEvent {
		return events[index]
	}
	with := func(event ContextFabricInvestigationEvent, edit func(*ContextFabricInvestigationEvent)) ContextFabricInvestigationEvent {
		edit(&event)
		return event
	}
	cases := []struct {
		name   string
		events []ContextFabricInvestigationEvent
		want   string
	}{
		{"starts with a stage", renumber(result[1:]), "starts with accepted"},
		{"starts at seq 2", append([]ContextFabricInvestigationEvent{with(result[0], func(e *ContextFabricInvestigationEvent) { e.Seq = 2 })}, result[1:]...), "starts at seq 1"},
		{"seq gap", append(slices.Clone(result[:3]), with(result[3], func(e *ContextFabricInvestigationEvent) { e.Seq = 5 })), "grows by one"},
		{"seq repeats", append(slices.Clone(result[:3]), with(result[3], func(e *ContextFabricInvestigationEvent) { e.Seq = 3 })), "grows by one"},
		{"second accepted", renumber(append(slices.Clone(result[:2]), result[0])), "carries accepted once"},
		{"request_id changes", append(slices.Clone(result[:2]), with(result[2], func(e *ContextFabricInvestigationEvent) { e.RequestID = "request_other1" })), "changes request_id"},
		{"elapsed goes back", append(slices.Clone(result[:2]), with(result[2], func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(1) })), "elapsed_ms"},
		{"stage starts while one is open", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), at(result, 3)}), "while stage interpreting is open"},
		{"stage goes back", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 3), at(result, 4), with(at(result, 1), func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(4000) })}), "do not repeat"},
		{"stage repeats", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), at(result, 2), with(at(result, 1), func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(4000) })}), "do not repeat"},
		{"stage completes unopened", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 2)}), "completes while the open stage"},
		{"heartbeat names a closed stage", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), at(result, 2), with(at(result, 6), func(e *ContextFabricInvestigationEvent) { e.Stage = ContextFabricStageInterpreting })}), "heartbeat names stage"},
		{"heartbeat omits the open stage", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), with(at(result, 6), func(e *ContextFabricInvestigationEvent) { e.Stage, e.ElapsedMS = "", int64Ptr(5000) })}), "heartbeat names stage"},
		{"result with a stage open", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), at(result, len(result)-1)}), "result while stage"},
		{"error names another stage", renumber(append(slices.Clone(failure[:len(failure)-1]), with(failure[len(failure)-1], func(e *ContextFabricInvestigationEvent) { e.Stage = ContextFabricStageReadingFacts }))), "the last stage started"},
		{"error names no stage after one started", renumber(append(slices.Clone(failure[:len(failure)-1]), with(failure[len(failure)-1], func(e *ContextFabricInvestigationEvent) { e.Stage = "" }))), "the last stage started"},
		{"error reserved id differs", renumber(append(slices.Clone(failure[:len(failure)-1]), with(failure[len(failure)-1], func(e *ContextFabricInvestigationEvent) { e.ReservedResultID = "result_99999999" }))), "reserved_result_id differs"},
		{"silence over the heartbeat bound", renumber([]ContextFabricInvestigationEvent{at(result, 0), with(at(result, 1), func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(11001) })}), "a heartbeat is due"},
		{"silence over the bound between two events", renumber([]ContextFabricInvestigationEvent{at(result, 0), at(result, 1), with(at(result, 2), func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(4 + 11001) })}), "a heartbeat is due"},
		{"two terminals", renumber(append(slices.Clone(result), result[len(result)-1])), "follows the terminal"},
		{"event after the terminal", renumber(append(slices.Clone(result), result[6])), "follows the terminal"},
	}
	for _, test := range cases {
		err := ValidateContextFabricInvestigationEventStream(test.events)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: want an error containing %q, got %v", test.name, test.want, err)
		}
	}

	// A stream with no terminal is interrupted, and says so by sentinel.
	if err := ValidateContextFabricInvestigationEventStream(result[:len(result)-1]); !errors.Is(err, ErrContextFabricInvestigationStreamInterrupted) {
		t.Fatalf("a stream without its terminal: got %v, want the interrupted sentinel", err)
	}
	if err := ValidateContextFabricInvestigationEventStream(nil); err == nil {
		t.Fatal("an empty stream must be refused")
	}
	// An error before any stage started names no stage; that is valid.
	early := renumber([]ContextFabricInvestigationEvent{result[0], with(failure[len(failure)-1], func(e *ContextFabricInvestigationEvent) { e.Stage = "" })})
	if err := ValidateContextFabricInvestigationEventStream(early); err != nil {
		t.Fatalf("an error before the first stage: %v", err)
	}
	// A heartbeat sooner than the interval is valid: the bound is on silence.
	earlyHeartbeat := renumber([]ContextFabricInvestigationEvent{result[0], result[1], with(result[6], func(e *ContextFabricInvestigationEvent) {
		e.Stage, e.ElapsedMS = ContextFabricStageInterpreting, int64Ptr(5)
	}), result[2], result[len(result)-1]})
	if err := ValidateContextFabricInvestigationEventStream(earlyHeartbeat); err != nil {
		t.Fatalf("an early heartbeat: %v", err)
	}
	// Silence of exactly the interval plus its slack is within the bound.
	bound := renumber([]ContextFabricInvestigationEvent{result[0], with(result[1], func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(11000) }), with(result[2], func(e *ContextFabricInvestigationEvent) { e.ElapsedMS = int64Ptr(22000) }), result[len(result)-1]})
	if err := ValidateContextFabricInvestigationEventStream(bound); err != nil {
		t.Fatalf("silence at the bound: %v", err)
	}
	// Skipping stages is allowed (answer reuse, early clarification).
	skipped := renumber([]ContextFabricInvestigationEvent{result[0], result[1], result[2], result[len(result)-1]})
	if err := ValidateContextFabricInvestigationEventStream(skipped); err != nil {
		t.Fatalf("a stream that skips stages: %v", err)
	}
}

// --- framing ----------------------------------------------------------------

func TestInvestigationEventFramesRoundTrip(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	stream := encodeStreamFrames(t, goldens.resultStream)
	if !bytes.HasPrefix(stream, []byte("id: 1\nevent: accepted\ndata: {")) {
		t.Fatalf("frame layout: %q", stream[:min(len(stream), 64)])
	}
	decoded, err := DecodeContextFabricInvestigationEventFrames(bytes.NewReader(stream), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if again := encodeStreamFrames(t, decoded); !bytes.Equal(again, stream) {
		t.Fatal("decode then encode changed the stream")
	}

	// The reader holds ACR's writer to its own form: every SSE variation the
	// writer never writes is refused (CHAOS-11909 review round 2, F2/F4).
	first := "id: 1\nevent: accepted\ndata: " + string(mustEventData(t, goldens.resultStream[0])) + "\n\n"
	cases := []struct {
		name, body string
		max        int
		want       string
	}{
		{"CRLF line ends", strings.ReplaceAll(first, "\n", "\r\n"), 1 << 20, "carriage return"},
		{"a comment frame", ": keepalive\n\n" + first, 1 << 20, "1 lines"},
		{"no space after id:", strings.Replace(first, "id: 1", "id:1", 1), 1 << 20, "frame line 1"},
		{"no space after event:", strings.Replace(first, "event: accepted", "event:accepted", 1), 1 << 20, "frame line 2"},
		{"no space after data:", strings.Replace(first, "data: {", "data:{", 1), 1 << 20, "frame line 3"},
		{"id differs from seq", strings.Replace(first, "id: 1", "id: 7", 1), 1 << 20, "SSE id"},
		{"event differs from type", strings.Replace(first, "event: accepted", "event: stage", 1), 1 << 20, "SSE event"},
		{"unknown SSE field", strings.Replace(first, "id: 1\n", "id: 1\nretry: 100\n", 1), 1 << 20, "4 lines"},
		{"id twice", strings.Replace(first, "id: 1\n", "id: 1\nid: 1\n", 1), 1 << 20, "4 lines"},
		{"no data", "id: 1\nevent: accepted\n\n", 1 << 20, "2 lines"},
		{"data over two lines", strings.Replace(first, ",\"seq\":1,", ",\ndata: \"seq\":1,", 1), 1 << 20, "4 lines"},
		{"unknown JSON key", strings.Replace(first, "{\"schema_version\"", "{\"partial\":true,\"schema_version\"", 1), 1 << 20, "unknown field"},
		{"two JSON values", strings.Replace(first, "}\n\n", "} {}\n\n", 1), 1 << 20, "trailing"},
		{"a second space after data:", strings.Replace(first, "data: {", "data:  {", 1), 1 << 20, "writer's encoding"},
		{"a key given twice", strings.Replace(first, ",\"seq\":1,", ",\"seq\":1,\"seq\":1,", 1), 1 << 20, "twice"},
		{"an escaped key", strings.Replace(first, "\"type\":", "\"\\u0074ype\":", 1), 1 << 20, "writer's encoding"},
		{"an escaped value", strings.Replace(first, "\"type\":\"accepted\"", "\"type\":\"\\u0061ccepted\"", 1), 1 << 20, "writer's encoding"},
		{"keys in another order", strings.Replace(first, "{\"schema_version\":\"context_fabric_investigation_event.v1\",\"type\":\"accepted\",", "{\"type\":\"accepted\",\"schema_version\":\"context_fabric_investigation_event.v1\",", 1), 1 << 20, "writer's encoding"},
		{"an explicit empty field", strings.Replace(first, ",\"budget_seconds\":120}", ",\"budget_seconds\":120,\"thread_id\":\"\"}", 1), 1 << 20, "thread_id"},
		{"an explicit null field", strings.Replace(first, ",\"budget_seconds\":120}", ",\"budget_seconds\":120,\"stage\":null}", 1), 1 << 20, "stage"},
		{"ends inside a frame", strings.TrimSuffix(first, "\n"), 1 << 20, "ends inside"},
		{"frame over the limit", first, 64, "exceeds"},
	}
	for _, test := range cases {
		if test.body == first {
			if test.max != 64 {
				t.Fatalf("%s: the mutation changed nothing", test.name)
			}
		}
	}
	for _, test := range cases {
		_, err := DecodeContextFabricInvestigationEventFrames(strings.NewReader(test.body), test.max)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: want an error containing %q, got %v", test.name, test.want, err)
		}
	}
}

// TestParseAcceptsEverySpellingDecodeOnlyTheWriters: the consumer decode
// takes any JSON spelling of a valid event; the writer-form decode takes
// only the writer's bytes.
func TestParseAcceptsEverySpellingDecodeOnlyTheWriters(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	data := string(mustEventData(t, goldens.resultStream[0]))
	spellings := map[string]string{
		"keys in another order": strings.Replace(data, `{"schema_version":"context_fabric_investigation_event.v1","type":"accepted",`, `{"type":"accepted","schema_version":"context_fabric_investigation_event.v1",`, 1),
		"an escaped value":      strings.Replace(data, `"type":"accepted"`, `"type":"\u0061ccepted"`, 1),
		"whitespace":            strings.Replace(data, `,"seq":1,`, `, "seq": 1 ,`, 1),
	}
	for name, spelling := range spellings {
		if spelling == data {
			t.Fatalf("%s: the spelling changed nothing", name)
		}
		if _, err := ParseContextFabricInvestigationEventData([]byte(spelling)); err != nil {
			t.Errorf("%s: Parse refused a valid spelling: %v", name, err)
		}
		if _, err := DecodeContextFabricInvestigationEventData([]byte(spelling)); err == nil {
			t.Errorf("%s: Decode accepted bytes the writer does not write", name)
		}
	}
	if _, err := DecodeContextFabricInvestigationEventData([]byte(data)); err != nil {
		t.Fatalf("Decode refused the writer's own bytes: %v", err)
	}
}

func mustEventData(t *testing.T, event ContextFabricInvestigationEvent) []byte {
	t.Helper()
	data, err := MarshalContextFabricInvestigationEventData(event)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestInvestigationEventEncoderRefusesAnInvalidEvent pins fail-closed
// encoding: the writer cannot put an event on the wire that the schema would
// refuse.
func TestInvestigationEventEncoderRefusesAnInvalidEvent(t *testing.T) {
	invalid := ContextFabricInvestigationEvent{SchemaVersion: ContextFabricInvestigationEventSchema, Type: ContextFabricInvestigationEventStage, RequestID: "request_12345678", Seq: 2}
	if _, err := MarshalContextFabricInvestigationEventFrame(invalid); err == nil {
		t.Fatal("a stage event with no stage, state or elapsed_ms was encoded")
	}
	// The writer checks a result body in full (round-3 F2): a body of another
	// contract, one with no result_id, and a compact body with a valid
	// identity whose content its own response contract refuses are all
	// refused at the encoder, so the writer never writes what a consumer and
	// the schema refuse.
	goldens := buildInvestigationEventGoldens(t)
	golden := goldens.single["context_fabric_investigation_event_result.v1.json"]
	reshaped := func(edit func(map[string]any)) string {
		decoder := json.NewDecoder(bytes.NewReader(golden.Result))
		decoder.UseNumber()
		var body map[string]any
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		edit(body)
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	for name, body := range map[string]string{
		"another contract":                `{"schema_version":"context_packet.v1","request_id":"request_12345678","result_id":"result_12345678"}`,
		"no result_id":                    `{"schema_version":"context_fabric_investigation_result.v1","request_id":"request_12345678"}`,
		"not an object":                   `["context_fabric_investigation_result.v1"]`,
		"only an identity":                `{"schema_version":"context_fabric_investigation_result.v1","request_id":"request_12345678","result_id":"result_12345678"}`,
		"a status outside its vocabulary": reshaped(func(body map[string]any) { body["status"] = "finished" }),
		"an unknown key":                  reshaped(func(body map[string]any) { body["answer_draft"] = "x" }),
	} {
		event := golden
		event.Result = json.RawMessage(body)
		if err := event.Validate(); err == nil {
			t.Errorf("%s: Validate accepted a result body it must refuse", name)
		}
		if _, err := MarshalContextFabricInvestigationEventData(event); err == nil {
			t.Errorf("%s: the encoder wrote a result body it must refuse", name)
		}
	}
	// The unchanged golden body, re-encoded the same way, is still written:
	// the refusals above are the edits, not the re-encoding.
	unchanged := golden
	unchanged.Result = json.RawMessage(reshaped(func(map[string]any) {}))
	if _, err := MarshalContextFabricInvestigationEventData(unchanged); err != nil {
		t.Fatalf("the golden body, re-encoded, was refused: %v", err)
	}
	// error.v1 details (round-3 F1): the encoder refuses a typed key at a
	// value error.v1 refuses.
	failure := goldens.single["context_fabric_investigation_event_error_model_deadline.v1.json"]
	envelope := *failure.Error
	envelope.Error.Details = map[string]any{"deadline": "not-a-deadline"}
	failure.Error = &envelope
	if _, err := MarshalContextFabricInvestigationEventData(failure); err == nil {
		t.Error("the encoder wrote an error event whose details.deadline error.v1 refuses")
	}
}

// TestAnEmptyStreamIsInterrupted (round-3 F3): a stream with no event at all
// -- a connection that closed before `accepted` -- is interrupted, the same
// sentinel as a stream that ends before its terminal, through the stream
// check and through the frame reader.
func TestAnEmptyStreamIsInterrupted(t *testing.T) {
	if err := ValidateContextFabricInvestigationEventStream(nil); !errors.Is(err, ErrContextFabricInvestigationStreamInterrupted) {
		t.Fatalf("empty stream: %v, want ErrContextFabricInvestigationStreamInterrupted", err)
	}
	var check ContextFabricInvestigationEventStreamCheck
	if err := check.Finish(); !errors.Is(err, ErrContextFabricInvestigationStreamInterrupted) {
		t.Fatalf("Finish on no events: %v, want ErrContextFabricInvestigationStreamInterrupted", err)
	}
	events, err := DecodeContextFabricInvestigationEventFrames(bytes.NewReader(nil), 1<<20)
	if err != nil {
		t.Fatalf("an empty body is zero frames, not malformed: %v", err)
	}
	if err := ValidateContextFabricInvestigationEventStream(events); !errors.Is(err, ErrContextFabricInvestigationStreamInterrupted) {
		t.Fatalf("empty body: %v, want ErrContextFabricInvestigationStreamInterrupted", err)
	}
}

// TestInvestigationStageVocabularyIsOrdered pins the one stage order and the
// zero Order of a value outside it.
func TestInvestigationStageVocabularyIsOrdered(t *testing.T) {
	want := []ContextFabricInvestigationStage{"interpreting", "resolving_subjects", "reading_facts", "synthesizing", "validating"}
	if !slices.Equal(ContextFabricInvestigationStages(), want) {
		t.Fatalf("stages %v, want %v", ContextFabricInvestigationStages(), want)
	}
	for index, stage := range want {
		if stage.Order() != index+1 {
			t.Fatalf("%s has order %d, want %d", stage, stage.Order(), index+1)
		}
	}
	if ContextFabricInvestigationStage("persistence").Order() != 0 {
		t.Fatal("a value outside the vocabulary has an order")
	}
	for _, kind := range ContextFabricInvestigationEventTypes() {
		if kind.Terminal() != (kind == ContextFabricInvestigationEventResult || kind == ContextFabricInvestigationEventError) {
			t.Fatalf("%s terminal = %t", kind, kind.Terminal())
		}
	}
}

// TestNoEventCarriesInvalidUTF8 (round-4 R4-F1, closed class-wide): JSON and
// SSE are UTF-8. The writer refuses an event with an invalid byte in ANY
// string it carries -- each kind of place a string can sit: a field, a field
// of the error envelope behind its pointer, a details key, a details value
// behind an interface, inside a slice, inside an array, inside a struct value,
// and the raw result body -- and the consumer decode refuses a data line with
// an invalid byte before it decodes anything.
func TestNoEventCarriesInvalidUTF8(t *testing.T) {
	goldens := buildInvestigationEventGoldens(t)
	result := goldens.single["context_fabric_investigation_event_result.v1.json"]
	failure := goldens.single["context_fabric_investigation_event_error_model_deadline.v1.json"]
	const bad = "a\xffb"
	withError := func(edit func(*ErrorEnvelope)) ContextFabricInvestigationEvent {
		event := failure
		envelope := *failure.Error
		envelope.Error.Details = map[string]any{}
		for key, value := range failure.Error.Error.Details {
			envelope.Error.Details[key] = value
		}
		edit(&envelope)
		event.Error = &envelope
		return event
	}
	type detailStruct struct{ Note string }
	cases := map[string]ContextFabricInvestigationEvent{
		"a field (request_id)": func() ContextFabricInvestigationEvent {
			e := goldens.single["context_fabric_investigation_event_stage_started.v1.json"]
			e.RequestID = "request_" + bad
			return e
		}(),
		"the envelope message, behind its pointer": withError(func(e *ErrorEnvelope) { e.Error.Message = bad }),
		"a details key":                   withError(func(e *ErrorEnvelope) { e.Error.Details[bad] = "x" }),
		"a details value":                 withError(func(e *ErrorEnvelope) { e.Error.Details["note"] = bad }),
		"a details value inside a slice":  withError(func(e *ErrorEnvelope) { e.Error.Details["notes"] = []any{"ok", bad} }),
		"a details value inside an array": withError(func(e *ErrorEnvelope) { e.Error.Details["notes"] = [2]string{"ok", bad} }),
		"a details value inside a struct": withError(func(e *ErrorEnvelope) { e.Error.Details["detail"] = detailStruct{Note: bad} }),
		"a details value as raw JSON":     withError(func(e *ErrorEnvelope) { e.Error.Details["raw"] = json.RawMessage(`"` + bad + `"`) }),
		"the result body (a raw JSON field)": func() ContextFabricInvestigationEvent {
			e := result
			at := bytes.Index(e.Result, []byte(`"question":"`)) + len(`"question":"`)
			e.Result = append(append(append(json.RawMessage{}, e.Result[:at]...), 0xff), e.Result[at:]...)
			return e
		}(),
	}
	for name, event := range cases {
		if err := event.Validate(); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
			t.Errorf("%s: Validate = %v, want a UTF-8 refusal", name, err)
		}
		if _, err := MarshalContextFabricInvestigationEventFrame(event); err == nil {
			t.Errorf("%s: the encoder wrote an event with an invalid byte", name)
		}
	}
	// Controls: the same shapes with valid strings are written, so each
	// refusal above is the byte, not the shape.
	for name, event := range map[string]ContextFabricInvestigationEvent{
		"details with a slice, an array, a struct and raw JSON": withError(func(e *ErrorEnvelope) {
			e.Error.Details["notes"] = []any{"ok", "é"}
			e.Error.Details["pair"] = [2]string{"ok", "ü"}
			e.Error.Details["detail"] = detailStruct{Note: "ñ"}
			e.Error.Details["raw"] = json.RawMessage(`"ß"`)
		}),
		"the result body": result,
	} {
		if _, err := MarshalContextFabricInvestigationEventFrame(event); err != nil {
			t.Errorf("control %s: refused: %v", name, err)
		}
	}
	// The consumer decode: an invalid byte anywhere in the data line,
	// including inside a string the decoder would turn into U+FFFD.
	data := mustEventData(t, goldens.single["context_fabric_investigation_event_stage_started.v1.json"])
	at := bytes.Index(data, []byte(`"request_id":"`)) + len(`"request_id":"`)
	invalid := append(append(append([]byte{}, data[:at]...), 0xff), data[at:]...)
	if _, err := ParseContextFabricInvestigationEventData(invalid); err == nil || !strings.Contains(err.Error(), "not valid UTF-8") {
		t.Fatalf("Parse of a data line with an invalid byte = %v, want a UTF-8 refusal", err)
	}
	if _, err := ParseContextFabricInvestigationEventData(data); err != nil {
		t.Fatalf("control: the valid data line was refused: %v", err)
	}
}
