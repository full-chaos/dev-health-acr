package directread

import (
	"fmt"
	"strings"
	"testing"
)

func parseIntDefault(raw string) (int64, bool) {
	var v int64
	if _, err := fmt.Sscan(raw, &v); err != nil || fmt.Sprint(v) != raw {
		return 0, false
	}
	return v, true
}

func TestAdvertisedDefaultIsWithinBoundsAndMatchesTheClampOnEveryOperation(t *testing.T) {
	cat := loadDefault(t)
	catalog := catalogFor(t, ClassUnrestricted, true, true).Operations
	advertised := map[string]CatalogVariable{}
	for _, op := range catalog.Operations {
		for _, v := range op.Variables {
			advertised[op.Name+" "+v.Path] = v
		}
	}
	checked, clamped := 0, 0
	for _, op := range cat.Operations(CallerUnrestricted) {
		for _, rule := range op.Variables {
			if !rule.Allowed || rule.Source != SourceClient || rule.Kind == VariableKindObject {
				continue
			}
			v, ok := advertised[op.Name+" "+rule.Path]
			if !ok {
				t.Fatalf("%s %s is not advertised by data_catalog", op.Name, rule.Path)
			}
			def, isInt := parseIntDefault(v.Default)
			if !isInt || baseTypeName(rule.Type) != "Int" {
				continue
			}
			checked++
			if v.Min != nil && def < *v.Min || v.Max != nil && def > *v.Max {
				t.Errorf("%s %s advertises default %d outside min/max %v/%v", op.Name, rule.Path, def, deref(v.Min), deref(v.Max))
			}
			if strings.Contains(rule.Path, "[*]") {
				continue
			}
			tree := map[string]any{}
			if err := applyCostClamps(op, tree); err != nil {
				t.Fatal(err)
			}
			got := lookupOne(tree, rule.Path)
			if isNullish(got) {
				if v.Default != rule.Default {
					t.Errorf("%s %s: omitted variable is left to the upstream default %s but data_catalog advertises %s", op.Name, rule.Path, rule.Default, v.Default)
				}
				continue
			}
			clamped++
			if fmt.Sprint(got.value) != v.Default {
				t.Errorf("%s %s: the clamp uses %v for an omitted variable but data_catalog advertises %s", op.Name, rule.Path, got.value, v.Default)
			}
		}
	}
	if checked < 10 || clamped < 2 {
		t.Fatalf("measured %d integer defaults and %d clamped ones; the check measured too little", checked, clamped)
	}
}

func deref(p *int64) any {
	if p == nil {
		return "none"
	}
	return *p
}

func TestSchemaViewAdvertisedDefaultsAreWithinBounds(t *testing.T) {
	policy, err := DefaultGraphQLPolicy()
	if err != nil {
		t.Fatal(err)
	}
	section := BuildCatalogSchema(policy, CallerUnrestricted, true, true, true)
	measured := 0
	for _, root := range section.Roots {
		for _, arg := range root.Arguments {
			for _, v := range arg.Paths {
				def, isInt := parseIntDefault(v.Default)
				if !isInt {
					continue
				}
				measured++
				if v.Min != nil && def < *v.Min || v.Max != nil && def > *v.Max {
					t.Errorf("root %s path %s advertises default %d outside min/max %v/%v", root.Field, v.Path, def, deref(v.Min), deref(v.Max))
				}
			}
		}
	}
	if measured < 8 {
		t.Fatalf("measured %d integer defaults; the check measured too little", measured)
	}
}

func TestEffectiveDefaultMovesAnIntegerIntoItsBounds(t *testing.T) {
	lo, hi := int64(5), int64(200)
	for _, tc := range []struct {
		rule VariableRule
		want string
	}{
		{VariableRule{Type: "Int!", Default: "1000", Min: &lo, Max: &hi}, "200"},
		{VariableRule{Type: "Int!", Default: "1", Min: &lo, Max: &hi}, "5"},
		{VariableRule{Type: "Int!", Default: "50", Min: &lo, Max: &hi}, "50"},
		{VariableRule{Type: "Int", Default: "1000", Max: &hi}, "200"},
		{VariableRule{Type: "Int", Default: "1", Min: &lo}, "5"},
		{VariableRule{Type: "Int", Default: "null", Min: &lo, Max: &hi}, "null"},
		{VariableRule{Type: "String", Default: "1000", Max: &hi}, "1000"},
		{VariableRule{Type: "Int", Default: "", Max: &hi}, ""},
		{VariableRule{Type: "Int", Default: "007", Min: &lo, Max: &hi}, "007"},
	} {
		if got := tc.rule.EffectiveDefault(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.rule, got, tc.want)
		}
	}
}
