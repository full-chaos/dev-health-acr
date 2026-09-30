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

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The published input schemas type a number as "integer", and JSON Schema's
// integer is a NUMBER WITHOUT A FRACTIONAL PART: 0.0, 1.0 and 1e2 are integers
// to a schema-validating client. encoding/json refuses them for a Go int field,
// so a request the schema (and the tools/list a client validates against)
// allowed was refused by the handler decode. The other way round, a handler
// that reads a zero as "not set" (a Go int with omitempty cannot tell 0 from an
// absent field) accepted an explicit 0 that the schema's minimum refuses.
//
// integerArgumentsMiddleware closes both once, for every tool, at the one place
// every tools/call passes: at each path a tool's OWN published input schema
// types as integer it (1) writes an integral number given with a fraction or an
// exponent as an integer, and (2) refuses a number the field's schema does not
// allow. So the server follows the schema for integers, and the schema is the
// contract.
//
// Nothing else changes. Only numbers at those paths are looked at: a string,
// null or boolean is left to the handler decode, and every number elsewhere
// (free-form variables keep their exact text) is untouched. Arguments that need
// no rewrite are passed on byte for byte.

// integerField is one place a tool's input schema declares an integer: object
// keys, with "[]" for a list element.
type integerField struct {
	path []string
	// schema validates the value the field may hold. It is nil when the field
	// sits under allOf/anyOf/oneOf, where the branch alone does not decide
	// validity: such a field is only normalized, never refused.
	schema *jsonschema.Resolved
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
				fields = collectIntegerFields(root, root, nil, false, map[string]bool{})
			}
		}
	}
	integerFieldsByTool.Store(tool, fields)
	return fields
}

// collectIntegerFields walks a schema node the way a validator does: object
// properties, array items, local $refs and the allOf/anyOf/oneOf branches.
func collectIntegerFields(root, node map[string]any, at []string, viaBranch bool, refs map[string]bool) []integerField {
	if ref, ok := node["$ref"].(string); ok {
		target, ok := resolveLocalRef(root, ref)
		if !ok || refs[ref] {
			return nil
		}
		next := map[string]bool{ref: true}
		for k := range refs {
			next[k] = true
		}
		return collectIntegerFields(root, target, at, viaBranch, next)
	}
	var out []integerField
	if isIntegerType(node["type"]) {
		field := integerField{path: append([]string{}, at...)}
		if !viaBranch {
			field.schema = compileIntegerNode(root, node)
		}
		out = append(out, field)
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			if subNode, ok := sub.(map[string]any); ok {
				out = append(out, collectIntegerFields(root, subNode, append(append([]string{}, at...), name), viaBranch, refs)...)
			}
		}
	}
	if items, ok := node["items"].(map[string]any); ok {
		out = append(out, collectIntegerFields(root, items, append(append([]string{}, at...), "[]"), viaBranch, refs)...)
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[keyword].([]any); ok {
			for _, branch := range branches {
				if branchNode, ok := branch.(map[string]any); ok {
					out = append(out, collectIntegerFields(root, branchNode, at, true, refs)...)
				}
			}
		}
	}
	return out
}

// compileIntegerNode compiles one integer property on its own, with the
// document's $defs so a local $ref inside it still resolves. A node that does
// not compile is not enforced (it is still normalized).
func compileIntegerNode(root, node map[string]any) *jsonschema.Resolved {
	doc := make(map[string]any, len(node)+1)
	for k, v := range node {
		doc[k] = v
	}
	if defs, ok := root["$defs"]; ok {
		doc["$defs"] = defs
	}
	data, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return nil
	}
	return resolved
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

// integerArgumentsMiddleware applies checkIntegerArguments to every tools/call.
// A number the schema refuses is answered as a validation tool error, the same
// category the handlers use, and never reaches a handler.
func integerArgumentsMiddleware() mcpsdk.Middleware {
	return func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call, ok := req.(*mcpsdk.CallToolRequest)
			if !ok || call.Params == nil || len(call.Params.Arguments) == 0 {
				return next(ctx, method, req)
			}
			arguments, refused := checkIntegerArguments(call.Params.Name, call.Params.Arguments)
			if refused != "" {
				return toolErrorResult(&classifiedError{category: "validation", message: call.Params.Name + " arguments failed schema validation: " + refused + " is not an integer the input schema allows"}), nil
			}
			call.Params.Arguments = arguments
			return next(ctx, method, req)
		}
	}
}

// checkIntegerArguments returns raw with every integral number at an integer
// field of the tool's schema written as an integer, and the path of the first
// number the field's schema refuses ("" when none). It returns raw itself when
// nothing needs to change or the arguments are not a single JSON object.
func checkIntegerArguments(tool string, raw []byte) ([]byte, string) {
	fields := integerFieldsOf(tool)
	if len(fields) == 0 {
		return raw, ""
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var tree any
	if err := decoder.Decode(&tree); err != nil {
		return raw, ""
	}
	if _, isObject := tree.(map[string]any); !isObject {
		return raw, ""
	}
	if _, err := decoder.Token(); err != io.EOF {
		return raw, "" // trailing data: the handler decode reports it
	}
	changed, refused := false, ""
	for _, field := range fields {
		visitAt(tree, field.path, func(v any) (any, bool) {
			number, isNumber := v.(json.Number)
			if !isNumber {
				return v, false // a string, null or boolean is the handler's to refuse
			}
			text := number.String()
			replaced := false
			if integral, ok := integralText(text); ok && integral != text {
				text, replaced = integral, true
			}
			if field.schema != nil && refused == "" {
				f, err := strconv.ParseFloat(text, 64)
				if err != nil || field.schema.Validate(f) != nil {
					refused = strings.Join(field.path, ".")
				}
			}
			if replaced {
				changed = true
				return json.Number(text), true
			}
			return v, false
		})
	}
	if refused != "" || !changed {
		return raw, refused
	}
	var out bytes.Buffer
	encoder := json.NewEncoder(&out)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tree); err != nil {
		return raw, ""
	}
	return bytes.TrimRight(out.Bytes(), "\n"), ""
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
