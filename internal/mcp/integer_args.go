package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path"
	"strings"
	"sync"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The published input schemas type a number as "integer", and JSON Schema's
// integer is a NUMBER WITHOUT A FRACTIONAL PART: 0.0, 1.0 and 1e2 are integers
// to a schema-validating client. encoding/json refuses them for a Go int field,
// so a request the schema (and the tools/list a client validates against)
// allowed was refused by the handler decode.
//
// integerArgumentsMiddleware closes that once, for every tool, at the one place
// every tools/call passes: at each path a tool's OWN published input schema
// types as integer it writes an integral number given with a fraction or an
// exponent as an integer, before any handler decodes.
//
// The decision is made on the exact value, never on the spelling's length or
// exponent: classifyInteger folds the mantissa zeros into the exponent, so
// 0e31, 10000000000000000000000000000000e-31 and 1.000...0 (any number of zeros)
// are the integers they are, and 1e999999999 is out of range without any large
// number being built. Every decision is logged at Debug (tool, field path,
// decision; never the literal).
//
// Nothing else changes, and no bound is enforced here: the handlers' own
// validators refuse what they refuse, and TestEveryPublishedIntegerFieldIs
// HandledAsItsSchemaSays holds them to the schema. Only numbers at those paths
// are looked at: a fraction (1.5), a number outside the int64 range, a string,
// null or a boolean is left as it is for the handler decode to refuse, and every
// number elsewhere (free-form variables keep their exact text) is untouched.
// Arguments that need no rewrite are passed on byte for byte.

// integerField is one place a tool's input schema declares an integer: object
// keys, with "[]" for a list element.
type integerField struct {
	path []string
}

var integerFieldsByTool sync.Map // tool name -> []integerField

// integerFieldsOf returns the integer fields of a tool's published input
// schema, computed once per tool. A tool with no manifest entry or no readable
// schema has none: its request is handled as it arrived.
func integerFieldsOf(tool string) []integerField {
	if cached, ok := integerFieldsByTool.Load(tool); ok {
		return cached.([]integerField)
	}
	var fields []integerField
	if entry, ok := lookupManifestEntry(tool); ok && entry.InputSchemaRef != "" {
		if data, err := schemaFiles.ReadFile("schemas/" + path.Base(entry.InputSchemaRef)); err == nil {
			var root map[string]any
			if err := json.Unmarshal(data, &root); err == nil {
				fields = collectIntegerFields(root, root, nil, map[string]bool{})
			}
		}
	}
	integerFieldsByTool.Store(tool, fields)
	return fields
}

// collectIntegerFields walks a schema node the way a validator does: object
// properties, array items, local $refs and the allOf/anyOf/oneOf branches.
func collectIntegerFields(root, node map[string]any, at []string, refs map[string]bool) []integerField {
	if ref, ok := node["$ref"].(string); ok {
		target, ok := resolveLocalRef(root, ref)
		if !ok || refs[ref] {
			return nil
		}
		next := map[string]bool{ref: true}
		for k := range refs {
			next[k] = true
		}
		return collectIntegerFields(root, target, at, next)
	}
	var out []integerField
	if isIntegerType(node["type"]) {
		out = append(out, integerField{path: append([]string{}, at...)})
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			if subNode, ok := sub.(map[string]any); ok {
				out = append(out, collectIntegerFields(root, subNode, append(append([]string{}, at...), name), refs)...)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		out = append(out, collectIntegerFields(root, items, append(append([]string{}, at...), "[]"), refs)...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[keyword].([]any); ok {
			for _, branch := range branches {
				if branchNode, ok := branch.(map[string]any); ok {
					out = append(out, collectIntegerFields(root, branchNode, at, refs)...)
				}
			}
		}
	}
	return out
}

func isIntegerType(v any) bool {
	switch t := v.(type) {
	case string:
		return t == "integer"
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok && s == "integer" {
				return true
			}
		}
	}
	return false
}

// resolveLocalRef follows a "#/a/b" reference inside the same document.
func resolveLocalRef(root map[string]any, ref string) (map[string]any, bool) {
	pointer, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, false
	}
	var cur any = root
	for _, part := range strings.Split(pointer, "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[part]; !ok {
			return nil, false
		}
	}
	target, ok := cur.(map[string]any)
	return target, ok
}

// integerDecision is what the middleware decided for one integer field value.
type integerDecision string

const (
	// decisionPassed: already an integer literal, or not a number (a string,
	// null or boolean is the handler's to refuse); left as it is.
	decisionPassed integerDecision = "passed"
	// decisionRewritten: an integral number in another spelling, written as the
	// integer it is.
	decisionRewritten integerDecision = "rewritten"
	// decisionRefusedRange: an integral number outside the int64 range; left
	// as it is, and the handler's decode refuses it (as the schema's maximum
	// does).
	decisionRefusedRange integerDecision = "refused-range"
	// decisionRefusedNonIntegral: a number with a fractional part; left as it
	// is, and the handler's decode refuses it (as the schema's type does).
	decisionRefusedNonIntegral integerDecision = "refused-nonintegral"
)

// integerDecisionRecord is one logged decision: where, never the literal.
type integerDecisionRecord struct {
	field    string
	decision integerDecision
}

