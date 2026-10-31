package contractcheck

import (
	"strings"
	"testing"
)

// CHAOS-12222: the check compares requirements per instance location, not
// per schema path. A requirement added at an instance location the baseline
// already had -- through a $ref, a combinator branch, items,
// additionalProperties or a true baseline at any depth -- is a new
// constraint on bodies old producers already send, unless the baseline
// required or declared the field at that location. A requirement under a
// property name that is new since the baseline is a new optional object and
// passes.

// baselineFiles commits several published schema files and tags them.
func baselineFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "commit.gpgsign", "false")
	runGit(t, dir, "config", "tag.gpgsign", "false")
	runGit(t, dir, "config", "user.name", "t")
	runGit(t, dir, "config", "user.email", "t@example.invalid")
	writeBaseline(t, dir, "go.mod", "module x\n")
	for name, body := range files {
		writeBaseline(t, dir, "contracts/jsonschema/v1/"+name, body)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "base")
	runGit(t, dir, "tag", "v1.0.0")
	return dir
}

func commitFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		writeBaseline(t, dir, "contracts/jsonschema/v1/"+name, body)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "change")
}

type locationCase struct {
	name     string
	base     map[string]string
	current  map[string]string
	refused  bool
	mentions []string // substrings the refusal must carry
}

func runLocationCases(t *testing.T, cases []locationCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := baselineFiles(t, c.base)
			commitFiles(t, dir, c.current)
			err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir})
			if c.refused {
				if err == nil {
					t.Fatal("accepted; want refused")
				}
				for _, want := range c.mentions {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("refusal %q lacks %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("refused; want accepted: %v", err)
			}
		})
	}
}

const (
	defsOptional = `"$defs":{"opt":{"type":"object","properties":{"id":{}}},"req":{"type":"object","required":["id"],"properties":{"id":{}}},"reqzz":{"type":"object","required":["zz"],"properties":{"id":{},"zz":{}}}}`
	itemSchema   = `{"type":"object","required":["id"],"properties":{"id":{}}}`
)

func one(body string) map[string]string { return map[string]string{"a.v1.schema.json": body} }

func TestRequiredBaselineRepro1RefRetarget(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "retarget to a def requiring a field the old target never declared", refused: true, mentions: []string{`"zz"`},
			base:    one(`{` + defsOptional + `,"$ref":"#/$defs/opt"}`),
			current: one(`{` + defsOptional + `,"$ref":"#/$defs/reqzz"}`)},
		{name: "control: retarget to a def requiring a field the old target declared",
			base:    one(`{` + defsOptional + `,"$ref":"#/$defs/opt"}`),
			current: one(`{` + defsOptional + `,"$ref":"#/$defs/req"}`)},
		{name: "a property retargeted to a def requiring an undeclared field", refused: true, mentions: []string{`"zz"`, `location "/box"`},
			base:    one(`{"type":"object",` + defsOptional + `,"properties":{"box":{"$ref":"#/$defs/opt"}}}`),
			current: one(`{"type":"object",` + defsOptional + `,"properties":{"box":{"$ref":"#/$defs/reqzz"}}}`)},
	})
}

func TestRequiredBaselineRepro2NewCombinatorBranch(t *testing.T) {
	base := one(`{"type":"object","properties":{"id":{}},"allOf":[{"type":"object"}]}`)
	cases := []locationCase{}
	for name, added := range map[string]string{
		"allOf":            `"allOf":[{"type":"object"},{"required":["zz"]}]`,
		"anyOf":            `"allOf":[{"type":"object"}],"anyOf":[{"required":["zz"]}]`,
		"oneOf":            `"allOf":[{"type":"object"}],"oneOf":[{"required":["zz"]}]`,
		"if/then":          `"allOf":[{"type":"object"}],"if":{"required":["id"]},"then":{"required":["zz"]}`,
		"else":             `"allOf":[{"type":"object"}],"if":{"required":["id"]},"else":{"required":["zz"]}`,
		"dependentSchemas": `"allOf":[{"type":"object"}],"dependentSchemas":{"id":{"required":["zz"]}}`,
	} {
		cases = append(cases, locationCase{name: "new " + name + " requiring an undeclared field", refused: true, mentions: []string{`"zz"`},
			base: base, current: one(`{"type":"object","properties":{"id":{}},` + added + `}`)})
	}
	cases = append(cases, locationCase{name: "control: a new allOf branch requiring a declared field",
		base: base, current: one(`{"type":"object","properties":{"id":{}},"allOf":[{"type":"object"},{"required":["id"]}]}`)})
	runLocationCases(t, cases)
}

