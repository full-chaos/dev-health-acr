package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/sidecar"
)

// CHAOS-7231, and the class of CHAOS-4867: the schema a client validates a
// request against and the code that answers the request must accept and refuse
// the SAME integers. The class has three known members:
//
//   - a published minimum that the Go validator does not share (run_operation
//     max_bytes, minimum 1 against Go's 0);
//   - JSON Schema's "integer" is any number without a fractional part, so 0.0,
//     1.0 and 1e2 pass a schema-validating client while encoding/json refuses
//     them for a Go int;
//   - a bound the handler enforces that the schema states differently (read_facts
//     max_bytes: Go takes 0 as the default, the schema refused it; the same for
//     every optional integer whose Go field reads a zero as "not set").
//
// This test derives EVERY integer field of EVERY tool schema from what a client
// receives in tools/list (no field list, no skip) and, for each, drives a fixed
// set of values through both the schema and the real tools/call path.

// publishedIntegerField is one integer property of a tool's published input schema.
type publishedIntegerField struct {
	path       []string // object keys; "[]" is a list element
	boundaries []int64  // every minimum, maximum and const stated at the field
	// zeroDefault: the published description says 0 means the default.
	zeroDefault bool
}

// zeroDefaultPhrase is how a published integer field says 0 is the default.
var zeroDefaultPhrase = regexp.MustCompile(`\b0 (means not set|or absent means the default)`)

