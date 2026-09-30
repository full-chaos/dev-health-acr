package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// CHAOS-7202 r1 P1 class: an acr variable requirement stricter than the SDL
// (acr refuses a request the ops resolver would answer, because the SDL makes
// the field nullable or defaults it). Every requirement acr imposes on a served
// operation must either be imposed by the SDL too (non-null, no default, on
// every step of the path) or be listed below with the written reason it is a
// deliberate acr rule.
var deliberateAcrRequirements = map[string]string{
	"cognitiveLoad input.teamId":         "design D.3: cognitiveLoad is served only per team; the SDL default null reads org-wide ratios (cognitiveload.go:178-195)",
	"capacityForecasts filters.fromDate": "design D.6: a stored-forecast list needs a bounded window for the clamp to apply",
	"capacityForecasts filters.toDate":   "design D.6: a stored-forecast list needs a bounded window for the clamp to apply",
}

// pinnedLimit is one limit acr narrows below the SDL. The SDL types are plain
// Int and [String!]: they bound none of these.
type pinnedLimit struct {
	min, max int64 // 0, 0 = no numeric limit
	maxItems int
}

// narrowedLimits pins the narrowings CHAOS-7202 introduced (CHAOS-7224). The
// values are literals on purpose: a change of the policy declaration alone
// must fail here. The consistency of EVERY served limit with the SDL is
// checked by limitViolations, not by this table.
var narrowedLimits = map[string]pinnedLimit{
	"recommendations window.value":         {min: 1, max: 26},
	"home window.rangeDays":                {min: 1, max: 90},
	"home window.compareDays":              {min: 1, max: 90},
	"workItemTeamAttributions workItemIds": {maxItems: 200},
}

// sdlNode is one input path of an operation resolved against the SDL.
type sdlNode struct {
	// required: the SDL forces the path to be present and not null. Every
	// step is non-null with no default, and so is every list element passed
	// through a [*]. The walk never restarts at a [*]: a nullable list, a
	// nullable element or a nullable field behind a wildcard is not required.
	required bool
	// perElement: the same, counted from the last [*] only. It answers "does
	// the SDL force this path in every element that exists": the nullability
	// of the list itself does not count, the nullability of its elements and
	// of everything below them does. It equals required on a path with no [*].
	perElement bool
	// typ is the declared type at the path (list wrappers kept).
	typ *ast.Type
	// def is the SDL default at the path (nil = none).
	def *ast.Value
}

// resolveSDL finds path ("a.b[*].c") in the SDL: the variable definition of
// the operation and the input object fields below it.
func resolveSDL(schema *ast.Schema, op *ast.OperationDefinition, path string) (sdlNode, bool) {
	var node sdlNode
	for i, raw := range strings.Split(path, ".") {
		name, wildcards := raw, 0
		for {
			trimmed, cut := strings.CutSuffix(name, "[*]")
			if !cut {
				break
			}
			name, wildcards = trimmed, wildcards+1
		}
		if i == 0 {
			var def *ast.VariableDefinition
			for _, v := range op.VariableDefinitions {
				if v.Variable == name {
					def = v
				}
			}
			if def == nil {
				return sdlNode{}, false
			}
			req := def.Type.NonNull && def.DefaultValue == nil
			node = sdlNode{required: req, perElement: req, typ: def.Type, def: def.DefaultValue}
		} else {
			if node.typ.Elem != nil {
				return sdlNode{}, false // a field of a list needs a [*] first
			}
			td := schema.Types[node.typ.NamedType]
			if td == nil {
				return sdlNode{}, false
			}
			var field *ast.FieldDefinition
			for _, f := range td.Fields {
				if f.Name == name {
					field = f
				}
			}
			if field == nil {
				return sdlNode{}, false
			}
			forced := field.Type.NonNull && field.DefaultValue == nil
			node = sdlNode{
				required:   node.required && forced,
				perElement: node.perElement && forced,
				typ:        field.Type,
				def:        field.DefaultValue,
			}
		}
		for ; wildcards > 0; wildcards-- {
			if node.typ.Elem == nil {
				return sdlNode{}, false
			}
			node = sdlNode{required: node.required && node.typ.Elem.NonNull, perElement: node.typ.Elem.NonNull, typ: node.typ.Elem}
		}
	}
	return node, true
}

