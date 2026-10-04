package directread

// The edge rules of run_operation (CHAOS-7036 design E.1 steps 7b, 7e and
// 9a): the variable path check that the top doc block of operation_policy.go
// states, the cross-path constraints, the acr-set values, the cost clamps,
// and the row check of an answer. The runner (operation_runner.go) calls
// them in order; each is a pure function of the policy and its input.
//
// The variable check is an ALLOWLIST walk over the client's variables:
//
//   - a path with no rule is refused (variable_not_allowed);
//   - a path whose rule is set by acr (acr_principal_org, acr_forced) is
//     refused when the client names it at all, even as null;
//   - a present, non-null path whose rule has Allowed=false is refused with
//     the rule's own code, and the walk goes on below it, so a deeper
//     refusal can win (who.developers answers person_scope_not_served, not
//     the "who" object's variable_not_allowed);
//   - an enum value answers its RefusedValues code, or variable_not_allowed
//     when it is in neither list; that refusal is one level deeper than the
//     path, so a person value inside a refused object still wins;
//   - Min/Max, MaxItems and MaxLength answer variable_out_of_range;
//   - a value of the wrong JSON shape answers invalid_request.
//
// Every refusal found is kept; the DEEPEST one is answered (ties: the
// smallest path). The output of the walk is a NEW tree that holds only the
// allowed client paths: nothing the client sent outside the allowlist can
// reach the upstream request, because it is never copied.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// edgeRefusal is one refusal the walk found, with where it was found.
type edgeRefusal struct {
	code   RefusalCode
	reason string
	path   string
	depth  int
}

// subjectUse is one subject id the client put in a variable.
type subjectUse struct {
	path string
	kind string
	id   string
}

// variableCheck walks one request's variables against one operation.
type variableCheck struct {
	op       *OperationPolicy
	refusals []edgeRefusal
	subjects []subjectUse
}

// checkVariables applies the request-check rules to the client's variables.
// It returns the allowlisted copy, every subject id it found, and the
// refusals (deepest first). A non-empty refusal list means: do not dispatch.
func checkVariables(op *OperationPolicy, vars map[string]any) (map[string]any, []subjectUse, []edgeRefusal) {
	vc := &variableCheck{op: op}
	out := vc.object("", vars)
	sort.SliceStable(vc.refusals, func(i, j int) bool {
		if vc.refusals[i].depth != vc.refusals[j].depth {
			return vc.refusals[i].depth > vc.refusals[j].depth
		}
		return vc.refusals[i].path < vc.refusals[j].path
	})
	return out, vc.subjects, vc.refusals
}

// pathDepth counts the field segments of a generalized path.
func pathDepth(path string) int {
	if path == "" {
		return 0
	}
	return len(pathSegments(path))
}