// discoverPublishedIntegerFields walks a decoded schema the way a validator does:
// properties, items, local $refs and the allOf/anyOf/oneOf branches.
func discoverPublishedIntegerFields(root, node map[string]any, at []string, seen map[string]bool) []publishedIntegerField {
	if ref, ok := node["$ref"].(string); ok {
		pointer, isLocal := strings.CutPrefix(ref, "#/")
		if !isLocal || seen[ref] {
			return nil
		}
		var target any = root
		for _, part := range strings.Split(pointer, "/") {
			obj, ok := target.(map[string]any)
			if !ok {
				return nil
			}
			target = obj[part]
		}
		targetNode, ok := target.(map[string]any)
		if !ok {
			return nil
		}
		next := map[string]bool{ref: true}
		for k := range seen {
			next[k] = true
		}
		return discoverPublishedIntegerFields(root, targetNode, at, next)
	}
	var out []publishedIntegerField
	if node["type"] == "integer" {
		description, _ := node["description"].(string)
		out = append(out, publishedIntegerField{path: append([]string{}, at...), boundaries: collectBoundaries(node), zeroDefault: zeroDefaultPhrase.MatchString(description)})
	}
	if props, ok := node["properties"].(map[string]any); ok {
		names := make([]string, 0, len(props))
		for name := range props {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if sub, ok := props[name].(map[string]any); ok {
				out = append(out, discoverPublishedIntegerFields(root, sub, append(append([]string{}, at...), name), seen)...)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		out = append(out, discoverPublishedIntegerFields(root, items, append(append([]string{}, at...), "[]"), seen)...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[keyword].([]any); ok {
			for _, branch := range branches {
				if b, ok := branch.(map[string]any); ok {
					out = append(out, discoverPublishedIntegerFields(root, b, at, seen)...)
				}
			}
		}
	}
	return out
}

// collectBoundaries gathers every number a field's own constraints name.
func collectBoundaries(node map[string]any) []int64 {
	var out []int64
	for _, key := range []string{"minimum", "maximum", "const", "exclusiveMinimum", "exclusiveMaximum"} {
		if f, ok := node[key].(float64); ok {
			out = append(out, int64(f))
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[keyword].([]any); ok {
			for _, branch := range branches {
				if b, ok := branch.(map[string]any); ok {
					out = append(out, collectBoundaries(b)...)
				}
			}
		}
	}
	return out
}

// integerBoundGrid holds every bound the published integer fields use, each
// with its neighbours, so a schema that lost or moved a floor or a ceiling is
// still probed at the handler's real bound.
var integerBoundGrid = []int64{2, 3, 49, 50, 51, 59, 60, 61, 99, 100, 101, 199, 200, 201, 249, 250, 251, 364, 365, 366, 499, 500, 501, 4095, 4096, 4097, 8191, 8192, 8193, 15999, 16000, 16001, 262143, 262144, 262145, 1048575, 1048576, 1048577}

// integerSpellings are values in unusual notations. Each is decided on its
// value: 0 and 1 and their exact spellings (the round 2 reproductions among
// them), out-of-range and fractional spellings that both sides must refuse.
// (Not here: 1e-999999999 and 1.000...0001. They are not integers, but the
// schema validator reads a number as a float64, which underflows the first to 0
// and rounds the second to 1, so its verdict on them is not exact;
// TestClassifyIntegerDecidesOnTheExactValue pins the exact decision.)
var integerSpellings = []string{
	"0e31", "10000000000000000000000000000000e-31", "1.000000000000000000000000000000000000000000",
	"0e999999999", "0e-999999999", "0.000e5", "-0e5", "-0", "0e0",
	"1e-0", "1.0e0", "1." + strings.Repeat("0", 60) + "e0", "1" + strings.Repeat("0", 31) + "e-31",
	"10e-1", "100e-2", "0.1e1", "1E+0", "1e+0", "1.0E-0",
	"1e400", "1e999999999", "1e-1", "0.5e1", "1" + strings.Repeat("0", 1000),
	"1." + strings.Repeat("0", 999),
}

// integerCells is the value set for one field, as JSON text: the values the
// class is about (0, integral floats, fractions, a string, a boolean), and each
// stated boundary with its neighbours and its integral-float spelling. null is
// not a cell: every handler reads a null optional field as absent, by design.
func integerCells(f publishedIntegerField) []string {
	cells := []string{"0", "0.0", "1", "1.0", "1e0", "-1", "1.5", "-0.5", `"1"`, "true"}
	seen := map[string]bool{}
	for _, c := range cells {
		seen[c] = true
	}
	add := func(text string) {
		if !seen[text] {
			seen[text] = true
			cells = append(cells, text)
		}
	}
	// The handlers state bounds the schema might not (that is the drift this
	// test hunts), so the grid also straddles every bound any tool publishes.
	for _, b := range integerBoundGrid {
		add(strconv.FormatInt(b, 10))
	}
	// The notation axis: a schema-valid integer can be spelled with any number
	// of digits, zeros and any exponent, and the decision is on the value.
	for _, spelling := range integerSpellings {
		add(spelling)
	}
	for _, b := range f.boundaries {
		for _, v := range []int64{b - 1, b, b + 1} {
			add(strconv.FormatInt(v, 10))
		}
		add(strconv.FormatInt(b, 10) + "." + strings.Repeat("0", 999)) // a 1000-digit spelling of the bound
		add(strconv.FormatInt(b, 10) + ".0")
		add(strconv.FormatInt(b-1, 10) + ".0")
		add(strconv.FormatInt(b+1, 10) + ".0")
	}
	return cells
}

// instanceWith returns base with the value (raw JSON text) placed at path,
// creating the objects and one-element lists on the way.
func instanceWith(base map[string]any, path []string, value string) map[string]any {
	out := cloneJSON(base).(map[string]any)
	var cur any = out
	for i, key := range path {
		last := i == len(path)-1
		switch node := cur.(type) {
		case map[string]any:
			if last {
				node[key] = json.RawMessage(value)
				return out
			}
			next, ok := node[key]
			if !ok || next == nil {
				if path[i+1] == "[]" {
					next = []any{}
				} else {
					next = map[string]any{}
				}
				node[key] = next
			}
			cur = next
		case []any:
			// "[]": the first element stands for every element.
			if len(node) == 0 {
				panic("instanceWith: an empty list at " + strings.Join(path[:i], "."))
			}
			if last {
				node[0] = json.RawMessage(value)
				return out
			}
			cur = node[0]
		}
	}
	return out
}

func cloneJSON(v any) any {
	data, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

// integerToolRig is a hosted-mode MCP server that advertises every data and
// answer tool, over an in-memory session, in front of a hosted API that answers
// every tool's route with a valid response and records what it was sent.
type integerToolRig struct {
	session *mcpsdk.ClientSession
	mu      sync.Mutex
	calls   int
	path    string
	body    []byte
}

// last returns how many requests reached the hosted API and the last one.
func (r *integerToolRig) last() (int, string, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls, r.path, r.body
}

// newIntegerToolRig starts the rig. diagnostics receives the server's JSON log
// at Debug.
func newIntegerToolRig(t *testing.T, diagnostics io.Writer) *integerToolRig {
	t.Helper()
	rig := &integerToolRig{}
	readFacts, readRelationships := readFactsExample(t), readRelationshipsExample(t)
	hosted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rig.mu.Lock()
		rig.calls++
		rig.path, rig.body = r.URL.Path, raw
		rig.mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/context-fabric/data/catalog":
			dtRaw(w, http.StatusOK, dtCatalogBody)
		case "/api/v1/context-fabric/data/subjects":
			dtRaw(w, http.StatusOK, dtFindBody)
		case "/api/v1/context-fabric/data/operations":
			dtRaw(w, http.StatusOK, dtOpBody)
		case "/api/v1/context-fabric/data/graphql":
			dtRaw(w, http.StatusOK, dtGQLBody)
		case "/api/v1/context-fabric/data/facts":
			dtRaw(w, http.StatusOK, string(readFacts))
		case "/api/v1/context-fabric/data/relationships":
			dtRaw(w, http.StatusOK, string(readRelationships))
		case "/api/v1/context-fabric/investigations":
			writeJSONFixture(t, w, http.StatusOK, parityResult())
		case "/api/v1/agent-context/context-packets":
			var received contractsv1.ContextPacketRequest
			_ = json.Unmarshal(raw, &received)
			writeJSONFixture(t, w, http.StatusOK, validContextPacketFixture(received.RequestID))
		default:
			writeErrorFixture(t, w, http.StatusNotFound, "not_found", false)
		}
	}))
	t.Cleanup(hosted.Close)

	cfg := fixtureConfig(t, hosted)
	client, err := sidecar.NewClient(cfg, fixedCredentialSource(fixtureToken(0xAB)))
	if err != nil {
		t.Fatal(err)
	}
	caps := validCapabilitiesFixture()
	caps.EnabledTools = []string{
		toolContextForTask, toolSourceEvidence, toolInvestigateQuestion, toolInvestigationResult,
		toolReadFacts, toolReadRelationships, toolDataCatalog, toolFindSubjects, toolRunOperation, toolGraphQLQuery,
	}
	boot := &Bootstrap{Config: cfg, Client: client, Capabilities: caps}
	processConfig, caller := boot.split(diagnostics)
	processConfig.transport = TransportHTTP
	processConfig.diagnostics = newDiagnosticsLogger(diagnostics, slog.LevelDebug)
	rig.session = connectedClientForCaller(t, processConfig, caller)
	return rig
}

func TestEveryPublishedIntegerFieldIsHandledAsItsSchemaSays(t *testing.T) {
	rig := newIntegerToolRig(t, io.Discard)
	session := rig.session

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) < 10 {
		t.Fatalf("tools/list returned %d tools, want every advertised tool", len(listed.Tools))
	}

	fieldsChecked, cellsChecked := 0, 0
	for _, tool := range listed.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(encoded, &raw); err != nil {
			t.Fatal(err)
		}
		fields := discoverPublishedIntegerFields(raw, raw, nil, map[string]bool{})
		if len(fields) == 0 {
			continue
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(encoded, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		examples, _ := raw["examples"].([]any)
		if len(examples) == 0 {
			t.Fatalf("%s: the input schema has no example to build a valid request around", tool.Name)
		}
		base, ok := examples[0].(map[string]any)
		if !ok {
			t.Fatalf("%s: examples[0] is not an object", tool.Name)
		}
		// A hosted server cannot detect a workspace, so the example of
		// context_for_task needs the repository the tool then requires. This is
		// the only precondition the example lacks; the integer fields and their
		// values are all derived.
		if tool.Name == toolContextForTask {
			base = cloneJSON(base).(map[string]any)
			base["repository"] = map[string]any{"slug": "acme/billing"}
		}

		// call returns whether the request got past argument handling: the
		// hosted API was reached. Anything the handler refuses (decode,
		// validation) never gets there.
		// call returns whether the request was ANSWERED: it reached the hosted API
		// and the tool result is not an error. The hosted API answers every route
		// with a valid response, so an error result is a refusal of the request
		// (decode, validation, a bound), never a stub artefact.
		var lastOutcome string // what the last call answered, for a failure message
		call := func(args map[string]any) bool {
			data, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			before, _, _ := rig.last()
			result, callErr := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool.Name, Arguments: json.RawMessage(data)})
			lastOutcome = fmt.Sprintf("error=%v", callErr)
			if callErr == nil && result != nil && len(result.Content) > 0 {
				if text, ok := result.Content[0].(*mcpsdk.TextContent); ok {
					lastOutcome = fmt.Sprintf("isError=%v text=%q", result.IsError, text.Text)
				}
			}
			after, _, _ := rig.last()
			return callErr == nil && result != nil && !result.IsError && after > before
		}
		schemaAccepts := func(args map[string]any) bool {
			data, _ := json.Marshal(args)
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				// A number beyond float64 (1e400): it cannot satisfy any
				// maximum a schema states, so the schema refuses it.
				return false
			}
			return resolved.Validate(value) == nil
		}

		if !schemaAccepts(base) || !call(base) {
			t.Fatalf("%s: the schema's own example is not accepted by both the schema and the tools/call path (%s): the cells below would prove nothing", tool.Name, lastOutcome)
		}
		for _, field := range fields {
			fieldsChecked++
			name := tool.Name + " " + strings.Join(field.path, ".")
			accepted := 0
			for _, cell := range integerCells(field) {
				args := instanceWith(base, field.path, cell)
				bySchema, byCall := schemaAccepts(args), call(args)
				cellsChecked++
				if bySchema {
					accepted++
				}
				if bySchema != byCall {
					t.Errorf("%s = %.70s (%d chars): the published schema %s it, tools/call %s it (%.120s)", name, cell, len(cell), verdict(bySchema), verdict(byCall), lastOutcome)
				}
			}
			if accepted == 0 {
				t.Errorf("%s: no value was accepted by the schema: the instance built around the field is not valid, so the cells prove nothing", name)
			}
		}
	}
	if fieldsChecked < 14 {
		t.Fatalf("only %d integer fields were derived from tools/list: the derivation is broken, not the tools", fieldsChecked)
	}
	t.Logf("checked %d integer fields, %d values, against the schema and the tools/call path", fieldsChecked, cellsChecked)
	_ = fmt.Sprint
}