func TestRequiredBaselineRepro3And4DefaultedLocations(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "items added where the baseline accepted any item", refused: true, mentions: []string{`"id"`},
			base: one(`{"type":"array"}`), current: one(`{"type":"array","items":` + itemSchema + `}`)},
		{name: "additionalProperties added where the baseline accepted any value", refused: true, mentions: []string{`"id"`},
			base: one(`{"type":"object"}`), current: one(`{"type":"object","additionalProperties":` + itemSchema + `}`)},
		{name: "additionalProperties added under an object that declares its properties", refused: true, mentions: []string{`"id"`, `location "/~{*}"`},
			base: one(`{"type":"object","properties":{"a":{}}}`), current: one(`{"type":"object","properties":{"a":{}},"additionalProperties":` + itemSchema + `}`)},
		{name: "control: items whose field the baseline items declared",
			base: one(`{"type":"array","items":{"type":"object","properties":{"id":{}}}}`), current: one(`{"type":"array","items":` + itemSchema + `}`)},
	})
}

func TestRequiredBaselineRepro5DeepInsideATrueBaseline(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "a field required two levels inside a baseline of true", refused: true, mentions: []string{`"id"`, `location "/box/inner"`},
			base:    one(`{"type":"object","properties":{"box":true}}`),
			current: one(`{"type":"object","properties":{"box":{"type":"object","properties":{"inner":` + itemSchema + `}}}}`)},
	})
}

