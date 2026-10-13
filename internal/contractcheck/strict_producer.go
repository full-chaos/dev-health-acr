package contractcheck

import (
	"fmt"
	"sort"
	"strings"
)

func isMCPResponseSchema(name string) bool {
	return strings.HasPrefix(name, "mcp_") && strings.Contains(name, "_response.")
}

// rejectUndeclared is the producer-side half of the open-output rule: output
// schemas leave objects open so a cached copy tolerates a field added later,
// but a payload the server serves (or a golden example) must carry only keys
// some schema branch declares. An object whose branches set
// additionalProperties (a map, or an explicit boolean) keeps that rule.
func (r *schemaRegistry) rejectUndeclared(schemaName string, instance any) error {
	schema, ok := r.byName[schemaName]
	if !ok {
		return fmt.Errorf("unknown schema %s", schemaName)
	}
	return r.undeclaredAt(schemaName, []map[string]any{schema}, instance, "$", 0)
}

type branch struct {
	name   string
	schema map[string]any
}

func (r *schemaRegistry) flatten(schemaName string, schema map[string]any, depth int, out *[]branch) {
	if depth > 64 {
		return
	}
	*out = append(*out, branch{schemaName, schema})
	if ref, ok := schema["$ref"].(string); ok {
		name, fragment := splitSchemaRef(schemaName, ref)
		if target, ok := r.byName[name]; ok {
			var resolved any = target
			if fragment != "" {
				var err error
				if resolved, err = resolveJSONPointer(target, fragment); err != nil {
					return
				}
			}
			if m, ok := resolved.(map[string]any); ok {
				r.flatten(name, m, depth+1, out)
			}
		}
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := schema[key].([]any); ok {
			for _, raw := range list {
				if m, ok := raw.(map[string]any); ok {
					r.flatten(schemaName, m, depth+1, out)
				}
			}
		}
	}
	for _, key := range []string{"if", "then", "else"} {
		if m, ok := schema[key].(map[string]any); ok {
			r.flatten(schemaName, m, depth+1, out)
		}
	}
}

func (r *schemaRegistry) undeclaredAt(schemaName string, schemas []map[string]any, instance any, path string, depth int) error {
	var branches []branch
	for _, s := range schemas {
		r.flatten(schemaName, s, depth, &branches)
	}
	switch value := instance.(type) {
	case map[string]any:
		declared := map[string]bool{}
		open := false
		for _, b := range branches {
			if _, ok := b.schema["additionalProperties"]; ok {
				open = true
			}
			if props, ok := b.schema["properties"].(map[string]any); ok {
				for name := range props {
					declared[name] = true
				}
			}
		}
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !declared[key] && !open && len(declared) > 0 {
				return fmt.Errorf("%s: undeclared property %q in a served payload", path, key)
			}
			if err := r.descend(branches, key, value[key], childPath(path, key), depth); err != nil {
				return err
			}
		}
	case []any:
		for i, item := range value {
			var items []branch
			for _, b := range branches {
				if m, ok := b.schema["items"].(map[string]any); ok {
					items = append(items, branch{b.name, m})
				}
			}
			if err := r.descendBranches(items, item, fmt.Sprintf("%s[%d]", path, i), depth); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *schemaRegistry) descend(branches []branch, key string, value any, path string, depth int) error {
	var children []branch
	for _, b := range branches {
		if props, ok := b.schema["properties"].(map[string]any); ok {
			if m, ok := props[key].(map[string]any); ok {
				children = append(children, branch{b.name, m})
				continue
			}
		}
		if m, ok := b.schema["additionalProperties"].(map[string]any); ok {
			if props, _ := b.schema["properties"].(map[string]any); props[key] == nil {
				children = append(children, branch{b.name, m})
			}
		}
	}
	return r.descendBranches(children, value, path, depth)
}

func (r *schemaRegistry) descendBranches(children []branch, value any, path string, depth int) error {
	if depth > 64 {
		return nil
	}
	for _, c := range children {
		if err := r.undeclaredAt(c.name, []map[string]any{c.schema}, value, path, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// openOutputObjects returns a deep copy of schema without any
// additionalProperties:false: the embedded MCP output copy of a canonical
// schema is open to added fields while the canonical HTTP schema stays as is.
func openOutputObjects(value any) any {
	switch node := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for k, v := range node {
			if b, ok := v.(bool); ok && !b && k == "additionalProperties" {
				continue
			}
			out[k] = openOutputObjects(v)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, v := range node {
			out[i] = openOutputObjects(v)
		}
		return out
	}
	return value
}
