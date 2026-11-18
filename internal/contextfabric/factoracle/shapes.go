package factoracle

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// Shape is one generated selection of one served operation behind a root
// field. Nothing in it is written by hand: the paths are the operation's
// output allowlist (or one branch of it) and the arguments are the root
// arguments of the registered document.
type Shape struct {
	Root      string
	Operation string
	// Name is "all" (every allowed output path) or "branch:<field>" (the
	// allowed paths under one field of the root type).
	Name  string
	Paths []string

	op *directread.OperationPolicy
}

// ID names the shape in reports and recordings.
func (s Shape) ID() string { return s.Root + "/" + s.Operation + "/" + s.Name }

// Shapes generates, for every allowed root field and every candidate
// operation, the full selection and one selection per branch.
func Shapes(policy *directread.GraphQLPolicy) ([]Shape, error) {
	if policy == nil {
		return nil, fmt.Errorf("no root policy")
	}
	var out []Shape
	for _, root := range policy.Roots() {
		for _, name := range root.Operations() {
			op, refusal := policy.Catalogue().Lookup(name)
			if refusal != nil || op == nil {
				return nil, fmt.Errorf("root %s: operation %s is not in the catalogue", root.Field, name)
			}
			if len(op.Outputs) == 0 {
				return nil, fmt.Errorf("root %s: operation %s has no output path", root.Field, name)
			}
			all := make([]string, 0, len(op.Outputs))
			branches := map[string][]string{}
			var order []string
			for _, o := range op.Outputs {
				if o.BeyondDocument {
					continue
				}
				all = append(all, o.Path)
				segs := strings.Split(strings.ReplaceAll(o.Path, "[*]", ""), ".")
				if segs[0] != root.Field {
					return nil, fmt.Errorf("operation %s: output %q is not under root %s", name, o.Path, root.Field)
				}
				branch := "(root)"
				if len(segs) > 2 {
					branch = segs[1]
				}
				if _, seen := branches[branch]; !seen {
					order = append(order, branch)
				}
				branches[branch] = append(branches[branch], o.Path)
			}
			out = append(out, Shape{Root: root.Field, Operation: name, Name: "all", Paths: all, op: op})
			if len(order) > 1 {
				for _, branch := range order {
					out = append(out, Shape{Root: root.Field, Operation: name, Name: "branch:" + branch, Paths: branches[branch], op: op})
				}
			}
		}
	}
	return out, nil
}

// ShapeByID finds one generated shape.
func ShapeByID(shapes []Shape, id string) (Shape, bool) {
	for _, s := range shapes {
		if s.ID() == id {
			return s, true
		}
	}
	return Shape{}, false
}

// OutputType returns the SDL type of an allowed output path.
func (s Shape) OutputType(path string) (string, bool) {
	for _, o := range s.op.Outputs {
		if o.Path == path {
			return o.Type, true
		}
	}
	return "", false
}

// Allowed reports whether a variable path may be set by a client.
func (s Shape) Allowed(path string) bool {
	rule, ok := s.op.Variable(path)
	return ok && rule.Allowed && rule.Source == directread.SourceClient
}

// Query renders the client query for vars, keyed by the document's variable
// names. A variable that is not given is left out together with its
// argument; the org argument is never sent (acr sets it from the caller).
func (s Shape) Query(vars map[string]any) (string, map[string]any, error) {
	doc, err := parser.ParseQuery(&ast.Source{Input: s.op.DocumentText})
	if err != nil {
		return "", nil, fmt.Errorf("operation %s: parse registered document: %v", s.Operation, err)
	}
	if len(doc.Operations) != 1 || len(doc.Operations[0].SelectionSet) != 1 {
		return "", nil, fmt.Errorf("operation %s: document is not one root field", s.Operation)
	}
	def := doc.Operations[0]
	root, ok := def.SelectionSet[0].(*ast.Field)
	if !ok || root.Name != s.Root {
		return "", nil, fmt.Errorf("operation %s: root selection is not %s", s.Operation, s.Root)
	}
	types := map[string]string{}
	for _, v := range def.VariableDefinitions {
		types[v.Variable] = v.Type.String()
	}
	used := map[string]bool{}
	var defs, args []string
	sent := map[string]any{}
	for _, a := range root.Arguments {
		if a.Value.Kind != ast.Variable {
			args = append(args, a.Name+": "+a.Value.String())
			continue
		}
		name := a.Value.Raw
		value, given := vars[name]
		if !given {
			continue
		}
		used[name] = true
		defs = append(defs, "$"+name+": "+types[name])
		args = append(args, a.Name+": $"+name)
		sent[name] = value
	}
	for name := range vars {
		if !used[name] {
			return "", nil, fmt.Errorf("operation %s: %q is not a root argument variable of the registered document", s.Operation, name)
		}
	}
	selection, err := selectionText(s.Root, s.Paths)
	if err != nil {
		return "", nil, err
	}
	head := "query O4Oracle"
	if len(defs) > 0 {
		head += "(" + strings.Join(defs, ", ") + ")"
	}
	field := s.Root
	if len(args) > 0 {
		field += "(" + strings.Join(args, ", ") + ")"
	}
	return head + " { " + field + " " + selection + " }", sent, nil
}

