package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

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
}

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
		out = append(out, publishedIntegerField{path: append([]string{}, at...), boundaries: collectBoundaries(node)})
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
	for _, b := range f.boundaries {
		for _, v := range []int64{b - 1, b, b + 1} {
			add(strconv.FormatInt(v, 10))
		}
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

func TestEveryPublishedIntegerFieldIsHandledAsItsSchemaSays(t *testing.T) {
	var hostedCalls atomic.Int64
	hosted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer hosted.Close()

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
	processConfig, caller := boot.split(io.Discard)
	processConfig.transport = TransportHTTP
	session := connectedClientForCaller(t, processConfig, caller)

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
		var lastOutcome string // what the last call answered, for a failure message
		call := func(args map[string]any) bool {
			data, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			before := hostedCalls.Load()
			result, callErr := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tool.Name, Arguments: json.RawMessage(data)})
			lastOutcome = fmt.Sprintf("error=%v", callErr)
			if callErr == nil && result != nil && len(result.Content) > 0 {
				if text, ok := result.Content[0].(*mcpsdk.TextContent); ok {
					lastOutcome = fmt.Sprintf("isError=%v text=%q", result.IsError, text.Text)
				}
			}
			return callErr == nil && hostedCalls.Load() > before
		}
		schemaAccepts := func(args map[string]any) bool {
			data, _ := json.Marshal(args)
			var value any
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
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
					t.Errorf("%s = %s: the published schema %s it, tools/call %s it (%s)", name, cell, verdict(bySchema), verdict(byCall), lastOutcome)
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
