package main

import (
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

// sdlRequired reports whether the SDL forces path to be present: the variable
// definition and every input field along the path are non-null with no default.
func sdlRequired(schema *ast.Schema, op *ast.OperationDefinition, path string) (bool, bool) {
	rawSegs := strings.Split(path, ".")
	segs := make([]string, len(rawSegs))
	list := make([]bool, len(rawSegs))
	for i, seg := range rawSegs {
		segs[i], list[i] = strings.CutSuffix(seg, "[*]")
	}
	var def *ast.VariableDefinition
	for _, v := range op.VariableDefinitions {
		if v.Variable == segs[0] {
			def = v
		}
	}
	if def == nil {
		return false, false
	}
	required := def.Type.NonNull && def.DefaultValue == nil
	typ := def.Type
	if list[0] {
		required = true // the requirement applies to a list element that exists
	}
	for i, seg := range segs[1:] {
		for typ.Elem != nil {
			typ = typ.Elem
		}
		td := schema.Types[typ.NamedType]
		if td == nil {
			return false, false
		}
		var field *ast.FieldDefinition
		for _, f := range td.Fields {
			if f.Name == seg {
				field = f
			}
		}
		if field == nil {
			return false, false
		}
		required = required && field.Type.NonNull && field.DefaultValue == nil
		typ = field.Type
		if list[i+1] {
			required = true // the requirement applies to a list element that exists
		}
	}
	return required, true
}

func TestNoAcrRequirementStricterThanTheSDL(t *testing.T) {
	in := vendoredInputs(t)
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
	checked := 0
	used := map[string]bool{}
	for _, op := range cat.Operations(directread.CallerUnrestricted) {
		doc, errs := gqlparser.LoadQuery(schema, op.DocumentText)
		if len(errs) > 0 || len(doc.Operations) != 1 {
			t.Fatalf("%s: document does not load: %v", op.Name, errs)
		}
		var paths []string
		for _, con := range op.Constraints {
			switch {
			case con.Kind == directread.ConstraintRequired:
				paths = append(paths, con.Path)
			case con.Kind == directread.ConstraintWindowMaxDays && !con.AllowOpenStart:
				paths = append(paths, con.Path, con.Other)
			}
		}
		for _, path := range paths {
			required, ok := sdlRequired(schema, doc.Operations[0], path)
			if !ok {
				t.Errorf("%s %s: path does not resolve against the SDL", op.Name, path)
				continue
			}
			checked++
			key := op.Name + " " + path
			if required {
				continue
			}
			if _, ok := deliberateAcrRequirements[key]; ok {
				used[key] = true
				continue
			}
			t.Errorf("%s: acr requires %s but the SDL makes it optional or defaulted; drop the requirement or list it in deliberateAcrRequirements with the reason", op.Name, path)
		}
	}
	for key := range deliberateAcrRequirements {
		if !used[key] {
			t.Errorf("deliberateAcrRequirements entry %q is stale: the SDL now requires it, or acr no longer does", key)
		}
	}
	if checked < 10 {
		t.Fatalf("only %d requirements checked: the walk is broken, not the policy", checked)
	}
}
