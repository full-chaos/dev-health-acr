package contextfabric

import (
	"reflect"
	"sort"
	"strings"
)

// CHAOS-4452 stage 2 (S7b-i): the frame's field tree, DERIVED rather than
// hand-listed.
//
// WHY THIS FILE EXISTS, stated once, because it is the whole point.
//
// Four adversarial rounds found ONE defect stated four times: the repair
// bound and its shape sweep were written PER LEVEL. Each level had to be
// hand-enumerated, so the next level down was always missing --
//
//	round 1: a variant READ licensed changing the discriminator
//	round 2: a permitted discriminator move excused the whole variant
//	round 3: `subject_expression_variant` was one token for six fields
//	round 4: `operands` was one token for a LIST and its elements' contents
//
// -- and each fix was more general than the last while still being written
// at a fixed depth. A rule written at depth N cannot see depth N+1, and a
// sweep that enumerates axes by hand cannot sweep a field nobody thought
// to list. The orchestrator's ruling: make both TREE-GENERIC, so depth
// stops being an axis anyone enumerates.
//
// So: the field paths of the whole frame are derived by REFLECTION over
// the type tree. Invariants declare what they constrain as PATHS. The
// repair bound states its rule ONCE over paths. The sweep is GENERATED
// from the same path set, so a new field -- or a new level of nesting --
// appears in both without anyone editing a list.
//
// **A hand-listed axis table is banned in this lane.** If you find
// yourself typing field names into a slice to make a rule work, the rule
// is at the wrong altitude; that is what this file replaced.

// FramePath is a dotted path to a field of the frame's type tree, using
// the types' own JSON names so a path is readable in a log line and in a
// receipt: `goals`, `subject_expression.kind`,
// `subject_expression.named.terms`,
// `subject_expression.explicit.operands[].scoped.member_kind`.
//
// A `[]` segment marks a slice of structs -- the element paths beneath it
// are the element's own fields.
type FramePath string

// framePathElementMarker is the segment appended for a slice-of-struct.
const framePathElementMarker = "[]"

// FramePaths returns every field path of the frame's type tree, sorted.
//
// DERIVED BY REFLECTION, never typed by hand. That is the property the
// sweep and the mutation proof both rest on: adding a field, or nesting a
// new struct, grows this set with no code change anywhere else.
func FramePaths() []FramePath {
	paths := map[FramePath]bool{}
	collectTypePaths(reflect.TypeOf(QuestionFrame{}), "", paths, map[reflect.Type]bool{})
	out := make([]FramePath, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func collectTypePaths(t reflect.Type, prefix string, out map[FramePath]bool, seen map[reflect.Type]bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	// Recursion guard. The union is acyclic today (operand depth is capped
	// at 1 by design), but a guard is cheaper than the stack overflow a
	// future self-referential variant would cause.
	if seen[t] {
		return
	}
	seen[t] = true
	defer delete(seen, t)

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue // unexported
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		out[FramePath(path)] = true

		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		switch fieldType.Kind() {
		case reflect.Struct:
			collectTypePaths(fieldType, path, out, seen)
		case reflect.Slice:
			element := fieldType.Elem()
			for element.Kind() == reflect.Ptr {
				element = element.Elem()
			}
			if element.Kind() == reflect.Struct {
				// The ELEMENT path itself is a path: an invariant must be
				// able to constrain "an operand's own fields" distinctly
				// from "the operand list". Emitting only the leaves under
				// it would leave that distinction unsayable, which is the
				// defect this whole file replaced.
				out[FramePath(path+framePathElementMarker)] = true
				collectTypePaths(element, path+framePathElementMarker, out, seen)
			}
		}
	}
}

func jsonFieldName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		// A field with no json tag still has a path -- falling back to the
		// Go name keeps the tree total. A field that is invisible to this
		// function is a field no rule can constrain, which is precisely
		// the hole this file exists to close.
		return field.Name
	}
	return name
}

// FrameStructurePaths returns the paths that are slices of structs -- the
// places where a sub-structure lives and therefore where the
// frozen-if-well-formed rule applies.
//
// Derived, not listed: today the only one is the comparison's operands,
// and the point is that a second one would be picked up here without
// anybody noticing it needed to be.
func FrameStructurePaths() []FramePath {
	out := make([]FramePath, 0, 2)
	for _, path := range FramePaths() {
		if strings.HasSuffix(string(path), framePathElementMarker) {
			out = append(out, path)
		}
	}
	return out
}

// PathConstrainedBy reports whether a repair whose failed invariant
// constrains `constrained` may change `path`.
//
// PREFIX SEMANTICS, so an invariant can constrain a subtree without
// naming every leaf under it: constraining
// `subject_expression.explicit.operands[]` permits correcting an operand's
// own fields, while constraining `subject_expression.explicit.operands`
// (no marker) permits changing the LIST -- its length and membership --
// and nothing inside an element. That distinction is exactly the one
// round 4 found missing, and here it is a property of the path grammar
// rather than a rule someone has to remember.
func PathConstrainedBy(constrained, path FramePath) bool {
	if constrained == path {
		return true
	}
	// `a.b` covers `a.b.c`; `a.b[]` covers `a.b[].c`. Crucially `a.b` does
	// NOT cover `a.b[]`: descending INTO a list's elements is a SEPARATE
	// permission from changing the list. Collapsing the two is precisely
	// the round-4 defect -- constraining "how many operands" would again
	// license rewriting what an operand IS -- so the separation lives in
	// the grammar rather than in a caller's memory. (It also means `a.bc`
	// is not covered by `a.b`, which the "." requirement gives for free.)
	return strings.HasPrefix(string(path), string(constrained)+".")
}

