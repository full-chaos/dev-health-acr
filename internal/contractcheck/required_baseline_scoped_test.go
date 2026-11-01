package contractcheck

import "testing"

// CHAOS-12340: a baseline fact counts only where it held. A baseline anyOf or
// oneOf covers a current group, and a baseline requirement a current one,
// only when the baseline branch path encloses the current one (and, for a
// requirement, its triggers are among the current one's). Unconditional
// means no trigger and no branch. A property is forbidden, and so not
// declared, where a subschema that refuses every value guards it, or where a
// subschema with additionalProperties or unevaluatedProperties false neither
// declares it nor matches it by a pattern.

// A: a group under a baseline then covers no group outside it.
func TestRequiredBaselineScopesGroupsToTheirBranch(t *testing.T) {
	runLocationCases(t, []locationCase{
		// The old body {} lacks f, so it never met the oneOf; now it must.
		{name: "a oneOf leaves its then", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (0 at v1.0.0, 2 now)`},
			base:     one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"if":{"required":["f"]},"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current:  one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`)},
		{name: "an anyOf leaves its then", refused: true,
			mentions: []string{`the anyOf at instance location "/" changed its alternatives (0 at v1.0.0, 2 now)`},
			base:     one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"if":{"required":["f"]},"then":{"anyOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current:  one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"anyOf":[{"required":["a"]},{"required":["b"]}]}`)},
		// zz is declared outright, so it is not newly required: the
		// refusal names the group, not zz.
		{name: "a oneOf leaves its then and swaps in an alternative over a field declared outright", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (0 at v1.0.0, 2 now)`}, absent: []string{`field "zz"`},
			base:    one(`{"type":"object","properties":{"f":{},"a":{},"b":{},"zz":{}},"if":{"required":["f"]},"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current: one(`{"type":"object","properties":{"f":{},"a":{},"b":{},"zz":{}},"oneOf":[{"required":["a"]},{"required":["zz"]}]}`)},
		{name: "control: a oneOf moves into a then",
			base:    one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`),
			current: one(`{"type":"object","properties":{"f":{},"a":{},"b":{}},"if":{"required":["f"]},"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`)},
	})
}

// B: a field required under an else, or only beside a trigger, is not
// required unconditionally.
func TestRequiredBaselineUnconditionalMeansNoBranchAndNoTrigger(t *testing.T) {
	runLocationCases(t, []locationCase{
		// {"f":1} took the then side, so it never needed a; it meets no
		// alternative now.
		{name: "an anyOf alternative needs only what an else required", refused: true,
			mentions: []string{`the anyOf at instance location "/" changed its alternatives (0 at v1.0.0, 2 now)`},
			base:     one(`{"type":"object","properties":{"f":{},"a":{},"zz":{}},"if":{"required":["f"]},"else":{"required":["a"]}}`),
			current:  one(`{"type":"object","properties":{"f":{},"a":{},"zz":{}},"if":{"required":["f"]},"else":{"required":["a"]},"anyOf":[{"required":["a"]},{"required":["zz"]}]}`)},
		{name: "an anyOf alternative needs only what a dependentRequired required", refused: true,
			mentions: []string{`the anyOf at instance location "/"`},
			base:     one(`{"type":"object","properties":{"f":{},"a":{},"zz":{}},"dependentRequired":{"f":["a"]}}`),
			current:  one(`{"type":"object","properties":{"f":{},"a":{},"zz":{}},"dependentRequired":{"f":["a"]},"anyOf":[{"required":["a"]},{"required":["zz"]}]}`)},
		{name: "control: an anyOf alternative needs only what the body required",
			base:    one(`{"type":"object","required":["a"],"properties":{"a":{},"zz":{}}}`),
			current: one(`{"type":"object","required":["a"],"properties":{"a":{},"zz":{}},"anyOf":[{"required":["a"]},{"required":["zz"]}]}`)},
	})
}

// C: a baseline requirement meets a current one only where it applied.
func TestRequiredBaselineScopesRequirementsToTheirBranch(t *testing.T) {
	runLocationCases(t, []locationCase{
		// The old body {} lacks f, so it never needed x; now it must.
		{name: "a field required only under a then becomes required outright", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"required":["x"]}}`),
			current: one(`{"type":"object","required":["x"],"properties":{"f":{}},"if":{"required":["f"]},"then":{"required":["x"]}}`)},
		{name: "a field required only beside a trigger becomes required outright", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{}},"dependentRequired":{"f":["x"]}}`),
			current: one(`{"type":"object","required":["x"],"properties":{"f":{}},"dependentRequired":{"f":["x"]}}`)},
		{name: "control: a requirement under a then meets the same one in an alternative inside it",
			base: one(`{"type":"object","properties":{"f":{},"box":{"type":"object"}},"$defs":{"R":{"properties":{"box":{"type":"object","required":["x"]}}},"K":{"properties":{"k":{}}}},` +
				`"if":{"required":["f"]},"then":{"properties":{"box":{"type":"object","required":["x"]}}}}`),
			current: one(`{"type":"object","properties":{"f":{},"box":{"type":"object"}},"$defs":{"R":{"properties":{"box":{"type":"object","required":["x"]}}},"K":{"properties":{"k":{}}}},` +
				`"if":{"required":["f"]},"then":{"anyOf":[{"$ref":"#/$defs/R"},{"$ref":"#/$defs/K"}]}}`)},
		{name: "control: an unchanged requirement under a then",
			base:    one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"required":["x"]}}`),
			current: one(`{"type":"object","properties":{"f":{},"g":{}},"if":{"required":["f"]},"then":{"required":["x"]}}`)},
	})
}

// D: a property whose schema can meet no value is forbidden, not declared.
func TestRequiredBaselineForbidsAPropertyNoValueMeets(t *testing.T) {
	with := func(x string) map[string]string {
		return one(`{"type":"object","$defs":{"N":{"allOf":[{"not":{}}]}},"properties":{"a":{},"x":` + x + `}}`)
	}
	required := func(x string) map[string]string {
		return one(`{"type":"object","required":["x"],"$defs":{"N":{"allOf":[{"not":{}}]}},"properties":{"a":{},"x":` + x + `}}`)
	}
	var cases []locationCase
	for name, x := range map[string]string{
		"an allOf with a not of the empty schema": `{"allOf":[{"not":{}}]}`,
		"an allOf one of whose members refuses":   `{"allOf":[{"type":"string"},{"not":{}}]}`,
		"a not of the empty schema":               `{"not":{}}`,
		"a not of true":                           `{"not":true}`,
		"an anyOf whose members all refuse":       `{"anyOf":[false,{"not":{}}]}`,
		"a oneOf whose members all refuse":        `{"oneOf":[false,{"not":true}]}`,
		"a $ref to such a schema":                 `{"$ref":"#/$defs/N"}`,
	} {
		cases = append(cases, locationCase{name: "requiring a property guarded by " + name, refused: true,
			mentions: []string{`field "x" at instance location "/"`}, base: with(x), current: required(x)})
	}
	cases = append(cases,
		locationCase{name: "control: an anyOf one of whose members admits a value", base: with(`{"anyOf":[false,{}]}`), current: required(`{"anyOf":[false,{}]}`)},
		locationCase{name: "control: a not of a schema that refuses some values", base: with(`{"not":{"type":"string"}}`), current: required(`{"not":{"type":"string"}}`)},
	)
	runLocationCases(t, cases)
}

// D+: a subschema with additionalProperties or unevaluatedProperties false
// forbids every name it does not admit, where it applies.
func TestRequiredBaselineForbidsANameAClosedSubschemaRefuses(t *testing.T) {
	closedA := func(required, closed string) map[string]string {
		return one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
			`"A":{"allOf":[{"required":["a"],"properties":{"a":{},"x":{}}},`+closed+`],"required":[`+required+`]},"B":{"required":["b"]}`, "A", "B"))
	}
	root := func(required, members, extra string) map[string]string {
		return one(`{"type":"object",` + required + `"allOf":[` + members + `]` + extra + `}`)
	}
	declaresAX := `{"properties":{"a":{},"x":{}}}`
	runLocationCases(t, []locationCase{
		// {"a":true} met A; A now requires x, which its closed member
		// refuses, so it meets no alternative.
		{name: "an alternative requires a field its closed allOf member does not admit", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: closedA(`"a"`, `{"properties":{"a":{}},"additionalProperties":false}`), current: closedA(`"a","x"`, `{"properties":{"a":{}},"additionalProperties":false}`)},
		{name: "control: the closed member admits the field by name",
			base: closedA(`"a"`, `{"properties":{"a":{},"x":{}},"additionalProperties":false}`), current: closedA(`"a","x"`, `{"properties":{"a":{},"x":{}},"additionalProperties":false}`)},
		{name: "control: the closed member admits the field by pattern",
			base: closedA(`"a"`, `{"properties":{"a":{}},"patternProperties":{"^x$":{}},"additionalProperties":false}`), current: closedA(`"a","x"`, `{"properties":{"a":{}},"patternProperties":{"^x$":{}},"additionalProperties":false}`)},
		{name: "the body requires a field its closed allOf member does not admit", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: root(``, declaresAX+`,{"properties":{"a":{}},"additionalProperties":false}`, ``), current: root(`"required":["x"],`, declaresAX+`,{"properties":{"a":{}},"additionalProperties":false}`, ``)},
		{name: "the body requires a field its unevaluatedProperties-false member does not admit", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: root(``, declaresAX+`,{"properties":{"a":{}},"unevaluatedProperties":false}`, ``), current: root(`"required":["x"],`, declaresAX+`,{"properties":{"a":{}},"unevaluatedProperties":false}`, ``)},
		// unevaluatedProperties counts what the subschema's own applicators
		// declare: the allOf member declares x, so x is admitted.
		{name: "control: unevaluatedProperties false admits a field its own allOf declares",
			base: root(``, declaresAX, `,"unevaluatedProperties":false`), current: root(`"required":["x"],`, declaresAX, `,"unevaluatedProperties":false`)},
		// The second member's own allOf holds an additionalProperties that
		// evaluates every name, so its unevaluatedProperties false refuses
		// none: x, declared by the first member, stays declared.
		{name: "control: unevaluatedProperties false over an additionalProperties schema admits every field",
			base:    root(``, declaresAX+`,{"allOf":[{"additionalProperties":{}}],"unevaluatedProperties":false}`, ``),
			current: root(`"required":["x"],`, declaresAX+`,{"allOf":[{"additionalProperties":{}}],"unevaluatedProperties":false}`, ``)},
		{name: "control: unevaluatedProperties false over an inner unevaluatedProperties that admits every field",
			base:    root(``, declaresAX+`,{"allOf":[{"unevaluatedProperties":{}}],"unevaluatedProperties":false}`, ``),
			current: root(`"required":["x"],`, declaresAX+`,{"allOf":[{"unevaluatedProperties":{}}],"unevaluatedProperties":false}`, ``)},
		// A closed subschema under a then forbids only where the then
		// applies; outside it the body's declaration stands.
		{name: "control: a closed subschema under a then does not forbid outside it",
			base:    one(`{"type":"object","properties":{"f":{},"x":{}},"if":{"required":["f"]},"then":{"properties":{"f":{}},"additionalProperties":false}}`),
			current: one(`{"type":"object","required":["x"],"properties":{"f":{},"x":{}},"if":{"required":["f"]},"then":{"properties":{"f":{}},"additionalProperties":false}}`)},
		{name: "a then requires a field its own closed subschema does not admit", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"allOf":[` + declaresAX + `],"properties":{"f":{}},"additionalProperties":false}}`),
			current: one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"required":["x"],"allOf":[` + declaresAX + `],"properties":{"f":{}},"additionalProperties":false}}`)},
	})
}

// resAt puts R at /res: directly, or wrapped, beside S, in a new outer anyOf,
// as #1847 wrapped full_result in anyOf [result.v1, result.v2]. Wrapping
// adds an outer branch element to everything inside R; what held under R's
// own branches still holds there.
func resAt(wrapped bool, r string) map[string]string {
	res := `{"$ref":"#/$defs/R"}`
	if wrapped {
		res = `{"anyOf":[{"$ref":"#/$defs/R"},{"$ref":"#/$defs/S"}]}`
	}
	return one(`{"type":"object","$defs":{"R":` + r + `,"S":{}},"properties":{"res":` + res + `}}`)
}

// A baseline rule holds wherever every condition of its branch path holds:
// a wrap adds conditions and removes none (CHAOS-12340).
func TestRequiredBaselineScopesByEveryBranchCondition(t *testing.T) {
	ifF := `"if":{"required":["f"]},`
	runLocationCases(t, []locationCase{
		{name: "an unchanged oneOf under a then, wrapped",
			base:    resAt(false, `{"properties":{"f":{},"a":{},"b":{}},`+ifF+`"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current: resAt(true, `{"properties":{"f":{},"a":{},"b":{}},`+ifF+`"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`)},
		{name: "an unchanged anyOf under a then, wrapped",
			base:    resAt(false, `{"properties":{"f":{},"a":{},"b":{}},`+ifF+`"then":{"anyOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current: resAt(true, `{"properties":{"f":{},"a":{},"b":{}},`+ifF+`"then":{"anyOf":[{"required":["a"]},{"required":["b"]}]}}`)},
		{name: "a new anyOf under a then whose alternative needs what the then required, wrapped",
			base:    resAt(false, `{"properties":{"f":{},"a":{},"zz":{}},`+ifF+`"then":{"required":["a"]}}`),
			current: resAt(true, `{"properties":{"f":{},"a":{},"zz":{}},`+ifF+`"then":{"required":["a"],"anyOf":[{"required":["a"]},{"required":["zz"]}]}}`)},
		{name: "an unchanged requirement under a then of a field never declared, wrapped",
			base:    resAt(false, `{"properties":{"f":{}},`+ifF+`"then":{"required":["x"]}}`),
			current: resAt(true, `{"properties":{"f":{}},`+ifF+`"then":{"required":["x"]}}`)},
		{name: "a then requires a field it declared, wrapped",
			base:    resAt(false, `{"properties":{"f":{}},`+ifF+`"then":{"properties":{"x":{}}}}`),
			current: resAt(true, `{"properties":{"f":{}},`+ifF+`"then":{"required":["x"],"properties":{"x":{}}}}`)},
		// The report weighs the baseline group whose conditions all still
		// hold: it names the group with its baseline count, not a and b.
		{name: "a oneOf under a then loses an alternative, wrapped", refused: true,
			mentions: []string{`the oneOf at instance location "/res" changed its alternatives (3 at v1.0.0, 2 now)`}, absent: []string{`field "a"`, `field "b"`},
			base:    resAt(false, `{"properties":{"f":{}},`+ifF+`"then":{"properties":{"a":{},"b":{},"c":{}},"oneOf":[{"required":["a"]},{"required":["b"]},{"required":["c"]}]}}`),
			current: resAt(true, `{"properties":{"f":{}},`+ifF+`"then":{"properties":{"a":{},"b":{},"c":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}}`)},
		// A baseline condition absent from the current path: the oneOf left
		// its then, so a body at /res without f now meets it.
		{name: "control: a baseline condition the current path lacks", refused: true, mentions: []string{`instance location "/res"`},
			base:    resAt(true, `{"properties":{"f":{},"a":{},"b":{}},`+ifF+`"then":{"oneOf":[{"required":["a"]},{"required":["b"]}]}}`),
			current: resAt(true, `{"properties":{"f":{},"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`)},
		// Pairing keeps exact branch equality, which is conservative: the
		// same $defs group (same schema position) moved into a then, so it
		// is not paired, and x is refused.
		{name: "control: a paired group that moved into a then is not paired", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{}},"$defs":{` + defA + `,` + defB + `,"D":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}},"allOf":[{"$ref":"#/$defs/D"}]}`),
			current: one(`{"type":"object","properties":{"f":{}},"$defs":{` + defAx + `,` + defB + `,"D":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}},` + ifF + `"then":{"$ref":"#/$defs/D"}}`)},
	})
}

// A forbid holds wherever all its conditions hold, also under a path that
// reached its branch through another one. BOX's then forbids x through N.
// The baseline reaches BOX twice: from /box directly (path [then]), and
// through the root anyOf's N alternative (path [N alternative][then]), where
// the $ref back to N stops on the recursion guard. The x declared in BOX's
// then under the second path is still forbidden there.
func TestRequiredBaselineForbidHoldsUnderEveryPathItsConditionsHold(t *testing.T) {
	schema := func(n string, rootBox bool, then string) map[string]string {
		box := ``
		if rootBox {
			box = `"properties":{"box":{"$ref":"#/$defs/BOX"}},`
		}
		return one(`{"type":"object",` + box + `"$defs":{"N":` + n + `,` +
			`"BOX":{"properties":{"f":{}},"if":{"required":["f"]},"then":` + then + `},"Z":{}},` +
			`"anyOf":[{"$ref":"#/$defs/N"},{"$ref":"#/$defs/Z"}]}`)
	}
	declares := `{"properties":{"x":{}},"allOf":[{"$ref":"#/$defs/N"}]}`
	requires := `{"required":["x"],"properties":{"x":{}},"allOf":[{"$ref":"#/$defs/N"}]}`
	named := `{"properties":{"x":false,"box":{"$ref":"#/$defs/BOX"}}}`
	closed := `{"properties":{"f":{},"box":{"$ref":"#/$defs/BOX"}},"additionalProperties":false}`
	runLocationCases(t, []locationCase{
		{name: "a field its then forbids by name, required where the then is reached another way", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base: schema(named, true, declares), current: schema(named, false, requires)},
		{name: "a field its then's closed subschema does not admit, required where the then is reached another way", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base: schema(closed, true, declares), current: schema(closed, false, requires)},
	})
}

// Every branch element names its condition by absolute place: two if, two
// dependentSchemas or two anyOf blocks alike in content but at different
// schema positions are different branches, so a requirement moved from one
// to the other is refused.
func TestRequiredBaselineBranchElementsNameTheirPosition(t *testing.T) {
	ifThen := func(required string) string {
		return `{"if":{"required":["f"]},"then":{` + required + `}}`
	}
	dependent := func(required string) string {
		return `{"dependentSchemas":{"f":{` + required + `}}}`
	}
	group := func(required string) string {
		return `{"anyOf":[{"$ref":"#/$defs/R","properties":{"box":{"type":"object"` + required + `}}},{"$ref":"#/$defs/K"}]}`
	}
	allOf := func(first, second string) map[string]string {
		return one(`{"type":"object","properties":{"f":{},"box":{"type":"object"}},"$defs":{"R":{},"K":{}},"allOf":[` + first + `,` + second + `]}`)
	}
	defs := func(d1, d2 string) map[string]string {
		return one(`{"type":"object","properties":{"f":{}},"$defs":{"D1":` + d1 + `,"D2":` + d2 + `},"allOf":[{"$ref":"#/$defs/D1"},{"$ref":"#/$defs/D2"}]}`)
	}
	requireX := `"required":["x"]`
	runLocationCases(t, []locationCase{
		{name: "a requirement moves between two identical if blocks in one allOf", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: allOf(ifThen(requireX), ifThen(``)), current: allOf(ifThen(``), ifThen(requireX))},
		{name: "a requirement moves between identical if blocks in two $ref'd defs", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: defs(ifThen(requireX), ifThen(``)), current: defs(ifThen(``), ifThen(requireX))},
		{name: "a requirement moves between two identical dependentSchemas blocks in one allOf", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: allOf(dependent(requireX), dependent(``)), current: allOf(dependent(``), dependent(requireX))},
		{name: "a requirement moves between two identical $ref groups in one allOf", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base: allOf(group(`,`+requireX), group(``)), current: allOf(group(``), group(`,`+requireX))},
	})
}
