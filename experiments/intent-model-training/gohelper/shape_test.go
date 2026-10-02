package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/genkitruntime"
)

const runtimeSource = "../../../internal/contextfabric/genkitruntime/runtime.go"

type astField struct {
	Name      string
	OmitEmpty bool
}

// productionStructFields reads the json tags of the named structs from
// runtime.go's AST, in declaration order. This is the production type
// itself, so the helper's shape table cannot drift from it unnoticed.
func productionStructFields(t *testing.T, names ...string) map[string][]astField {
	t.Helper()
	path, err := filepath.Abs(runtimeSource)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	wanted := make(map[string]bool, len(names))
	for _, name := range names {
		wanted[name] = true
	}
	out := make(map[string][]astField)
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || !wanted[spec.Name.Name] {
			return true
		}
		structType, ok := spec.Type.(*ast.StructType)
		if !ok {
			t.Fatalf("%s is not a struct", spec.Name.Name)
		}
		fields := []astField{}
		for _, field := range structType.Fields.List {
			if field.Tag == nil {
				continue
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				t.Fatal(err)
			}
			jsonTag := reflect.StructTag(tag).Get("json")
			parts := strings.Split(jsonTag, ",")
			if parts[0] == "" || parts[0] == "-" {
				continue
			}
			omit := false
			for _, option := range parts[1:] {
				if option == "omitempty" {
					omit = true
				}
			}
			fields = append(fields, astField{Name: parts[0], OmitEmpty: omit})
		}
		out[spec.Name.Name] = fields
		return true
	})
	for _, name := range names {
		if _, ok := out[name]; !ok {
			t.Fatalf("struct %s not found in %s", name, runtimeSource)
		}
	}
	return out
}

func allSpecs() []*objectSpec {
	return []*objectSpec{outputSpec, timeContextSpec, factRequirementSpec, questionFrameSpec, subjectExpressionSpec, operandSpec}
}

func TestShapeTableMatchesProductionAST(t *testing.T) {
	names := []string{}
	for _, spec := range allSpecs() {
		names = append(names, spec.GoType)
	}
	production := productionStructFields(t, names...)
	for _, spec := range allSpecs() {
		got := []astField{}
		for _, f := range spec.Fields {
			got = append(got, astField{Name: f.Name, OmitEmpty: f.OmitEmpty})
		}
		if !reflect.DeepEqual(got, production[spec.GoType]) {
			t.Errorf("%s shape table drifted from runtime.go\n helper:     %v\n production: %v", spec.GoType, got, production[spec.GoType])
		}
	}
}

// schemaProperties returns the sorted property names of a schema object.
func schemaProperties(t *testing.T, schema map[string]any) []string {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema object has no properties: %v", schema)
	}
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func specNames(spec *objectSpec) []string {
	names := []string{}
	for _, f := range spec.Fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func TestShapeTableMatchesProductionSchema(t *testing.T) {
	raw, err := genkitruntime.InterpretationOutputSchema()
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var check func(path string, node map[string]any, spec *objectSpec)
	check = func(path string, node map[string]any, spec *objectSpec) {
		if got, want := specNames(spec), schemaProperties(t, node); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: helper fields %v, schema properties %v", path, got, want)
		}
		properties := node["properties"].(map[string]any)
		for _, f := range spec.Fields {
			child, _ := properties[f.Name].(map[string]any)
			switch f.Kind {
			case kindObject:
				check(path+"."+f.Name, child, f.Child)
			case kindObjectList:
				items, _ := child["items"].(map[string]any)
				check(path+"."+f.Name+"[]", items, f.Child)
			}
		}
	}
	check("$", schema, outputSpec)
}