// integerArgumentsMiddleware applies normalizeIntegerArguments to every
// tools/call and logs each decision at Debug.
func integerArgumentsMiddleware(cfg *ProcessConfig) mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/call" {
				if call, ok := req.(*mcpsdk.CallToolRequest); ok && call.Params != nil && len(call.Params.Arguments) > 0 {
					arguments, decisions := normalizeIntegerArguments(call.Params.Name, call.Params.Arguments)
					call.Params.Arguments = arguments
					if cfg != nil && cfg.diagnostics != nil {
						for _, d := range decisions {
							cfg.diagnostics.DebugContext(ctx, "mcp integer argument decided", "tool", call.Params.Name, "field", d.field, "decision", string(d.decision))
						}
					}
				}
			}
			return next(ctx, method, req)
		}
	}
}

// normalizeIntegerArguments returns raw with every integral number at an
// integer field of the tool's schema written as an integer, and the decision
// made for each integer field value it looked at. It returns raw itself when
// nothing needs to change or the arguments are not a single JSON object.
func normalizeIntegerArguments(tool string, raw []byte) ([]byte, []integerDecisionRecord) {
	fields := integerFieldsOf(tool)
	if len(fields) == 0 {
		return raw, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return raw, nil
	}
	if _, isObject := tree.(map[string]any); !isObject {
		return raw, nil
	}
	if _, err := decoder.Token(); err != io.EOF {
		return raw, nil // trailing data: the handler decode reports it
	}
	var decisions []integerDecisionRecord
	changed := false
	for _, field := range fields {
		name := strings.Join(field.path, ".")
		visitAt(tree, field.path, func(v any) (any, bool) {
			number, isNumber := v.(json.Number)
			if !isNumber {
				decisions = append(decisions, integerDecisionRecord{name, decisionPassed})
				return v, false
			}
			canonical, decision := classifyInteger(number.String())
			decisions = append(decisions, integerDecisionRecord{name, decision})
			if decision == decisionRewritten {
				changed = true
				return json.Number(canonical), true
			}
			return v, false
		})
	}
	if !changed {
		return raw, decisions
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return raw, decisions
	}
	return bytes.TrimRight(out.Bytes(), "\n"), decisions
}

// visitAt calls fn for every value at path in tree ("[]" walks every element
// of a list) and stores the replacement when fn returns one.
func visitAt(tree any, p []string, fn func(v any) (any, bool)) {
	if len(p) == 0 {
		return
	}
	head, rest := p[0], p[1:]
	switch node := tree.(type) {
	case map[string]any:
		child, ok := node[head]
		if !ok || head == "[]" {
			return
		}
		if len(rest) == 0 {
			if replacement, replaced := fn(child); replaced {
				node[head] = replacement
			}
			return
		}
		visitAt(child, rest, fn)
	case []any:
		if head != "[]" {
			return
		}
		for i, element := range node {
			if len(rest) == 0 {
				if replacement, replaced := fn(element); replaced {
					node[i] = replacement
				}
				continue
			}
			visitAt(element, rest, fn)
		}
	}
}

// maxInt64Digits is the digit count of the largest int64 (9223372036854775807).
const maxInt64Digits = 19

// classifyInteger reads one JSON number literal exactly and says what to do
// with it. The value is m x 10^e for the mantissa digits m and the exponent e:
// leading mantissa zeros are dropped, trailing mantissa zeros are folded into
// the exponent, and an all-zero mantissa is 0 whatever the exponent. The number
// is an integer iff the folded exponent is not negative, and it fits an int64
// iff its digit count (mantissa digits plus exponent) does. Nothing is
// expanded beyond 19 digits, so no spelling, however long or however large its
// exponent, costs more than a pass over the literal. The canonical literal is
// returned for decisionPassed and decisionRewritten.
func classifyInteger(literal string) (string, integerDecision) {
	s, negative := strings.CutPrefix(literal, "-")
	mantissa, exponent := s, ""
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa, exponent = s[:i], s[i+1:]
	}
	whole, fraction, _ := strings.Cut(mantissa, ".")
	digits := strings.TrimLeft(whole+fraction, "0")
	if digits == "" { // zero, -0, 0.000, 0e999999999
		if literal == "0" {
			return literal, decisionPassed
		}
		return "0", decisionRewritten
	}
	exp10 := saturatingExponent(exponent) - int64(len(fraction))
	trimmed := strings.TrimRight(digits, "0")
	exp10 += int64(len(digits) - len(trimmed))
	if exp10 < 0 {
		return literal, decisionRefusedNonIntegral
	}
	if exp10 > maxInt64Digits || int64(len(trimmed))+exp10 > maxInt64Digits {
		return literal, decisionRefusedRange
	}
	canonical := trimmed + strings.Repeat("0", int(exp10))
	limit := "9223372036854775807"
	if negative {
		limit = "9223372036854775808"
	}
	if len(canonical) == maxInt64Digits && canonical > limit {
		return literal, decisionRefusedRange
	}
	if negative {
		canonical = "-" + canonical
	}
	if canonical == literal {
		return literal, decisionPassed
	}
	return canonical, decisionRewritten
}

// saturatingExponent reads the digits of a JSON exponent ("+31", "-5", "0")
// and saturates far beyond any decision boundary instead of overflowing, so an
// exponent of any length is classified without a large number.
func saturatingExponent(exponent string) int64 {
	const saturation = int64(1) << 40
	sign := int64(1)
	switch {
	case strings.HasPrefix(exponent, "-"):
		sign, exponent = -1, exponent[1:]
	case strings.HasPrefix(exponent, "+"):
		exponent = exponent[1:]
	}
	var v int64
	for _, c := range exponent {
		v = v*10 + int64(c-'0')
		if v > saturation {
			v = saturation
		}
	}
	return sign * v
}