// selectionText prints a selection set from generalized output paths.
func selectionText(root string, paths []string) (string, error) {
	type node struct {
		order    []string
		children map[string]*node
	}
	top := &node{children: map[string]*node{}}
	for _, p := range paths {
		segs := strings.Split(strings.ReplaceAll(p, "[*]", ""), ".")
		if segs[0] != root || len(segs) < 2 {
			return "", fmt.Errorf("path %q is not under root %q", p, root)
		}
		cur := top
		for _, seg := range segs[1:] {
			next, ok := cur.children[seg]
			if !ok {
				next = &node{children: map[string]*node{}}
				cur.children[seg] = next
				cur.order = append(cur.order, seg)
			}
			cur = next
		}
	}
	var print func(n *node) string
	print = func(n *node) string {
		parts := make([]string, 0, len(n.order))
		for _, name := range n.order {
			child := n.children[name]
			if len(child.order) == 0 {
				parts = append(parts, name)
			} else {
				parts = append(parts, name+" "+print(child))
			}
		}
		return "{ " + strings.Join(parts, " ") + " }"
	}
	return print(top), nil
}

// LeafJSON is the type of a custom scalar that holds an object.
const LeafJSON = "json"

// selectedChildren maps a selection prefix (a generalized path, a list marked
// [*]) to the field names the shape selects under it.
func (s Shape) selectedChildren() map[string][]string {
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, p := range s.Paths {
		segs := strings.Split(p, ".")
		prefix := segs[0]
		for _, seg := range segs[1:] {
			name := strings.TrimSuffix(seg, "[*]")
			if key := prefix + "\x00" + name; !seen[key] {
				seen[key] = true
				out[prefix] = append(out[prefix], name)
			}
			prefix += "." + seg
		}
	}
	return out
}

// shapeLeaves types every leaf of an answer by the SDL type of its output
// path. Three things are returned as problems: a value on a path the shape
// did not select, a value of the wrong JSON type, and a selected field that
// is absent from an object of the answer. A GraphQL answer holds a key for
// every selected field (null for a nullable one), so an absent key is an
// answer that was cut, never an empty one.
func (s Shape) shapeLeaves(data any) (map[string][]Leaf, []string) {
	selected := map[string]bool{}
	for _, p := range s.Paths {
		selected[p] = true
	}
	children := s.selectedChildren()
	out := map[string][]Leaf{}
	var problems []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		sdl, known := s.OutputType(path)
		isLeaf := known && selected[path]
		switch t := v.(type) {
		case map[string]any:
			if isLeaf {
				out[path] = append(out[path], Leaf{T: LeafJSON, V: canonicalJSON(t)})
				return
			}
			for _, name := range children[path] {
				if _, present := t[name]; !present {
					problems = append(problems, fmt.Sprintf("%s: the selected field %s.%s is absent from the answer", s.ID(), path, name))
				}
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				walk(path+"."+k, t[k])
			}
		case []any:
			for _, item := range t {
				if isLeaf {
					walk(path, item)
				} else {
					walk(path+"[*]", item)
				}
			}
		default:
			if !isLeaf {
				if v == nil && s.isSelectedPrefix(path) {
					return
				}
				problems = append(problems, fmt.Sprintf("%s: path %s is in the answer but not in the selection", s.ID(), path))
				return
			}
			leaf, err := typedLeaf(leafKindOfSDL(sdl), v)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: path %s (%s): %v", s.ID(), path, sdl, err))
				return
			}
			if leaf.T == LeafNull && strings.HasSuffix(strings.TrimSpace(sdl), "!") {
				problems = append(problems, fmt.Sprintf("%s: path %s is null but its type %s is non-null", s.ID(), path, sdl))
			}
			out[path] = append(out[path], leaf)
		}
	}
	root, ok := data.(map[string]any)
	if !ok {
		return out, []string{s.ID() + ": data is not an object"}
	}
	if _, present := root[s.Root]; !present {
		problems = append(problems, fmt.Sprintf("%s: the answer has no %s field", s.ID(), s.Root))
	}
	keys := make([]string, 0, len(root))
	for k := range root {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		walk(k, root[k])
	}
	return out, problems
}

func (s Shape) isSelectedPrefix(path string) bool {
	trimmed := strings.TrimSuffix(path, "[*]")
	for _, p := range s.Paths {
		if strings.HasPrefix(p, trimmed+".") || strings.HasPrefix(p, trimmed+"[*]") {
			return true
		}
	}
	return false
}
