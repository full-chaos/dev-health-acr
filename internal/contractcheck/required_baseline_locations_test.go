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
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"allOf":[{"anyOf":[{"required":["a"]},{"required":["zz"]}]}]}`)},
		// Codex #1817 r1 P2: a oneOf is exclusive, so an old body {a, zz}
		// that meets two alternatives fails it. Only an unchanged oneOf is
		// covered.
		{name: "a new oneOf, even one an alternative of which the baseline required", base: base, refused: true, mentions: []string{`"zz"`},
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"oneOf":[{"required":["a"]},{"required":["zz"]}]}`)},
		{name: "a oneOf that gains an alternative", refused: true, mentions: []string{`"zz"`},
			base:    one(`{"type":"object","properties":{"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`),
			current: one(`{"type":"object","properties":{"a":{},"b":{},"zz":{}},"oneOf":[{"required":["a"]},{"required":["b"]},{"required":["zz"]}]}`)},
		{name: "control: an unchanged oneOf naming a field properties never declared",
			base:    one(`{"type":"object","properties":{"a":{}},"oneOf":[{"required":["a"]},{"required":["q"]}]}`),
			current: one(`{"type":"object","properties":{"a":{}},"oneOf":[{"required":["a"]},{"required":["q"]}]}`)},
		{name: "control: an unchanged oneOf",
			base:    one(`{"type":"object","properties":{"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`),
			current: one(`{"type":"object","properties":{"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`)},
		{name: "a not names fields it forbids, not fields it requires", base: base,
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"b":{}},"anyOf":[{"required":["a"]},{"required":["b"]}],"not":{"required":["zz"]}}`)},
	})
}

// A condition inside an alternative still binds: the anyOf branch below
// requires nothing itself, but its then requires zz of every body that
// carries the declared field a.
func TestRequiredBaselineChecksAConditionInsideAnAlternative(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "anyOf branch whose then requires an undeclared field", refused: true, mentions: []string{`"zz"`},
			base:    one(`{"type":"object","properties":{"a":{}}}`),
			current: one(`{"type":"object","properties":{"a":{},"zz":{}},"anyOf":[{"if":{"required":["a"]},"then":{"required":["zz"]}}]}`)},
	})
}

// A property name the baseline already admitted as an extra key is not new:
// old bodies may carry it, so its members cannot become required.
func TestRequiredBaselineTreatsAnAdmittedExtraKeyAsExisting(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "a property matching a baseline patternProperties entry", refused: true, mentions: []string{`"id"`, `location "/x_new"`},
			base:    one(`{"type":"object","properties":{"a":{}},"patternProperties":{"^x_":{"type":"object"}}}`),
			current: one(`{"type":"object","properties":{"a":{},"x_new":` + itemSchema + `},"patternProperties":{"^x_":{"type":"object"}}}`)},
		{name: "a property under a baseline additionalProperties schema", refused: true, mentions: []string{`"id"`, `location "/extra"`},
			base:    one(`{"type":"object","properties":{"a":{}},"additionalProperties":{"type":"object"}}`),
			current: one(`{"type":"object","properties":{"a":{},"extra":` + itemSchema + `},"additionalProperties":{"type":"object"}}`)},
		{name: "control: a property under additionalProperties false is new",
			base:    one(`{"type":"object","properties":{"a":{}},"additionalProperties":false}`),
			current: one(`{"type":"object","properties":{"a":{},"extra":` + itemSchema + `},"additionalProperties":false}`)},
	})
}

// Codex #1817 r1 P3s.
func TestRequiredBaselineRoundOneEdges(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "prefixItems positions are separate locations", refused: true, mentions: []string{`"id"`, `location "/[1]"`},
			base:    one(`{"type":"array","prefixItems":[{"type":"object","properties":{"id":{}}},{"type":"object"}]}`),
			current: one(`{"type":"array","prefixItems":[{"type":"object","properties":{"id":{}}},` + itemSchema + `]}`)},
		{name: "a property whose $ref resolves to false was never optional", refused: true, mentions: []string{`"x"`},
			base:    one(`{"type":"object","$defs":{"never":false},"properties":{"a":{},"x":{"$ref":"#/$defs/never"}}}`),
			current: one(`{"type":"object","required":["x"],"$defs":{"never":{}},"properties":{"a":{},"x":{"$ref":"#/$defs/never"}}}`)},
		{name: "required on a schema that admits no object has no effect",
			base:    one(`{"type":"object","properties":{"s":{"type":"string"}}}`),
			current: one(`{"type":"object","properties":{"s":{"type":"string","required":["x"]}}}`)},
		{name: "control: required on a schema that may be an object counts", refused: true, mentions: []string{`"x"`},
			base:    one(`{"type":"object","properties":{"s":{"type":["string","object"]}}}`),
			current: one(`{"type":"object","properties":{"s":{"type":["string","object"],"required":["x"]}}}`)},
	})
}

// A $ref by $id, not by file name, resolves through the target's $id.
func TestRequiredBaselineResolvesARefByID(t *testing.T) {
	common := func(required string) string {
		return `{"$id":"https://example.invalid/schemas/shared.json","$defs":{"Needs":{"type":"object","required":[` + required + `],"properties":{"missing":{}}}}}`
	}
	result := `{"$id":"https://example.invalid/schemas/result.json","type":"object","properties":{"n":{"$ref":"shared.json#/$defs/Needs"}}}`
	runLocationCases(t, []locationCase{
		{name: "a ref by $id to an undeclared requirement", refused: true, mentions: []string{`"zz"`, `location "/n"`},
			base:    map[string]string{"common.v1.schema.json": common(`"missing"`), "result.v1.schema.json": result},
			current: map[string]string{"common.v1.schema.json": `{"$id":"https://example.invalid/schemas/shared.json","$defs":{"Needs":{"type":"object","required":["missing","zz"],"properties":{"missing":{},"zz":{}}}}}`, "result.v1.schema.json": result}},
	})
}

// A schema nested deeper than the walk's bound is an error, never a pass.
func TestRequiredBaselineRefusesASchemaDeeperThanTheBound(t *testing.T) {
	deep := `{"type":"object","properties":{"a":{}}}`
	for i := 0; i < maxLocationDepth+2; i++ {
		deep = `{"type":"object","properties":{"n":` + deep + `}}`
	}
	dir := baselineFiles(t, one(deep))
	commitFiles(t, dir, one(deep))
	if err := CheckRequiredAgainstTag(RequiredBaselineOptions{Root: dir}); err == nil || !strings.Contains(err.Error(), "deeper") {
		t.Fatalf("a schema deeper than the bound passed: %v", err)
	}
}

// Only an unconditional declaration lets a field become required: a field the
// baseline declared only under then, else, dependentSchemas or one anyOf or
// oneOf branch is sent only when that branch applies, so an old body outside
// it may lack the field (codex #1817 r2). A field declared that way still
// counts as one an old body may carry, so a condition keyed by it applies.
func TestRequiredBaselineCountsOnlyUnconditionalDeclarations(t *testing.T) {
	requireZZ := func(body string) string {
		return strings.Replace(body, `{"type":"object",`, `{"type":"object","required":["zz"],`, 1)
	}
	conditional := map[string]string{
		"then":             `{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":{"properties":{"zz":{}}}}`,
		"else":             `{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"else":{"properties":{"zz":{}}}}`,
		"dependentSchemas": `{"type":"object","properties":{"flag":{}},"dependentSchemas":{"flag":{"properties":{"zz":{}}}}}`,
		"anyOf":            `{"type":"object","anyOf":[{"properties":{"zz":{}}},{"properties":{"y":{}}}]}`,
		"oneOf":            `{"type":"object","oneOf":[{"properties":{"zz":{}},"required":["zz"]},{"properties":{"y":{}},"required":["y"]}]}`,
		"then, nested":     `{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":{"properties":{"box":{"type":"object","properties":{"zz":{}}}}}}`,
	}
	var cases []locationCase
	for name, body := range conditional {
		current := requireZZ(body)
		mention := `location "/"`
		if name == "then, nested" {
			current = strings.Replace(body, `"box":{"type":"object",`, `"box":{"type":"object","required":["zz"],`, 1)
			mention = `location "/box"`
		}
		cases = append(cases, locationCase{name: "a field declared only under " + name, refused: true, mentions: []string{`"zz"`, mention},
			base: one(body), current: one(current)})
	}
	cases = append(cases,
		locationCase{name: "control: declared in an allOf member",
			base:    one(`{"type":"object","allOf":[{"properties":{"zz":{}}}]}`),
			current: one(`{"type":"object","required":["zz"],"allOf":[{"properties":{"zz":{}}}]}`)},
		locationCase{name: "control: declared through a $ref",
			base:    one(`{"type":"object","$ref":"#/$defs/S","$defs":{"S":{"properties":{"zz":{}}}}}`),
			current: one(`{"type":"object","required":["zz"],"$ref":"#/$defs/S","$defs":{"S":{"properties":{"zz":{}}}}}`)},
		locationCase{name: "a condition keyed by a field the baseline declared only under then still applies", refused: true, mentions: []string{`"zz"`},
			base:    one(`{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":{"properties":{"t":{}}}}`),
			current: one(`{"type":"object","properties":{"flag":{},"zz":{}},"if":{"required":["flag"]},"then":{"properties":{"t":{}}},"dependentRequired":{"t":["zz"]}}`)},
	)
	// Under a then, the flag carries through every keyword that reaches a
	// location: a declaration there is still conditional.
	then := func(inner string) string {
		return `{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":` + inner + `}`
	}
	for _, c := range []struct{ name, base, current, at string }{
		{"a $ref", then(`{"$ref":"#/$defs/S"}`) + ``, ``, `/`},
		{"an allOf member", then(`{"allOf":[{"properties":{"zz":{}}}]}`), ``, `/`},
		{"items", then(`{"properties":{"arr":{"type":"array","items":{"type":"object","properties":{"zz":{}}}}}}`), `items`, `/arr/[]`},
		{"additionalProperties", then(`{"additionalProperties":{"type":"object","properties":{"zz":{}}}}`), `additional`, `/~{*}`},
	} {
		base := c.base
		current := ""
		switch c.current {
		case "":
			if c.name == "a $ref" {
				base = strings.TrimSuffix(base, "}") + `,"$defs":{"S":{"properties":{"zz":{}}}}}`
			}
			current = strings.Replace(base, `{"type":"object",`, `{"type":"object","required":["zz"],`, 1)
		case "items":
			current = strings.Replace(base, `"items":{"type":"object",`, `"items":{"type":"object","required":["zz"],`, 1)
		case "additional":
			current = strings.Replace(base, `"additionalProperties":{"type":"object",`, `"additionalProperties":{"type":"object","required":["zz"],`, 1)
		}
		cases = append(cases, locationCase{name: "a field declared under a then through " + c.name, refused: true,
			mentions: []string{`"zz"`, `location "` + c.at + `"`}, base: one(base), current: one(current)})
	}
	cases = append(cases, locationCase{name: "a new anyOf whose fields the baseline declared only under then", refused: true, mentions: []string{`"zz"`},
		base:    one(`{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":{"properties":{"zz":{},"y":{}}}}`),
		current: one(`{"type":"object","properties":{"flag":{}},"if":{"required":["flag"]},"then":{"properties":{"zz":{},"y":{}}},"anyOf":[{"required":["zz"]},{"required":["y"]}]}`)})
	runLocationCases(t, cases)
}

// A $ref resolves against the base URI of the nearest enclosing $id, so a
// relative ref or a "#..." ref inside a nested resource names that resource,
// not the document root (codex #1817 r2).
func TestRequiredBaselineResolvesRefsAgainstANestedID(t *testing.T) {
	result := func(thing string) string {
		return `{"$id":"https://example.invalid/v1/result.json","type":"object","properties":{` +
			`"via":{"$ref":"#/properties/box/properties/shared"},` +
			`"box":{"$id":"https://example.invalid/v2/box.json","type":"object","$defs":{"T":` + thing + `,"F":false},"properties":{"local":{"$ref":"#/$defs/T"},"shared":{"$ref":"shared.json#/$defs/U"},"gone":{"$ref":"#/$defs/F"}}}}}`
	}
	shared := func(u string) string {
		return `{"$id":"https://example.invalid/v2/shared.json","$defs":{"U":` + u + `}}`
	}
	thing := `{"type":"object","properties":{"n":{}}}`
	files := func(thing, u string) map[string]string {
		return map[string]string{"result.v1.schema.json": result(thing), "shared.v1.schema.json": shared(u)}
	}
	runLocationCases(t, []locationCase{
		{name: "an unchanged schema with refs inside a nested $id resolves", base: files(thing, thing), current: files(thing, thing)},
		{name: "a local ref inside the nested resource reaches its own $defs", refused: true, mentions: []string{`"zz"`, `location "/box/local"`},
			base: files(thing, thing), current: files(`{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`, thing)},
		{name: "a relative ref resolves against the nested $id", refused: true, mentions: []string{`"zz"`, `location "/box/shared"`},
			base: files(thing, thing), current: files(thing, `{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`)},
		{name: "control: requiring a field the nested target declared", base: files(thing, thing),
			current: files(`{"type":"object","required":["n"],"properties":{"n":{}}}`, thing)},
		{name: "a ref through the nested resource resolves against it", refused: true, mentions: []string{`"zz"`, `location "/via"`},
			base: files(thing, thing), current: files(thing, `{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`)},
		{name: "a property the nested resource forbids through a $ref is undeclared", refused: true, mentions: []string{`"gone"`, `location "/box"`},
			base:    files(thing, thing),
			current: map[string]string{"result.v1.schema.json": strings.Replace(result(thing), `"type":"object","$defs"`, `"type":"object","required":["gone"],"$defs"`, 1), "shared.v1.schema.json": shared(thing)}},
		{name: "an anyOf branch's ref inside the nested resource counts its requirement", refused: true, mentions: []string{`"zz"`, `location "/box"`},
			base:    map[string]string{"result.v1.schema.json": strings.Replace(result(thing), `"type":"object","$defs"`, `"type":"object","anyOf":[{"$ref":"#/$defs/T"}],"$defs"`, 1), "shared.v1.schema.json": shared(thing)},
			current: map[string]string{"result.v1.schema.json": strings.Replace(result(`{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`), `"type":"object","$defs"`, `"type":"object","anyOf":[{"$ref":"#/$defs/T"}],"$defs"`, 1), "shared.v1.schema.json": shared(thing)}},
	})
}

// A subschema's own $id sets the base of its own refs, wherever the walk meets
// it: an anyOf branch, a property weighed for false, and a resource whose $id
// is relative to its parent's (codex #1817 r2).
func TestRequiredBaselineResolvesRefsInsideTheirOwnID(t *testing.T) {
	plain := `{"type":"object","properties":{"n":{}}}`
	needsZZ := `{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`
	root := func(alt, required string) string {
		return `{"$id":"https://example.invalid/v1/r.json","type":"object",` + required + `"properties":{` +
			`"alt":{"type":"object","anyOf":[{"$id":"https://example.invalid/v3/alt.json","$defs":{"A":` + alt + `},"$ref":"#/$defs/A"}]},` +
			`"gone":{"$id":"https://example.invalid/v3/g.json","$defs":{"G":false},"$ref":"#/$defs/G"},` +
			`"rel":{"$ref":"box.json#/$defs/T"},` +
			`"holder":{"$id":"box.json","$defs":{"T":{"type":"object","properties":{"s":{"$ref":"shared.json#/$defs/U"}}}}}}}`
	}
	shared := func(u string) string {
		return `{"$id":"https://example.invalid/v1/shared.json","$defs":{"U":` + u + `}}`
	}
	files := func(r, u string) map[string]string {
		return map[string]string{"r.v1.schema.json": r, "shared.v1.schema.json": shared(u)}
	}
	runLocationCases(t, []locationCase{
		{name: "unchanged", base: files(root(plain, ``), plain), current: files(root(plain, ``), plain)},
		{name: "an anyOf branch with its own $id requires a field", refused: true, mentions: []string{`"zz"`, `location "/alt"`},
			base: files(root(plain, ``), plain), current: files(root(needsZZ, ``), plain)},
		{name: "a property forbidden through its own $id's ref is undeclared", refused: true, mentions: []string{`"gone"`, `location "/"`},
			base: files(root(plain, ``), plain), current: files(root(plain, `"required":["gone"],`), plain)},
		{name: "a relative $id resolves against its parent's", refused: true, mentions: []string{`"zz"`, `location "/rel/s"`},
			base: files(root(plain, ``), plain), current: files(root(plain, ``), needsZZ)},
	})
}

// An $id inside instance data (examples, const, enum, default) names no schema
// resource, so it cannot take a published schema's URI (codex #1817 r3).
func TestRequiredBaselineIgnoresAnIDInsideInstanceData(t *testing.T) {
	plain := `{"type":"object","properties":{"n":{}}}`
	for _, keyword := range []string{"examples", "const", "enum", "default"} {
		data := `{"$id":"https://example.invalid/target","$defs":{"S":` + plain + `}}`
		if keyword == "examples" || keyword == "enum" {
			data = "[" + data + "]"
		}
		consumer := `{"$id":"https://example.invalid/consumer","type":"object","properties":{"use":{"$ref":"https://example.invalid/target#/$defs/S"}},"` + keyword + `":` + data + `}`
		target := func(s string) string { return `{"$id":"https://example.invalid/target","$defs":{"S":` + s + `}}` }
		files := func(s string) map[string]string {
			return map[string]string{"a_consumer.v1.schema.json": consumer, "z_target.v1.schema.json": target(s)}
		}
		runLocationCases(t, []locationCase{
			{name: "an $id under " + keyword + " does not hide the real resource", refused: true, mentions: []string{`"zz"`, `location "/use"`},
				base: files(plain), current: files(`{"type":"object","required":["zz"],"properties":{"n":{},"zz":{}}}`)},
		})
	}
}
