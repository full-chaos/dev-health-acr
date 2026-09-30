package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
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
		{name: "a zero at a zero-default field is left out, not forwarded", tool: toolReadFacts, in: `{"kinds":["health"],"max_bytes":0.0}`, want: `{"kinds":["health"]}`},
		{name: "a zero in an integer literal is left out too", tool: toolReadFacts, in: `{"kinds":["health"],"max_bytes":0}`, want: `{"kinds":["health"]}`},
		{name: "a nested zero is left out and the object stays", tool: toolReadFacts, in: `{"window":{"mode":"trailing","days":0}}`, want: `{"window":{"mode":"trailing"}}`},
		{name: "a nonzero at a zero-default field stays", tool: toolReadFacts, in: `{"max_bytes":4096,"window":{"days":7}}`},
		{name: "a number outside an integer field keeps its exact text", tool: toolRunOperation, in: `{"operation":"hotspots","variables":{"score":1.0,"big":12345678901234567890},"max_bytes":8192.0}`, want: `{"max_bytes":8192,"operation":"hotspots","variables":{"big":12345678901234567890,"score":1.0}}`},
		{name: "html characters are not escaped on the way out", tool: toolRunOperation, in: `{"operation":"hotspots","variables":{"q":"a<b&c"},"max_bytes":8192.0}`, want: `{"max_bytes":8192,"operation":"hotspots","variables":{"q":"a<b&c"}}`},
		{name: "a string in an integer field is the handler's to refuse", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":"1"}`},
		{name: "null in an optional integer field is left to the handler", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":null}`},
		{name: "trailing data is not laundered away", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1.0} garbage`},
		{name: "not an object", tool: toolRunOperation, in: `[1.0]`},
		{name: "an exponent too large to expand is left for the handler", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1e400}`},
		{name: "a long integral spelling is decided on its value", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":0e31}`, want: `{"operation":"hotspots"}`},
		{name: "mantissa zeros fold into the exponent", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":10000000000000000000000000000000e-31}`, want: `{"max_bytes":1,"operation":"hotspots"}`},
		{name: "a fraction of zeros is an integer however many", tool: toolRunOperation, in: `{"operation":"hotspots","max_bytes":1.` + strings.Repeat("0", 1000) + `}`, want: `{"max_bytes":1,"operation":"hotspots"}`},
		{name: "a tool without integer fields", tool: toolDataCatalog, in: `{"sections":["limits"]}`},
		{name: "an unknown tool", tool: "no_such_tool", in: `{"max_bytes":1.0}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := normalizeIntegerArguments(tc.tool, []byte(tc.in))
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

// classifyInteger decides on the exact value, never on the spelling's length.
func TestClassifyIntegerDecidesOnTheExactValue(t *testing.T) {
	long := func(zeros int) string { return strings.Repeat("0", zeros) }
	cases := []struct {
		literal   string
		canonical string
		decision  integerDecision
	}{
		{"0", "0", decisionPassed},
		{"7", "7", decisionPassed},
		{"262144", "262144", decisionPassed},
		{"-5", "-5", decisionPassed},
		{"0.0", "0", decisionRewritten},
		{"1.0", "1", decisionRewritten},
		{"1e0", "1", decisionRewritten},
		{"1E+0", "1", decisionRewritten},
		{"1e-0", "1", decisionRewritten},
		{"1e2", "100", decisionRewritten},
		{"10e-1", "1", decisionRewritten},
		{"100e-2", "1", decisionRewritten},
		{"0.1e1", "1", decisionRewritten},
		{"1.0e0", "1", decisionRewritten},
		{"1." + long(60) + "e0", "1", decisionRewritten},
		{"1." + long(999), "1", decisionRewritten},
		{"0e31", "0", decisionRewritten},
		{"0e-31", "0", decisionRewritten},
		{"0e999999999", "0", decisionRewritten},
		{"0e-999999999", "0", decisionRewritten},
		{"0.000e5", "0", decisionRewritten},
		{"-0", "0", decisionRewritten},
		{"-0e5", "0", decisionRewritten},
		{"1" + long(31) + "e-31", "1", decisionRewritten},
		{"1" + long(40) + "e-40", "1", decisionRewritten},
		{"4096.0", "4096", decisionRewritten},
		{"-1.0", "-1", decisionRewritten},
		{"9223372036854775807", "9223372036854775807", decisionPassed},
		{"9223372036854775807.0", "9223372036854775807", decisionRewritten},
		{"-9223372036854775808", "-9223372036854775808", decisionPassed},
		{"9223372036854775808", "9223372036854775808", decisionRefusedRange},
		{"-9223372036854775809", "-9223372036854775809", decisionRefusedRange},
		{"1e18", "1000000000000000000", decisionRewritten},
		{"1e19", "1e19", decisionRefusedRange},
		{"1e400", "1e400", decisionRefusedRange},
		{"1e999999999", "1e999999999", decisionRefusedRange},
		{"1e99999999999999999999999999", "1e99999999999999999999999999", decisionRefusedRange},
		{"1" + long(1000), "1" + long(1000), decisionRefusedRange},
		{"1e-1", "1e-1", decisionRefusedNonIntegral},
		{"1.5", "1.5", decisionRefusedNonIntegral},
		{"-0.5", "-0.5", decisionRefusedNonIntegral},
		{"1e-999999999", "1e-999999999", decisionRefusedNonIntegral},
		{"1e-99999999999999999999999999", "1e-99999999999999999999999999", decisionRefusedNonIntegral},
		// exact, not rounded to a float: the float64 of this is exactly 1
		{"1." + long(40) + "1", "1." + long(40) + "1", decisionRefusedNonIntegral},
	}
	for _, tc := range cases {
		name := tc.literal
		if len(name) > 48 {
			name = name[:48] + "..."
		}
		t.Run(name, func(t *testing.T) {
			canonical, decision := classifyInteger(tc.literal)
			if canonical != tc.canonical || decision != tc.decision {
				t.Fatalf("classifyInteger(%.60q) = (%.60q, %s), want (%.60q, %s)", tc.literal, canonical, decision, tc.canonical, tc.decision)
			}
		})
	}
}

// One Debug record per decision, naming the tool, the field and the decision
// and never the literal.
func TestNormalizeIntegerArgumentsReportsEveryDecision(t *testing.T) {
	_, decisions := normalizeIntegerArguments(toolReadFacts, []byte(`{"max_bytes":4096.0,"window":{"days":1.5}}`))
	got := map[string]integerDecision{}
	for _, d := range decisions {
		got[d.field] = d.decision
	}
	if got["max_bytes"] != decisionRewritten || got["window.days"] != decisionRefusedNonIntegral || len(decisions) != 2 {
		t.Fatalf("decisions = %#v", decisions)
	}
	_, decisions = normalizeIntegerArguments(toolRunOperation, []byte(`{"max_bytes":1e999999999}`))
	if len(decisions) != 1 || decisions[0].decision != decisionRefusedRange {
		t.Fatalf("decisions = %#v", decisions)
	}
	_, decisions = normalizeIntegerArguments(toolFindSubjects, []byte(`{"limit":0.0}`))
	if len(decisions) != 1 || decisions[0].decision != decisionOmitted {
		t.Fatalf("decisions = %#v", decisions)
	}
}