func verdict(accepted bool) string {
	if accepted {
		return "accepts"
	}
	return "refuses"
}

// Each integer decision is one Debug record: the tool, the field and the
// decision, and never the literal the client sent.
func TestIntegerMiddlewareLogsEachDecisionAtDebugWithoutTheLiteral(t *testing.T) {
	var log bytes.Buffer
	session := newIntegerToolRig(t, &log).session
	call := func(tool string, args string) {
		t.Helper()
		if _, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool, Arguments: json.RawMessage(args)}); err != nil {
			t.Fatal(err)
		}
	}
	call(toolRunOperation, `{"operation":"hotspots","max_bytes":4097.0}`)
	call(toolRunOperation, `{"operation":"hotspots","max_bytes":1e999999999}`)
	call(toolReadFacts, `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"team:1"}],"window":{"mode":"trailing","days":1.5},"max_bytes":8192}`)

	type record struct {
		Msg      string `json:"msg"`
		Level    string `json:"level"`
		Tool     string `json:"tool"`
		Field    string `json:"field"`
		Decision string `json:"decision"`
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.Msg != "mcp integer argument decided" {
			continue
		}
		if r.Level != "DEBUG" {
			t.Errorf("a decision was logged at %s, want DEBUG", r.Level)
		}
		got[r.Tool+" "+r.Field+" "+r.Decision] = "seen"
	}
	for _, want := range []string{
		"run_operation max_bytes rewritten",
		"run_operation max_bytes refused-range",
		"read_facts window.days refused-nonintegral",
		"read_facts max_bytes passed",
	} {
		if got[want] == "" {
			t.Errorf("no Debug record %q in %v", want, got)
		}
	}
	scanned := logWithoutTimestamps(log.String())
	for _, literal := range []string{"4097.0", "1e999999999", "1.5"} {
		if strings.Contains(scanned, literal) {
			t.Errorf("the log carries the literal %q", literal)
		}
	}
}