// PathContains reports containment in the STRUCTURAL sense, crossing the
// `[]` boundary: `a.b` contains `a.b[].c`.
//
// DELIBERATELY DIFFERENT FROM PathConstrainedBy, and the difference is the
// whole point of having two relations. "May this path change?" must NOT
// cross `[]` -- that is what stops "how many operands" from licensing
// "what an operand is". But "does adding an element here bring new
// pointers with it?" MUST cross it, because an operand a repair is
// allowed to ADD necessarily arrives carrying its own terms. Answering
// both questions with one relation is what would force a caller to
// special-case depth again.
func PathContains(ancestor, path FramePath) bool {
	if ancestor == path {
		return true
	}
	prefix := string(ancestor)
	return strings.HasPrefix(string(path), prefix+".") ||
		strings.HasPrefix(string(path), prefix+framePathElementMarker)
}

// pointerCarryingPaths are the paths whose values are RETRIEVAL POINTERS
// -- free strings handed to the graph, never values.
//
// Derived by matching the leaf's own type and name against the two term
// fields the union declares, rather than by listing sites: any future
// variant that carries `terms` or `anchor_terms` is picked up
// automatically, at any depth.
func pointerCarryingPaths() map[FramePath]bool {
	out := map[FramePath]bool{}
	for _, path := range FramePaths() {
		leaf := string(path)
		if idx := strings.LastIndex(leaf, "."); idx >= 0 {
			leaf = leaf[idx+1:]
		}
		if leaf == "terms" || leaf == "anchor_terms" {
			out[path] = true
		}
	}
	return out
}

// changedFramePaths returns the paths at which two frames differ.
//
// Slice-of-struct paths compare as LIST IDENTITY (length and element
// order), never element-by-element: an element's own fields are covered by
// the frozen-if-well-formed rule, and folding them into the list's own
// path is what let an operand's contents ride on the list's permission.
func changedFramePaths(a, b QuestionFrame) map[FramePath]bool {
	left := map[FramePath]string{}
	right := map[FramePath]string{}
	collectValuePaths(reflect.ValueOf(a), "", left)
	collectValuePaths(reflect.ValueOf(b), "", right)

	changed := map[FramePath]bool{}
	for path, value := range left {
		if right[path] != value {
			changed[path] = true
		}
	}
	for path, value := range right {
		if left[path] != value {
			changed[path] = true
		}
	}
	return changed
}

func collectValuePaths(v reflect.Value, prefix string, out map[FramePath]string) {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		value := v.Field(i)

		fieldType := field.Type
		for fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}
		switch fieldType.Kind() {
		case reflect.Struct:
			out[FramePath(path)] = presenceOf(value)
			collectValuePaths(value, path, out)
		case reflect.Slice:
			element := fieldType.Elem()
			for element.Kind() == reflect.Ptr {
				element = element.Elem()
			}
			if element.Kind() == reflect.Struct {
				// LIST IDENTITY only. Element contents are the
				// frozen-if-well-formed rule's business.
				out[FramePath(path)] = sliceIdentity(value)
				continue
			}
			out[FramePath(path)] = renderFramePathValue(value)
		default:
			out[FramePath(path)] = renderFramePathValue(value)
		}
	}
}

// presenceOf records whether a nested struct/pointer is set, so a variant
// pointer appearing or disappearing registers as a change at its own path.
func presenceOf(v reflect.Value) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	return "set"
}

// sliceIdentity renders a slice-of-struct as its length alone. Element
// CONTENTS deliberately do not appear: a list's own path answers "how many
// and in what order", and letting element contents change that path's
// value is precisely how an operand rewrite rode in on the list's
// permission (round 4).
func sliceIdentity(v reflect.Value) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Slice {
		return "nil"
	}
	return "len:" + itoa(v.Len())
}

func renderFramePathValue(v reflect.Value) string {
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return "nil"
		}
		v = v.Elem()
	}
	switch v.Kind() {
	case reflect.Slice:
		parts := make([]string, 0, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts = append(parts, renderFramePathValue(v.Index(i)))
		}
		return "[" + strings.Join(parts, "\x1f") + "]"
	case reflect.String:
		return "s:" + v.String()
	case reflect.Bool:
		if v.Bool() {
			return "b:true"
		}
		return "b:false"
	default:
		return "v:" + v.String()
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// wellFormedPredicates declares, per slice-of-struct path, what makes ONE
// element well-formed -- the question the frozen rule asks.
//
// UNREGISTERED PATHS DEFAULT TO FROZEN, and that default is the safe
// direction on purpose: a new nested structure nobody has written a
// predicate for cannot be rewritten by a repair until someone decides what
// "well-formed" means for it. The registry test asserts every structure
// path the reflection finds is either registered here or explicitly
// accepted as frozen, so a new nesting level forces a DECISION rather than
// silently inheriting whatever the nearest hand-written rule happened to
// say.
var wellFormedPredicates = map[FramePath]func(reflect.Value) bool{
	"subject_expression.explicit.operands[]": func(v reflect.Value) bool {
		operand, ok := v.Interface().(SubjectOperand)
		return ok && subjectOperandWellFormed(operand)
	},
}