// sdlRequired reports whether the SDL forces path to be present (see
// sdlNode.required) and whether the path resolves at all.
func sdlRequired(schema *ast.Schema, op *ast.OperationDefinition, path string) (bool, bool) {
	node, ok := resolveSDL(schema, op, path)
	return node.required, ok
}

// sdlRequiredPerElement reports whether the SDL forces path in every list
// element that exists (see sdlNode.perElement).
func sdlRequiredPerElement(schema *ast.Schema, op *ast.OperationDefinition, path string) (bool, bool) {
	node, ok := resolveSDL(schema, op, path)
	return node.perElement, ok
}

// sdlFixture is a generated catalogue with the SDL it was generated from.
type sdlFixture struct {
	schema *ast.Schema
	cat    *directread.Catalogue
	docs   map[string]*ast.OperationDefinition
}

func loadSDLFixture(t *testing.T, in inputs) sdlFixture {
	t.Helper()
	out, err := generate(in)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := directread.LoadCatalogue(out)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: schemaPath, Input: string(in.SDL)})
	if err != nil {
		t.Fatal(err)
	}
	docs := map[string]*ast.OperationDefinition{}
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		doc, errs := gqlparser.LoadQuery(schema, op.DocumentText)
		if len(errs) > 0 || len(doc.Operations) != 1 {
			t.Fatalf("%s: document does not load: %v", op.Name, errs)
		}
		docs[op.Name] = doc.Operations[0]
	}
	return sdlFixture{schema: schema, cat: cat, docs: docs}
}

// requirementViolations lists every acr requirement stricter than the SDL
// that is not in deliberate, every entry of deliberate that is stale, and
// how many requirements were checked.
func requirementViolations(f sdlFixture, deliberate map[string]string) ([]string, int) {
	var out []string
	checked := 0
	used := map[string]bool{}
	for _, op := range f.cat.Operations(directread.CallerUnrestricted) {
		type requirement struct {
			path string
			// wildcard: the constraint is ConstraintRequired on a [*] path;
			// an empty or absent list is refused: see below.
			wildcard bool
			// perElement: a window pair is checked inside each element of
			// its shared list prefix (pairInstances), so a list that is
			// absent or empty is not refused and only the elements count.
			perElement bool
		}
		var reqs []requirement
		for _, con := range op.Constraints {
			switch {
			case con.Kind == directread.ConstraintRequired:
				// checkConstraint refuses ConstraintRequired when the path has
				// no instance: for a [*] path that is an absent, null OR EMPTY
				// list (operation_edge.go, ConstraintRequired case). No SDL type
				// forces a list to be non-empty, so a wildcard ConstraintRequired
				// is always stricter than the SDL for `[]`.
				reqs = append(reqs, requirement{path: con.Path, wildcard: strings.Contains(con.Path, "[*]")})
			case con.Kind == directread.ConstraintWindowMaxDays && !con.AllowOpenStart:
				reqs = append(reqs, requirement{path: con.Path, perElement: true}, requirement{path: con.Other, perElement: true})
			}
		}
		for _, req := range reqs {
			walk := sdlRequired
			if req.perElement {
				walk = sdlRequiredPerElement
			}
			required, ok := walk(f.schema, f.docs[op.Name], req.path)
			if !ok {
				out = append(out, fmt.Sprintf("%s %s: path does not resolve against the SDL", op.Name, req.path))
				continue
			}
			checked++
			key := op.Name + " " + req.path
			if required && !req.wildcard {
				continue
			}
			if _, ok := deliberate[key]; ok {
				used[key] = true
				continue
			}
			if req.wildcard {
				out = append(out, fmt.Sprintf("%s: acr requires the wildcard path %s, and refuses an empty list that no SDL type can forbid; list it in deliberateAcrRequirements with the reason", op.Name, req.path))
				continue
			}
			out = append(out, fmt.Sprintf("%s: acr requires %s but the SDL makes it optional or defaulted; drop the requirement or list it in deliberateAcrRequirements with the reason", op.Name, req.path))
		}
	}
	for key := range deliberate {
		if !used[key] {
			out = append(out, fmt.Sprintf("deliberateAcrRequirements entry %q is stale: the SDL now requires it, or acr no longer does", key))
		}
	}
	return out, checked
}

