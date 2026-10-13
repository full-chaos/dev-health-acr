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

// resolve splits the schemas that apply to one instance into the branches
// that always apply (the schema itself, its $ref targets, allOf members and
// if/then/else) and the anyOf/oneOf groups, of which the instance must match
// one alternative each.
func (r *schemaRegistry) resolve(branches []branch, depth int) (bases []branch, groups [][]branch) {
	if depth > 64 {
		return nil, nil
	}
	for _, b := range branches {
		bases = append(bases, b)
		if ref, ok := b.schema["$ref"].(string); ok {
			name, fragment := splitSchemaRef(b.name, ref)
			if target, ok := r.byName[name]; ok {
				var resolved any = target
				if fragment != "" {
					var err error
					if resolved, err = resolveJSONPointer(target, fragment); err != nil {
						continue
					}
				}
				if m, ok := resolved.(map[string]any); ok {
					sub, subGroups := r.resolve([]branch{{name, m}}, depth+1)
					bases = append(bases, sub...)
					groups = append(groups, subGroups...)
				}
			}
		}
		if list, ok := b.schema["allOf"].([]any); ok {
			for _, raw := range list {
				if m, ok := raw.(map[string]any); ok {
					sub, subGroups := r.resolve([]branch{{b.name, m}}, depth+1)
					bases = append(bases, sub...)
					groups = append(groups, subGroups...)
				}
			}
		}
		for _, key := range []string{"if", "then", "else"} {
			if m, ok := b.schema[key].(map[string]any); ok {
				sub, subGroups := r.resolve([]branch{{b.name, m}}, depth+1)
				bases = append(bases, sub...)
				groups = append(groups, subGroups...)
			}
		}
		for _, key := range []string{"anyOf", "oneOf"} {
			if list, ok := b.schema[key].([]any); ok {
				var group []branch
				for _, raw := range list {
					if m, ok := raw.(map[string]any); ok {
						group = append(group, branch{b.name, m})
					}
				}
				if len(group) > 0 {
					groups = append(groups, group)
				}
			}
		}
	}
	return bases, groups
}

func (r *schemaRegistry) undeclaredAt(schemaName string, schemas []map[string]any, instance any, path string, depth int) error {
	var start []branch
	for _, s := range schemas {
		start = append(start, branch{schemaName, s})
	}
	bases, groups := r.resolve(start, depth)
	return r.chooseAlternatives(bases, groups, instance, path, depth)
}

// chooseAlternatives settles each anyOf/oneOf group by picking an alternative
// under which the instance passes the strict check: a key that only another
// alternative declares does not rescue an object that matches this one. An
// alternative the instance does not validate against is tried only when none
// does.
func (r *schemaRegistry) chooseAlternatives(bases []branch, groups [][]branch, instance any, path string, depth int) error {
	if len(groups) == 0 {
		return r.checkResolved(bases, instance, path, depth)
	}
	group, rest := groups[0], groups[1:]
	var matching []branch
	for _, alt := range group {
		if r.validateAt(alt.name, alt.schema, instance, path, map[string]bool{}) == nil {
			matching = append(matching, alt)
		}
	}
	if len(matching) == 0 {
		matching = group
	}
	var first error
	for _, alt := range matching {
		altBases, altGroups := r.resolve([]branch{alt}, depth+1)
		nextBases := append(append([]branch(nil), bases...), altBases...)
		nextGroups := append(append([][]branch(nil), rest...), altGroups...)
		err := r.chooseAlternatives(nextBases, nextGroups, instance, path, depth)
		if err == nil {
			return nil
		}
		if first == nil {
			first = err
		}
	}
	return first
}

func (r *schemaRegistry) checkResolved(branches []branch, instance any, path string, depth int) error {
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
		var items []branch
		for _, b := range branches {
			if m, ok := b.schema["items"].(map[string]any); ok {
				items = append(items, branch{b.name, m})
			}
		}
		for i, item := range value {
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
		props, _ := b.schema["properties"].(map[string]any)
		if m, ok := props[key].(map[string]any); ok {
			children = append(children, branch{b.name, m})
			continue
		}
		if m, ok := b.schema["additionalProperties"].(map[string]any); ok && props[key] == nil {
			children = append(children, branch{b.name, m})
		}
	}
	return r.descendBranches(children, value, path, depth)
}

// descendBranches checks the value against every schema that applies to it as
// one set: the property schemas of different branches all hold together.
func (r *schemaRegistry) descendBranches(children []branch, value any, path string, depth int) error {
	if depth > 64 || len(children) == 0 {
		return nil
	}
	bases, groups := r.resolve(children, depth+1)
	return r.chooseAlternatives(bases, groups, value, path, depth+1)
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