func (vc *variableCheck) refuse(path string, extra int, code RefusalCode, reason string) {
	vc.refusals = append(vc.refusals, edgeRefusal{code: code, reason: reason, path: path, depth: pathDepth(path) + extra})
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (vc *variableCheck) object(parent string, obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for _, key := range sortedKeys(obj) {
		path := joinPath(parent, key)
		value := obj[key]
		rule, ok := vc.op.Variable(path)
		switch {
		case !ok:
			vc.refuse(path, 0, RefusalVariableNotAllowed, "variable path is not on the operation's allowlist")
		case rule.Allowed && rule.Source != SourceClient:
			// orgId and forced values are set by acr only; the client may
			// not name the path at all, not even as null.
			vc.refuse(path, 0, RefusalVariableNotAllowed, "variable is set by acr, never by the client")
		case !rule.Allowed:
			if value == nil {
				continue // absent or null is accepted, and dropped
			}
			refusal := rule.Refusal
			if refusal == nil {
				refusal = &Refusal{Code: RefusalVariableNotAllowed, Reason: "variable is not allowed"}
			}
			vc.refuse(path, 0, refusal.Code, refusal.Reason)
			vc.inspectRefused(path, rule, value)
		default:
			if kept, keep := vc.value(path, rule, value); keep {
				out[key] = kept
			}
		}
	}
	return out
}

// isListType reports whether an SDL type is a list ("[X!]", "[X]!").
func isListType(t string) bool { return strings.HasPrefix(t, "[") }

// baseTypeName strips list brackets and non-null marks: "[Int!]!" -> "Int".
func baseTypeName(t string) string {
	return strings.Trim(t, "[]!")
}

func (vc *variableCheck) value(path string, rule VariableRule, value any) (any, bool) {
	if value == nil {
		return nil, true
	}
	list := isListType(rule.Type)
	if list {
		items, ok := value.([]any)
		if !ok {
			vc.refuse(path, 0, RefusalInvalidRequest, "variable must be a list")
			return nil, false
		}
		if rule.MaxItems > 0 && len(items) > rule.MaxItems {
			vc.refuse(path, 0, RefusalVariableOutOfRange, fmt.Sprintf("list holds more than %d items", rule.MaxItems))
			return nil, false
		}
		out := make([]any, 0, len(items))
		for _, item := range items {
			if rule.Kind == VariableKindObject {
				obj, ok := item.(map[string]any)
				if !ok {
					vc.refuse(path, 1, RefusalInvalidRequest, "list item must be an object")
					continue
				}
				out = append(out, vc.object(path+"[*]", obj))
				continue
			}
			if kept, keep := vc.scalar(path, rule, item); keep {
				out = append(out, kept)
			}
		}
		return out, true
	}
	if rule.Kind == VariableKindObject {
		obj, ok := value.(map[string]any)
		if !ok {
			vc.refuse(path, 0, RefusalInvalidRequest, "variable must be an object")
			return nil, false
		}
		return vc.object(path, obj), true
	}
	return vc.scalar(path, rule, value)
}

// scalar checks one leaf value. Value-level refusals are one level deeper
// than the path.
func (vc *variableCheck) scalar(path string, rule VariableRule, value any) (any, bool) {
	if value == nil {
		vc.refuse(path, 1, RefusalInvalidRequest, "list item must not be null")
		return nil, false
	}
	if rule.Kind == VariableKindEnum {
		s, ok := value.(string)
		if !ok {
			vc.refuse(path, 1, RefusalInvalidRequest, "enum value must be a string")
			return nil, false
		}
		for _, rv := range rule.RefusedValues {
			if rv.Value == s {
				vc.refuse(path, 1, rv.Code, rv.Reason)
				return nil, false
			}
		}
		if !slices.Contains(rule.AllowedValues, s) {
			vc.refuse(path, 1, RefusalVariableNotAllowed, "enum value is not allowed")
			return nil, false
		}
		return s, true
	}
	switch baseTypeName(rule.Type) {
	case "Int":
		n, ok := value.(json.Number)
		if !ok {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be an integer")
			return nil, false
		}
		i, err := n.Int64()
		if err != nil || i > math.MaxInt32 || i < math.MinInt32 {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be a 32-bit integer")
			return nil, false
		}
		if (rule.Min != nil && i < *rule.Min) || (rule.Max != nil && i > *rule.Max) {
			vc.refuse(path, 1, RefusalVariableOutOfRange, "integer outside the allowed range")
			return nil, false
		}
		return n, true
	case "Float":
		n, ok := value.(json.Number)
		if !ok {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be a number")
			return nil, false
		}
		if _, err := n.Float64(); err != nil {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be a number")
			return nil, false
		}
		return n, true
	case "Boolean":
		b, ok := value.(bool)
		if !ok {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be a boolean")
			return nil, false
		}
		return b, true
	default:
		s, ok := value.(string)
		if !ok {
			vc.refuse(path, 1, RefusalInvalidRequest, "value must be a string")
			return nil, false
		}
		if rule.MaxLength > 0 && len([]rune(s)) > rule.MaxLength {
			vc.refuse(path, 1, RefusalVariableOutOfRange, fmt.Sprintf("string longer than %d characters", rule.MaxLength))
			return nil, false
		}
		if rule.Subject != nil {
			vc.subjects = append(vc.subjects, subjectUse{path: path, kind: rule.Subject.Kind, id: s})
		}
		return s, true
	}
}

// inspectRefused walks below a refused path only to find DEEPER refusals:
// a refused child path and a refused enum value. It applies no clamp and no
// allowed-value check (a refused path has no allowed values), and it keeps
// nothing.
func (vc *variableCheck) inspectRefused(path string, rule VariableRule, value any) {
	switch v := value.(type) {
	case map[string]any:
		if rule.Kind == VariableKindObject && !isListType(rule.Type) {
			vc.object(path, v)
		}
	case []any:
		for _, item := range v {
			switch iv := item.(type) {
			case map[string]any:
				if rule.Kind == VariableKindObject {
					vc.object(path+"[*]", iv)
				}
			case string:
				vc.refusedValue(path, rule, iv)
			}
		}
	case string:
		vc.refusedValue(path, rule, v)
	}
}

func (vc *variableCheck) refusedValue(path string, rule VariableRule, value string) {
	for _, rv := range rule.RefusedValues {
		if rv.Value == value {
			vc.refuse(path, 1, rv.Code, rv.Reason)
			return
		}
	}
}

// ---------------------------------------------------------------- lookups

type pathSeg struct {
	name  string
	array bool
}

func parsePath(path string) []pathSeg {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	segs := make([]pathSeg, 0, len(parts))
	for _, part := range parts {
		name, array := strings.CutSuffix(part, "[*]")
		segs = append(segs, pathSeg{name: name, array: array})
	}
	return segs
}

// found is one instance of a path in a tree.
type found struct {
	value   any
	present bool
}

// instancesAt returns every instance of path in root. A [*] segment expands
// over the list at that point; an absent or null list has no instance.
func instancesAt(root any, path string) []found {
	var out []found
	collectInstances(root, parsePath(path), &out)
	return out
}

func collectInstances(cur any, segs []pathSeg, out *[]found) {
	if len(segs) == 0 {
		*out = append(*out, found{value: cur, present: true})
		return
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		*out = append(*out, found{})
		return
	}
	value, ok := obj[segs[0].name]
	if !ok {
		*out = append(*out, found{})
		return
	}
	if segs[0].array {
		items, ok := value.([]any)
		if !ok {
			return
		}
		for _, item := range items {
			collectInstances(item, segs[1:], out)
		}
		return
	}
	collectInstances(value, segs[1:], out)
}

// lookupOne returns the one instance of a path that has no [*].
func lookupOne(root any, path string) found {
	got := instancesAt(root, path)
	if len(got) != 1 {
		return found{}
	}
	return got[0]
}

// setPath writes value at a path with no [*], creating objects on the way.
// It fails when an existing node on the way is not an object.
func setPath(root map[string]any, path string, value any) error {
	segs := parsePath(path)
	cur := root
	for i, seg := range segs {
		if seg.array {
			return fmt.Errorf("cannot set a list path")
		}
		if i == len(segs)-1 {
			cur[seg.name] = value
			return nil
		}
		next, ok := cur[seg.name]
		if !ok || next == nil {
			created := map[string]any{}
			cur[seg.name] = created
			cur = created
			continue
		}
		obj, ok := next.(map[string]any)
		if !ok {
			return fmt.Errorf("path node is not an object")
		}
		cur = obj
	}
	return nil
}

// sharedArrayPrefix returns the longest common prefix of a and b that ends
// with "[*]", and the two remainders.
func sharedArrayPrefix(a, b string) (string, string, string) {
	best := -1
	for i := 0; i+3 <= len(a) && i+3 <= len(b); i++ {
		if a[i] != b[i] {
			break
		}
		if a[i:i+3] == "[*]" && b[i:i+3] == "[*]" {
			best = i + 3
		}
	}
	if best < 0 {
		return "", a, b
	}
	return a[:best], strings.TrimPrefix(a[best:], "."), strings.TrimPrefix(b[best:], ".")
}

// pairInstances applies fn to (a, b) within each element of their shared
// list prefix, or once when they share none.
func pairInstances(root any, a, b string, fn func(fa, fb found) *Refusal) *Refusal {
	prefix, restA, restB := sharedArrayPrefix(a, b)
	if prefix == "" {
		return fn(lookupOne(root, a), lookupOne(root, b))
	}
	for _, element := range instancesAt(root, prefix) {
		if !element.present {
			continue
		}
		if r := fn(lookupOne(element.value, restA), lookupOne(element.value, restB)); r != nil {
			return r
		}
	}
	return nil
}

// ------------------------------------------------------------ constraints

func isNullish(f found) bool { return !f.present || f.value == nil }

// parseEdgeTime reads a GraphQL Date ("2006-01-02") or DateTime (RFC 3339).
func parseEdgeTime(value any) (time.Time, bool) {
	s, ok := value.(string)
	if !ok {
		return time.Time{}, false
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

func constraintRefusal(con Constraint) *Refusal {
	reason := con.Reason
	if con.Kind == ConstraintRequired && !strings.Contains(reason, "`"+con.Path+"`") {
		reason += "; missing required argument `" + con.Path + "`"
	}
	return &Refusal{Code: con.Code, Reason: reason}
}

// checkConstraints applies cross-path constraints to the allowlisted client
// tree. The first violated constraint is answered.
func checkConstraints(tree map[string]any, constraints []Constraint, now time.Time) *Refusal {
	for _, con := range constraints {
		if r := checkConstraint(tree, con, now); r != nil {
			return r
		}
	}
	return nil
}

func checkConstraint(tree map[string]any, con Constraint, now time.Time) *Refusal {
	switch con.Kind {
	case ConstraintRequired:
		for _, f := range instancesAt(tree, con.Path) {
			if isNullish(f) {
				return constraintRefusal(con)
			}
		}
		if len(instancesAt(tree, con.Path)) == 0 {
			return constraintRefusal(con)
		}
		return nil
	case ConstraintEmptyOrAbsent:
		for _, f := range instancesAt(tree, con.Path) {
			if isNullish(f) {
				continue
			}
			if items, ok := f.value.([]any); ok && len(items) == 0 {
				continue
			}
			return constraintRefusal(con)
		}
		return nil
	case ConstraintRequiresValue:
		otherValue := func(f found) (string, bool) {
			if isNullish(f) {
				return con.OtherDefault, con.OtherDefault != ""
			}
			s, ok := f.value.(string)
			return s, ok
		}
		if con.Path == "" {
			for _, f := range instancesAt(tree, con.Other) {
				if v, ok := otherValue(f); !ok || !slices.Contains(con.Values, v) {
					return constraintRefusal(con)
				}
			}
			return nil
		}
		return pairInstances(tree, con.Path, con.Other, func(fp, fo found) *Refusal {
			if isNullish(fp) {
				return nil
			}
			if v, ok := otherValue(fo); !ok || !slices.Contains(con.Values, v) {
				return constraintRefusal(con)
			}
			return nil
		})
	case ConstraintWindowMaxDays:
		return pairInstances(tree, con.Path, con.Other, func(fs, fe found) *Refusal {
			if isNullish(fs) {
				if con.AllowOpenStart {
					return nil
				}
				return constraintRefusal(con)
			}
			start, ok := parseEdgeTime(fs.value)
			if !ok {
				return &Refusal{Code: RefusalInvalidRequest, Reason: "window start is not a date"}
			}
			end := now
			if !isNullish(fe) {
				if end, ok = parseEdgeTime(fe.value); !ok {
					return &Refusal{Code: RefusalInvalidRequest, Reason: "window end is not a date"}
				}
			}
			if end.Before(start) || end.Sub(start) > time.Duration(con.MaxDays)*24*time.Hour {
				return constraintRefusal(con)
			}
			return nil
		})
	case ConstraintMaxDaysAhead:
		for _, f := range instancesAt(tree, con.Path) {
			if isNullish(f) {
				continue
			}
			t, ok := parseEdgeTime(f.value)
			if !ok {
				return &Refusal{Code: RefusalInvalidRequest, Reason: "date is not a date"}
			}
			if t.Sub(now) > time.Duration(con.MaxDays)*24*time.Hour {
				return constraintRefusal(con)
			}
		}
		return nil
	default:
		return &Refusal{Code: RefusalPolicyStale, Reason: "constraint kind is not known to this edge"}
	}
}

// -------------------------------------------------------- acr-set values

// applyAcrValues writes the values acr sets: orgId from the principal (never
// the client) and every forced value.
func applyAcrValues(op *OperationPolicy, tree map[string]any, orgID string) error {
	for _, rule := range op.Variables {
		if !rule.Allowed || strings.Contains(rule.Path, "[*]") {
			continue
		}
		switch rule.Source {
		case SourcePrincipalOrg:
			if err := setPath(tree, rule.Path, orgID); err != nil {
				return err
			}
		case SourceForced:
			var value any
			if len(rule.ForcedValue) > 0 {
				dec := json.NewDecoder(bytes.NewReader(rule.ForcedValue))
				dec.UseNumber()
				if err := dec.Decode(&value); err != nil {
					return err
				}
			} else if rule.Default != "" {
				dec := json.NewDecoder(strings.NewReader(rule.Default))
				dec.UseNumber()
				if err := dec.Decode(&value); err != nil {
					return err
				}
			} else {
				return errors.New("forced variable has no value")
			}
			if err := setPath(tree, rule.Path, value); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyCostClamps bounds work the client did not bound: an allowed client
// integer whose SDL default exceeds the rule's Max is set to Max when the
// client left it out (workGraph* limit defaults to 1000; D.6 caps lists at
// 200). The parent object is created when absent.
func applyCostClamps(op *OperationPolicy, tree map[string]any) error {
	for _, rule := range op.Variables {
		if !rule.Allowed || rule.Source != SourceClient || strings.Contains(rule.Path, "[*]") || baseTypeName(rule.Type) != "Int" {
			continue
		}
		effective := rule.EffectiveDefault()
		if effective == rule.Default {
			continue
		}
		if f := lookupOne(tree, rule.Path); !isNullish(f) {
			continue
		}
		if err := setPath(tree, rule.Path, json.Number(effective)); err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------- row check

// rowCheck is the result of checking an answer's rows against a grant.
type rowCheck struct {
	checked int
	foreign int
}

// checkRows checks every RowIDPaths value of data against grant (bare ids,
// lower case). A row whose id is missing, null or not a string is foreign:
// a row that cannot be checked is not served. A path that ends before its
// first list (the root field is null) has no rows.
func checkRows(data []byte, rowIDPaths []string, grant map[string]bool) (rowCheck, error) {
	var result rowCheck
	if len(bytes.TrimSpace(data)) == 0 {
		return result, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if err := dec.Decode(&root); err != nil {
		return result, err
	}
	for _, path := range rowIDPaths {
		segs := parsePath(path)
		walkRows(root, segs, false, func(value any, ok bool) {
			result.checked++
			id, isString := value.(string)
			if !ok || !isString || !grant[strings.ToLower(strings.TrimSpace(id))] {
				result.foreign++
			}
		})
	}
	return result, nil
}

// walkRows calls visit once per row instance. inRow is true below the first
// list: a missing node there is a row without an id.
func walkRows(cur any, segs []pathSeg, inRow bool, visit func(value any, ok bool)) {
	if len(segs) == 0 {
		visit(cur, true)
		return
	}
	obj, isObj := cur.(map[string]any)
	if !isObj {
		if inRow {
			visit(nil, false)
		}
		return
	}
	value, present := obj[segs[0].name]
	if !present || value == nil {
		if inRow {
			visit(nil, false)
		}
		return
	}
	if segs[0].array {
		items, ok := value.([]any)
		if !ok {
			// A list position that is not a list cannot be checked.
			visit(nil, false)
			return
		}
		for _, item := range items {
			walkRows(item, segs[1:], true, visit)
		}
		return
	}
	walkRows(value, segs[1:], inRow, visit)
}

// ------------------------------------------------------------- emptiness

// dataIsEmpty applies the D.7 emptiness rule to a filtered "data" object:
// empty when no list holds an element and, when there is no list at all,
// every scalar is null. A payload with a non-empty list or (list-free) a
// non-null value carries data.
func dataIsEmpty(data []byte) bool {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var root any
	if dec.Decode(&root) != nil {
		return true
	}
	var lists, nonEmptyLists, nonNullScalars int
	var walk func(v any, key string)
	walk = func(v any, key string) {
		switch t := v.(type) {
		case nil:
		case map[string]any:
			for k, child := range t {
				walk(child, k)
			}
		case []any:
			lists++
			if len(t) > 0 {
				nonEmptyLists++
			}
			for _, child := range t {
				walk(child, key)
			}
		default:
			if key != "__typename" {
				nonNullScalars++
			}
		}
	}
	walk(root, "")
	if nonEmptyLists > 0 {
		return false
	}
	if lists > 0 {
		return true
	}
	return nonNullScalars == 0
}