// The plant the fix must not trip: a property that is new since the baseline
// is a new optional object, so its own members may be required.
func TestRequiredBaselineAllowsANewOptionalObjectWithRequiredMembers(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "a new property whose object requires its members",
			base:    one(`{"type":"object","required":["a"],"properties":{"a":{}}}`),
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"inner":{"type":"object","required":["id"],"properties":{"id":{},"deep":` + itemSchema + `}}}}`)},
		{name: "a new property through a $ref to a new def",
			base:    one(`{"type":"object","properties":{"a":{}}}`),
			current: one(`{"type":"object","$defs":{"inner":` + itemSchema + `},"properties":{"a":{},"inner":{"$ref":"#/$defs/inner"}}}`)},
		{name: "a new array property whose items require their members",
			base:    one(`{"type":"object","properties":{"a":{}}}`),
			current: one(`{"type":"object","properties":{"a":{},"list":{"type":"array","items":` + itemSchema + `}}}`)},
		{name: "unchanged conditional and recursive schemas",
			base:    one(`{"type":"object","$defs":{"node":{"type":"object","required":["v"],"properties":{"v":{},"kids":{"type":"array","items":{"$ref":"#/$defs/node"}}}}},"properties":{"root":{"$ref":"#/$defs/node"},"x":{}},"if":{"required":["x"]},"then":{"required":["root"]}}`),
			current: one(`{"type":"object","$defs":{"node":{"type":"object","required":["v"],"properties":{"v":{},"kids":{"type":"array","items":{"$ref":"#/$defs/node"}}}}},"properties":{"root":{"$ref":"#/$defs/node"},"x":{}},"if":{"required":["x"]},"then":{"required":["root"]}}`)},
	})
}

// A definition shared across published files (context_fabric_common.v1) is
// checked where a published schema uses it.
func TestRequiredBaselineFollowsACrossFileRef(t *testing.T) {
	common := func(required string) string {
		return `{"$defs":{"Needs":{"type":"object","required":[` + required + `],"properties":{"missing":{},"anchor_options":{"type":"array","minItems":1}}}}}`
	}
	result := `{"type":"object","properties":{"structure_needs":{"$ref":"common.v1.schema.json#/$defs/Needs"}}}`
	runLocationCases(t, []locationCase{
		{name: "requiring a field the shared def declared",
			base:    map[string]string{"common.v1.schema.json": common(`"missing"`), "result.v2.schema.json": result},
			current: map[string]string{"common.v1.schema.json": common(`"missing","anchor_options"`), "result.v2.schema.json": result}},
		{name: "requiring a field the shared def never declared", refused: true, mentions: []string{"result.v2.schema.json", `"zz"`, `location "/structure_needs"`},
			base:    map[string]string{"common.v1.schema.json": common(`"missing"`), "result.v2.schema.json": result},
			current: map[string]string{"common.v1.schema.json": `{"$defs":{"Needs":{"type":"object","required":["missing","zz"],"properties":{"missing":{},"anchor_options":{"type":"array","minItems":1},"zz":{}}}}}`, "result.v2.schema.json": result}},
	})
}

// Conditions: a requirement that applies only to bodies carrying a field the
// baseline never declared cannot break an old body. The real schemas carry
// both shapes (ClaimedFact's derived_from/operation rules, CHAOS-11601's
// as_of alternative on EffectiveEvidenceWindow).
func TestRequiredBaselineWeighsConditionsAndAlternatives(t *testing.T) {
	base := one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{}},"anyOf":[{"required":["a"]},{"required":["b"]}]}`)
	runLocationCases(t, []locationCase{
		{name: "then waits on a field new since the baseline", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"n":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"if":{"required":["n"]},"then":{"required":["zz"]}}`)},
		{name: "then waits on a field the baseline declared", base: base, refused: true, mentions: []string{`"zz"`},
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"if":{"required":["b"]},"then":{"required":["zz"]}}`)},
		{name: "a field only an if names is a condition, not a requirement", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"if":{"required":["zz"]},"then":{"properties":{"zz":{"type":"string"}}}}`)},
		{name: "dependentRequired keyed by a field new since the baseline", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"n":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"dependentRequired":{"n":["zz"]}}`)},
		{name: "dependentRequired keyed by a field the baseline declared", base: base, refused: true, mentions: []string{`"zz"`},
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"dependentRequired":{"b":["zz"]}}`)},
		{name: "an anyOf gains an alternative (widens)", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]},{"required":["zz"]}]}`)},
		{name: "an anyOf gains an alternative where no alternative is required outright (EffectiveEvidenceWindow, CHAOS-11601)",
			base:    one(`{"type":"object","properties":{"r":{},"s":{},"e":{}},"anyOf":[{"required":["r"]},{"required":["s","e"]}]}`),
			current: one(`{"type":"object","properties":{"r":{},"s":{},"e":{},"as_of":{}},"anyOf":[{"required":["r"]},{"required":["s","e"]},{"required":["as_of"]}]}`)},
		{name: "control: the same anyOf loses an alternative and gains an undeclared one", refused: true, mentions: []string{`"as_of"`},
			base:    one(`{"type":"object","properties":{"r":{},"s":{},"e":{}},"anyOf":[{"required":["r"]},{"required":["s","e"]}]}`),
			current: one(`{"type":"object","properties":{"r":{},"s":{},"e":{},"as_of":{}},"anyOf":[{"required":["r"]},{"required":["as_of"]}]}`)},
		{name: "a new anyOf one of whose alternatives needs only what the baseline required", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"oneOf":[{"required":["a"]},{"required":["zz"]}]}`)},
		{name: "a not names fields it forbids, not fields it requires", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"not":{"required":["zz"]}}`)},
	})
}