// logWithoutTimestamps keeps each record's message and attributes and drops
// its "time", whose digits can spell a literal such as 1.5 by chance.
func logWithoutTimestamps(raw string) string {
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &fields); err != nil {
			kept = append(kept, line)
			continue
		}
		delete(fields, "time")
		body, err := json.Marshal(fields)
		if err != nil {
			kept = append(kept, line)
			continue
		}
		kept = append(kept, string(body))
	}
	return strings.Join(kept, "\n")
}

// A field whose published schema says 0 means the default is left out of the
// request the server forwards, never forwarded as 0: the hosted route may treat
// a 0 as a value (a trailing window of 0 days) where omission means "not set".
// The set of such fields is read from the published descriptions, not listed.
//
// Where a handler forwards the field at the path it arrived at (read_facts,
// read_relationships, the data tools), the zero must be gone from that path.
// Where it maps the field elsewhere and fills its own default (context_for_task
// sends budget.* as options.*), no key of that name may carry a 0 anywhere.
func TestZeroDefaultIntegerFieldsAreOmittedFromTheForwardedRequest(t *testing.T) {
	rig := newIntegerToolRig(t, io.Discard)
	listed, err := rig.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(body []byte) any {
		var node any
		if err := json.Unmarshal(body, &node); err != nil {
			t.Fatalf("the forwarded body is not JSON: %v: %s", err, body)
		}
		return node
	}
	// present reports whether a value sits at path.
	present := func(node any, path []string) bool {
		for _, key := range path {
			obj, ok := node.(map[string]any)
			if !ok {
				return false
			}
			if node, ok = obj[key]; !ok {
				return false
			}
		}
		return true
	}
	// valuesNamed collects the value of every key called name, at any depth.
	var valuesNamed func(node any, name string) []any
	valuesNamed = func(node any, name string) []any {
		var out []any
		switch n := node.(type) {
		case map[string]any:
			for k, v := range n {
				if k == name {
					out = append(out, v)
				}
				out = append(out, valuesNamed(v, name)...)
			}
		case []any:
			for _, v := range n {
				out = append(out, valuesNamed(v, name)...)
			}
		}
		return out
	}
	checked := 0
	for _, tool := range listed.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		if err := json.Unmarshal(encoded, &raw); err != nil {
			t.Fatal(err)
		}
		examples, _ := raw["examples"].([]any)
		if len(examples) == 0 {
			continue
		}
		base := examples[0].(map[string]any)
		if tool.Name == toolContextForTask {
			base = cloneJSON(base).(map[string]any)
			base["repository"] = map[string]any{"slug": "acme/billing"}
		}
		for _, field := range discoverPublishedIntegerFields(raw, raw, nil, map[string]bool{}) {
			if !field.zeroDefault {
				continue
			}
			name := tool.Name + " " + strings.Join(field.path, ".")
			key := field.path[len(field.path)-1]
			answer := func(value string) []byte {
				t.Helper()
				data, err := json.Marshal(instanceWith(base, field.path, value))
				if err != nil {
					t.Fatal(err)
				}
				before, _, _ := rig.last()
				result, callErr := rig.session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool.Name, Arguments: json.RawMessage(data)})
				after, _, body := rig.last()
				if callErr != nil || result == nil || result.IsError || after <= before {
					t.Fatalf("%s = %s: the call was not answered (err=%v, isError=%v, reached=%v)", name, value, callErr, result != nil && result.IsError, after > before)
				}
				return body
			}
			// The control: a nonzero value is forwarded, so the checks below can
			// fail. It says whether the handler keeps the field at its path.
			control := parse(answer(strconv.FormatInt(field.boundaries[len(field.boundaries)-1], 10)))
			sameSpot := present(control, field.path)
			if !sameSpot && len(valuesNamed(control, key)) == 0 {
				t.Fatalf("%s: the forwarded request does not carry the field under any path, so a zero could not be seen in it", name)
			}
			for _, zero := range []string{"0", "0.0", "-0", "0e31", "0." + strings.Repeat("0", 400)} {
				body := parse(answer(zero))
				if sameSpot && present(body, field.path) {
					t.Errorf("%s = %.30s: the forwarded request still carries the field at its path: %v", name, zero, body)
				}
				for _, v := range valuesNamed(body, key) {
					if n, isNumber := v.(float64); isNumber && n == 0 {
						t.Errorf("%s = %.30s: the forwarded request carries %q as 0: %v", name, zero, key, body)
					}
				}
			}
			checked++
		}
	}
	if checked < 12 {
		t.Fatalf("only %d zero-default fields were derived from the published descriptions", checked)
	}
	t.Logf("checked %d zero-default fields: never forwarded as 0", checked)
}