func TestNoAcrRequirementStricterThanTheSDL(t *testing.T) {
	f := loadSDLFixture(t, vendoredInputs(t))
	violations, checked := requirementViolations(f, deliberateAcrRequirements)
	for _, v := range violations {
		t.Error(v)
	}
	if checked < 10 {
		t.Fatalf("only %d requirements checked: the walk is broken, not the policy", checked)
	}
}

// sdlDefaultInt reads an SDL default as an integer.
func sdlDefaultInt(v *ast.Value) (int64, bool) {
	if v == nil || v.Kind != ast.IntValue {
		return 0, false
	}
	n, err := strconv.ParseInt(v.Raw, 10, 64)
	return n, err == nil
}

// limitViolations checks every limit acr narrows on a served, client-set
// variable against the SDL type it narrows: the limit must be one the edge
// enforces for that type, an enum allowlist must sit inside the SDL enum, and
// an SDL default (what an OMITTED variable becomes; the edge validates only
// supplied values) must not step outside the limit. The one exception is the
// cost clamp: an Int default above Max is set to Max when the client omits it
// (applyCostClamps, operation_edge.go). It returns the violations and how many
// limited variables it checked.
func limitViolations(f sdlFixture) ([]string, int) {
	var out []string
	checked := 0
	for _, op := range f.cat.Operations(directread.CallerUnrestricted) {
		for _, rule := range op.Variables {
			if !rule.Allowed || rule.Source != directread.SourceClient {
				continue
			}
			limited := rule.Min != nil || rule.Max != nil || rule.MaxItems > 0 || rule.MaxLength > 0 || len(rule.AllowedValues) > 0
			if !limited {
				continue
			}
			checked++
			where := op.Name + " " + rule.Path
			node, ok := resolveSDL(f.schema, f.docs[op.Name], rule.Path)
			if !ok {
				out = append(out, where+": the limited path does not resolve against the SDL")
				continue
			}
			named := node.typ
			for named.Elem != nil {
				named = named.Elem
			}
			def := f.schema.Types[named.NamedType]
			if def == nil {
				out = append(out, where+": the SDL type "+named.NamedType+" is unknown")
				continue
			}
			isList := node.typ.Elem != nil
			isInt := named.NamedType == "Int"
			if (rule.Min != nil || rule.Max != nil) && !isInt {
				out = append(out, fmt.Sprintf("%s: min/max are enforced on Int only, the SDL type is %s", where, node.typ))
			}
			if rule.Min != nil && rule.Max != nil && *rule.Min > *rule.Max {
				out = append(out, fmt.Sprintf("%s: min %d is above max %d", where, *rule.Min, *rule.Max))
			}
			if rule.MaxItems > 0 && !isList {
				out = append(out, fmt.Sprintf("%s: max_items on a type that is not a list (%s)", where, node.typ))
			}
			if rule.MaxLength > 0 && (def.Kind != ast.Scalar || slices.Contains([]string{"Int", "Float", "Boolean"}, named.NamedType)) {
				out = append(out, fmt.Sprintf("%s: max_length is enforced on string-like scalars only, the SDL type is %s", where, node.typ))
			}
			if len(rule.AllowedValues) > 0 {
				if def.Kind != ast.Enum {
					out = append(out, fmt.Sprintf("%s: an allowed-value list on a type that is not an enum (%s)", where, node.typ))
				}
				for _, v := range rule.AllowedValues {
					if def.Kind == ast.Enum && def.EnumValues.ForName(v) == nil {
						out = append(out, fmt.Sprintf("%s: allowed value %s is not in the SDL enum %s", where, v, def.Name))
					}
				}
			}
			if node.def == nil || node.def.Kind == ast.NullValue {
				continue
			}
			switch {
			case isList && node.def.Kind == ast.ListValue:
				if rule.MaxItems > 0 && len(node.def.Children) > rule.MaxItems {
					out = append(out, fmt.Sprintf("%s: the SDL default holds %d items, above max_items %d", where, len(node.def.Children), rule.MaxItems))
				}
			case !isList && def.Kind == ast.Enum && node.def.Kind == ast.EnumValue:
				if len(rule.AllowedValues) > 0 && !slices.Contains(rule.AllowedValues, node.def.Raw) {
					out = append(out, fmt.Sprintf("%s: the SDL default %s is not an allowed value, so an omitted variable serves what a supplied one is refused", where, node.def.Raw))
				}
			case !isList && isInt:
				d, ok := sdlDefaultInt(node.def)
				if !ok {
					continue
				}
				if rule.Min != nil && d < *rule.Min {
					out = append(out, fmt.Sprintf("%s: the SDL default %d is below min %d, so an omitted variable serves what a supplied one is refused", where, d, *rule.Min))
				}
				clamped := rule.Max != nil && !strings.Contains(rule.Path, "[*]")
				if rule.Max != nil && d > *rule.Max && !clamped {
					out = append(out, fmt.Sprintf("%s: the SDL default %d is above max %d and no clamp covers it", where, d, *rule.Max))
				}
			}
		}
	}
	return out, checked
}

