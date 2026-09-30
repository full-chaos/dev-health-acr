package mcp

import (
	"bytes"
	"encoding/json"
	"testing"
)

// The integer middleware rewrites only integral numbers at the integer fields
// of a tool's own schema, and passes everything else through untouched: it
// enforces no bound.
func TestNormalizeIntegerArgumentsChangesOnlyIntegerFields(t *testing.T) {
	cases := []struct {
		name string
		tool string
		in   string
		want string // "" = the arguments are returned byte for byte
	}{
		{name: "an integral float becomes an integer", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1024.0}`, want: `{"max_bytes":1024,"operation":"hotspots"}`},
		{name: "an exponent becomes an integer", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1e3}`, want: `{"max_bytes":1000,"operation":"hotspots"}`},
		{name: "an integer needs no rewrite", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1024}`},
		{name: "a fraction is left for the handler, not rounded", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1.5}`},
		{name: "a bound is the handler's, not enforced here", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":262145}`},
		{name: "an integral float above the bound is still only rewritten", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":262145.0}`, want: `{"max_bytes":262145,"operation":"hotspots"}`},
		{name: "a nested integer field", tool: toolReadFacts, in: `{"kinds":["health"],"subjects":[{"kind":"team","canonical_id":"team:1"}],"window":{"mode":"trailing","days":7.0}}`, want: `{"kinds":["health"],"subjects":[{"canonical_id":"team:1","kind":"team"}],"window":{"days":7,"mode":"trailing"}}`},
		{name: "a nested integer above the bound is only rewritten", tool: toolReadFacts, in: `{"window":{"days":61.0}}`, want: `{"window":{"days":61}}`},
		{name: "read_facts takes 0 as the default", tool: toolReadFacts, in: `{"kinds":["health"],"max_bytes":0.0}`, want: `{"kinds":["health"],"max_bytes":0}`},
		{name: "a number outside an integer field keeps its exact text", tool: toolRunOperation, in: `{"operation":"hotspots","variables":{"score":1.0,"big":12345678901234567890},"max_bytes":8192.0}`, want: `{"max_bytes":8192,"operation":"hotspots","variables":{"big":12345678901234567890,"score":1.0}}`},
		{name: "html characters are not escaped on the way out", tool: toolRunOperation, in: `{"operation":"hotspots","variables":{"q":"a<b&c"},"max_bytes":8192.0}`, want: `{"max_bytes":8192,"operation":"hotspots","variables":{"q":"a<b&c"}}`},
		{name: "a string in an integer field is the handler's to refuse", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":"1"}`},
		{name: "null in an optional integer field is left to the handler", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":null}`},
		{name: "trailing data is not laundered away", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1.0} garbage`},
		{name: "not an object", tool: toolRunOperation, in: `[1.0]`},
		{name: "an exponent too large to expand is left as it is", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1e400}`},
		{name: "a tool without integer fields", tool: toolDataCatalog, in: `{"sections":["limits"]}`},
		{name: "an unknown tool", tool: "no_such_tool", in: `{"max_bytes":1.0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeIntegerArguments(tc.tool, []byte(tc.in))
			want := tc.want
			if want == "" {
				want = tc.in
			}
			if !bytes.Equal(got, []byte(want)) {
				t.Fatalf("arguments = %s, want %s", got, want)
			}
			if tc.want != "" && !json.Valid(got) {
				t.Fatalf("the rewritten arguments are not JSON: %s", got)
			}
		})
	}
}
