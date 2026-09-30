package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/big"
	"path"
	"strconv"
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

// integerArgumentsMiddleware applies normalizeIntegerArguments to every
// tools/call.
func integerArgumentsMiddleware() mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/call" {
				if call, ok := req.(*mcpsdk.CallToolRequest); ok && call.Params != nil && len(call.Params.Arguments) > 0 {
					call.Params.Arguments = normalizeIntegerArguments(call.Params.Name, call.Params.Arguments)
				}
			}
			return next(ctx, method, req)
		}
	}
}

// normalizeIntegerArguments returns raw with every integral number at an
// integer field of the tool's schema written as an integer. It returns raw
// itself when nothing needs to change or the arguments are not a single JSON
// object.
func normalizeIntegerArguments(tool string, raw []byte) []byte {
	fields := integerFieldsOf(tool)
	if len(fields) == 0 {
		return raw
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return raw
	}
	if _, isObject := tree.(map[string]any); !isObject {
		return raw
	}
	if _, err := decoder.Token(); err != io.EOF {
		return raw // trailing data: the handler decode reports it
	}
	changed := false
	for _, field := range fields {
		visitAt(tree, field.path, func(v any) (any, bool) {
			number, isNumber := v.(json.Number)
			if !isNumber {
				return v, false
			}
			if integral, ok := integralText(number.String()); ok && integral != number.String() {
				changed = true
				return json.Number(integral), true
			}
			return v, false
		})
	}
	if !changed {
		return raw
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return raw
	}
	return bytes.TrimRight(out.Bytes(), "\n")
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

// integralText writes a JSON number that has no fractional part as an integer
// literal ("0.0" -> "0", "1e2" -> "100"). It refuses anything that is not
// exactly integral, does not fit an int64, or is written with an exponent
// large enough to be expensive to expand.
func integralText(literal string) (string, bool) {
	if len(literal) > 40 {
		return "", false
	}
	if i := strings.IndexAny(literal, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(strings.TrimPrefix(literal[i+1:], "+"))
		if err != nil || exponent > 30 || exponent < -30 {
			return "", false
		}
	}
	r, ok := new(big.Rat).SetString(literal)
	if !ok || !r.IsInt() {
		return "", false
	}
	n := r.Num()
	if !n.IsInt64() {
		return "", false
	}
	return strconv.FormatInt(n.Int64(), 10), true
}