func TestNarrowedLimitsAgreeWithTheSDL(t *testing.T) {
	f := loadSDLFixture(t, vendoredInputs(t))
	violations, checked := limitViolations(f)
	for _, v := range violations {
		t.Error(v)
	}
	if checked < 20 {
		t.Fatalf("only %d limited variables checked: the walk is broken, not the policy", checked)
	}
}

// pinnedLimitViolations compares the pinned narrowings with the generated
// catalogue: recommendations window.value 1..26, home rangeDays and
// compareDays 1..90, and the workItemIds cap.
func pinnedLimitViolations(f sdlFixture, pins map[string]pinnedLimit) []string {
	var out []string
	seen := map[string]bool{}
	for _, op := range f.cat.Operations(directread.CallerUnrestricted) {
		for _, rule := range op.Variables {
			key := op.Name + " " + rule.Path
			want, pinned := pins[key]
			if !pinned {
				continue
			}
			seen[key] = true
			if !rule.Allowed {
				out = append(out, key+": pinned limit on a path that is not allowed")
				continue
			}
			var gotMin, gotMax int64
			if rule.Min != nil {
				gotMin = *rule.Min
			}
			if rule.Max != nil {
				gotMax = *rule.Max
			}
			if gotMin != want.min || gotMax != want.max || rule.MaxItems != want.maxItems {
				out = append(out, fmt.Sprintf("%s: limits are min=%d max=%d max_items=%d, pinned min=%d max=%d max_items=%d", key, gotMin, gotMax, rule.MaxItems, want.min, want.max, want.maxItems))
			}
		}
	}
	for key := range pins {
		if !seen[key] {
			out = append(out, fmt.Sprintf("pinned limit %q is not a served variable path any more", key))
		}
	}
	return out
}

func TestNarrowedLimitsArePinned(t *testing.T) {
	f := loadSDLFixture(t, vendoredInputs(t))
	for _, v := range pinnedLimitViolations(f, narrowedLimits) {
		t.Error(v)
	}
}

// The walk must not restart requiredness at a [*]. The shapes use a small SDL
// because the vendored SDL has no wildcard constraint yet: the old walk set
// required=true at every [*], which reported all of these as required.
func TestSDLWalkDoesNotResetRequirednessAtAWildcard(t *testing.T) {
	const sdl = `
type Query { probe(a: A!, b: [B!]!, c: [C]!, d: [B!] = null, n: [[B!]!]!): [String!]! }
input A { items: [Item!]!, maybe: [Item!], loose: [Item]!, id: String! }
input B { id: String!, tag: String, label: String! = "x" }
input C { id: String! }
input Item { id: String!, name: String, kind: String! = "k" }
`
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "probe.graphql", Input: sdl})
	if err != nil {
		t.Fatal(err)
	}
	doc, errs := gqlparser.LoadQuery(schema, `query Probe($a: A!, $b: [B!]!, $c: [C]!, $d: [B!] = null, $n: [[B!]!]!) { probe(a: $a, b: $b, c: $c, d: $d, n: $n) }`)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	op := doc.Operations[0]
	cases := []struct {
		path                      string
		wantRequired, wantPerElem bool
		why                       string
	}{
		{"a.items[*].id", true, true, "every step is non-null with no default"},
		{"a.items[*].name", false, false, "a nullable field behind a wildcard"},
		{"a.items[*].kind", false, false, "a defaulted field behind a wildcard"},
		{"a.maybe[*].id", false, true, "a nullable list: absent, but every element that exists has an id"},
		{"a.loose[*].id", false, false, "a nullable list element"},
		{"b[*].id", true, true, "a non-null variable list of non-null elements"},
		{"b[*].tag", false, false, "a nullable field on a variable list element"},
		{"b[*].label", false, false, "a defaulted field on a variable list element"},
		{"c[*].id", false, false, "a nullable variable list element"},
		{"d[*].id", false, true, "a defaulted, nullable variable list of non-null elements"},
		{"n[*][*].id", true, true, "two wildcard levels, every step non-null"},
	}
	for _, tc := range cases {
		got, ok := sdlRequired(schema, op, tc.path)
		if !ok {
			t.Errorf("%s: does not resolve", tc.path)
			continue
		}
		if got != tc.wantRequired {
			t.Errorf("%s: required=%v, want %v (%s)", tc.path, got, tc.wantRequired, tc.why)
		}
		if per, _ := sdlRequiredPerElement(schema, op, tc.path); per != tc.wantPerElem {
			t.Errorf("%s: per-element required=%v, want %v (%s)", tc.path, per, tc.wantPerElem, tc.why)
		}
	}
	if _, ok := sdlRequired(schema, op, "a.items.id"); ok {
		t.Error("a.items.id: a field of a list resolved without a [*]")
	}
}

// The checks above must be able to fail. Each case plants one change in the
// policy declaration and requires the matching check to report it.
func TestSDLChecksFailOnPlantedChanges(t *testing.T) {
	base := vendoredInputs(t)
	planted := func(t *testing.T, op string, edit func(d *operationDecl)) sdlFixture {
		t.Helper()
		in := cloneInputs(base)
		decl, ok := in.Policy.Served[op]
		if !ok {
			t.Fatalf("no served operation %q", op)
		}
		decl.Variables = withVariables(decl.Variables, nil)
		decl.Constraints = slices.Clone(decl.Constraints)
		edit(&decl)
		in.Policy.Served[op] = decl
		return loadSDLFixture(t, in)
	}
	expect := func(t *testing.T, got []string, substr string) {
		t.Helper()
		if !slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, substr) }) {
			t.Errorf("no violation containing %q in %v", substr, got)
		}
	}
	limits := func(f sdlFixture) []string {
		got, _ := limitViolations(f)
		return got
	}
	requirements := func(f sdlFixture) []string {
		got, _ := requirementViolations(f, deliberateAcrRequirements)
		return got
	}

	t.Run("wildcard required constraint", func(t *testing.T) {
		f := planted(t, "investmentBreakdown", func(d *operationDecl) {
			d.Constraints = append(d.Constraints, directread.Constraint{Kind: directread.ConstraintRequired, Path: "batch.breakdowns[*].measure", Code: directread.RefusalScopeRequired, Reason: "planted"})
		})
		// measure is MeasureInput! in the SDL, so every element that exists has
		// one; the plant is still stricter, because acr also refuses `[]`.
		expect(t, requirements(f), "investmentBreakdown: acr requires the wildcard path batch.breakdowns[*].measure")
	})
	// plantSDL replaces one block of the vendored SDL; the block must occur
	// exactly once, so a moved or renamed type fails loudly, not silently.
	plantSDL := func(t *testing.T, old, replacement string) inputs {
		t.Helper()
		in := cloneInputs(base)
		if n := strings.Count(string(in.SDL), old); n != 1 {
			t.Fatalf("the vendored SDL has %d occurrences of %q, want 1", n, old)
		}
		in.SDL = []byte(strings.Replace(string(in.SDL), old, replacement, 1))
		return in
	}
	t.Run("window pair behind a wildcard whose SDL field is nullable", func(t *testing.T) {
		// investmentBreakdown limits batch.breakdowns[*].dateRange.startDate
		// and .endDate. The plant makes dateRange nullable in the SDL: an
		// element without a dateRange is then answerable by ops but refused by
		// acr. (The steps below the [*] were always accumulated; this pins
		// that they stay so.)
		in := plantSDL(t, "input BreakdownRequestInput {\n  dimension: DimensionInput!\n  measure: MeasureInput!\n  dateRange: DateRangeInput!\n",
			"input BreakdownRequestInput {\n  dimension: DimensionInput!\n  measure: MeasureInput!\n  dateRange: DateRangeInput\n")
		got := requirements(loadSDLFixture(t, in))
		expect(t, got, "investmentBreakdown: acr requires batch.breakdowns[*].dateRange.startDate but the SDL makes it optional")
		expect(t, got, "investmentFull: acr requires batch.breakdowns[*].dateRange.endDate but the SDL makes it optional")
	})
	t.Run("window pair behind a wildcard whose SDL elements are nullable", func(t *testing.T) {
		// A null element has no dateRange, so acr refuses what ops answers.
		// The old walk set required=true at the [*] and never looked at the
		// element nullability: it reported both paths as required.
		in := plantSDL(t, "  breakdowns: [BreakdownRequestInput!]! = []\n", "  breakdowns: [BreakdownRequestInput]! = []\n")
		expect(t, requirements(loadSDLFixture(t, in)), "acr requires batch.breakdowns[*].dateRange.startDate but the SDL makes it optional")
	})
	t.Run("required constraint on an optional path", func(t *testing.T) {
		f := planted(t, "securityOverview", func(d *operationDecl) {
			d.Constraints = append(d.Constraints, directread.Constraint{Kind: directread.ConstraintRequired, Path: "filters.search", Code: directread.RefusalScopeRequired, Reason: "planted"})
		})
		expect(t, requirements(f), "securityOverview: acr requires filters.search but the SDL makes it optional")
	})
	t.Run("recommendations window.value minimum above the SDL default", func(t *testing.T) {
		f := planted(t, "recommendations", func(d *operationDecl) { d.Variables["window.value"] = between(5, 26) })
		expect(t, limits(f), "recommendations window.value: the SDL default 4 is below min 5")
		expect(t, pinnedLimitViolations(f, narrowedLimits), "recommendations window.value: limits are min=5 max=26")
	})
	t.Run("enum allowlist that drops the SDL default", func(t *testing.T) {
		f := planted(t, "recommendations", func(d *operationDecl) { d.Variables["window.unit"] = enum("DAY", "CYCLE") })
		expect(t, limits(f), "recommendations window.unit: the SDL default WEEK is not an allowed value")
	})
	t.Run("max_items on a scalar", func(t *testing.T) {
		f := planted(t, "workItemTeamAttributions", func(d *operationDecl) { d.Variables["teamId"] = variableDecl{Subject: team, MaxItems: 5} })
		expect(t, limits(f), "workItemTeamAttributions teamId: max_items on a type that is not a list")
	})
	t.Run("min/max on a string", func(t *testing.T) {
		f := planted(t, "workItemTeamAttributions", func(d *operationDecl) {
			d.Variables["teamId"] = variableDecl{Subject: team, Min: i64(1), Max: i64(9)}
		})
		expect(t, limits(f), "workItemTeamAttributions teamId: min/max are enforced on Int only")
	})
	t.Run("max_length on an Int", func(t *testing.T) {
		f := planted(t, "home", func(d *operationDecl) { d.Variables["window.rangeDays"] = variableDecl{MaxLength: 9} })
		expect(t, limits(f), "home window.rangeDays: max_length is enforced on string-like scalars only")
	})
	t.Run("home window widened", func(t *testing.T) {
		f := planted(t, "home", func(d *operationDecl) { d.Variables["window.rangeDays"] = between(1, 3650) })
		expect(t, pinnedLimitViolations(f, narrowedLimits), "home window.rangeDays: limits are min=1 max=3650")
	})
	t.Run("workItemIds cap removed", func(t *testing.T) {
		f := planted(t, "workItemTeamAttributions", func(d *operationDecl) { d.Variables["workItemIds"] = variableDecl{MaxLength: 256} })
		expect(t, pinnedLimitViolations(f, narrowedLimits), "workItemTeamAttributions workItemIds: limits are min=0 max=0 max_items=0")
	})
}
