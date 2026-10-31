package contractcheck

import (
	"strings"
	"testing"
)

// CHAOS-12331: a requirement inside a then, else or dependentSchemas entry, or
// inside a $ref alternative of an anyOf or oneOf, counts a declaration the
// baseline made in the same branch. A then or else is named by its location,
// schema position and a digest of its if; a dependentSchemas entry by its
// key; a $ref alternative by its group's location, keyword and count, its
// index and its target. A group of $ref alternatives paired with the
// baseline's by target, order, keyword, count and enclosing branch may let
// an alternative require a field its own baseline branch declared, and lose
// none. An inline alternative is known only by its position, so a reorder
// could fake it: it keeps today's rule.

// The repro, after acr PR #1836: an event whose result is a oneOf of two
// response files; the v2 response newly requires a field it declared, and an
// object inside the v2 branch newly requires a field it declared.
func branchCommon(v2Required string) string {
	return `{"$defs":{"Needs":{"type":"object","required":["missing"],"properties":{"missing":{},"anchors":{}}},` +
		`"NeedsV2":{"type":"object","required":[` + v2Required + `],"properties":{"missing":{},"anchors":{}}}}}`
}

func branchResponse(kind, needs, required, extra string) string {
	return `{"type":"object","required":[` + required + `],"properties":{"id":{},"kind":{"const":"` + kind + `"},` +
		`"needs":{"$ref":"common.v1.schema.json#/$defs/` + needs + `"}` + extra + `}}`
}

func branchEvent(refs ...string) string {
	alternatives := make([]string, len(refs))
	for i, ref := range refs {
		alternatives[i] = `{"$ref":"` + ref + `"}`
	}
	return `{"type":"object","required":["type"],"properties":{"type":{},"result":{"oneOf":[` + strings.Join(alternatives, ",") + `]}}}`
}

func branchBase() map[string]string {
	return map[string]string{
		"event.v1.schema.json":  branchEvent("resp.v1.schema.json", "resp.v2.schema.json"),
		"resp.v1.schema.json":   branchResponse("v1", "Needs", `"id","kind"`, ``),
		"resp.v2.schema.json":   branchResponse("v2", "NeedsV2", `"id","kind"`, ``),
		"common.v1.schema.json": branchCommon(`"missing"`),
	}
}

// refGroup is one schema whose root keyword (anyOf or oneOf) is a list of
// $refs to its own $defs, in the order named; extra goes into the root.
func refGroup(keyword, extra, defs string, names ...string) string {
	alternatives := make([]string, len(names))
	for i, name := range names {
		alternatives[i] = `{"$ref":"#/$defs/` + name + `"}`
	}
	return `{"type":"object",` + extra + `"$defs":{` + defs + `},"` + keyword + `":[` + strings.Join(alternatives, ",") + `]}`
}

const (
	defA  = `"A":{"properties":{"a":{},"x":{}},"required":["a"]}`
	defAx = `"A":{"properties":{"a":{},"x":{}},"required":["a","x"]}`
	defB  = `"B":{"properties":{"b":{}},"required":["b"]}`
	defC  = `"C":{"properties":{"c":{}},"required":["c"]}`
	defK  = `"K":{"properties":{"k":{}}}`
	// boxX declares box.x; boxXRequired requires it; boxY declares box.y.
	boxX         = `{"properties":{"box":{"type":"object","properties":{"x":{}}}}}`
	boxXRequired = `{"properties":{"box":{"type":"object","required":["x"],"properties":{"x":{}}}}}`
	boxY         = `{"properties":{"box":{"type":"object","properties":{"y":{}}}}}`
	boxYXRequire = `{"properties":{"box":{"type":"object","required":["x"],"properties":{"x":{},"y":{}}}}}`
)

func TestRequiredBaselineBranchReproPR1836(t *testing.T) {
	tightV2 := branchResponse("v2", "NeedsV2", `"id","kind","needs"`, ``)
	runLocationCases(t, []locationCase{
		{name: "the v2 alternative requires a field it declared, and its nested object one it declared", base: branchBase(),
			current: map[string]string{"resp.v2.schema.json": tightV2, "common.v1.schema.json": branchCommon(`"missing","anchors"`)}},
		{name: "only the v2 alternative tightens", base: branchBase(),
			current: map[string]string{"resp.v2.schema.json": tightV2}},
		{name: "only the object inside the v2 alternative tightens", base: branchBase(),
			current: map[string]string{"common.v1.schema.json": branchCommon(`"missing","anchors"`)}},
	})
}

// What the widened rule accepts.
func TestRequiredBaselineBranchAccepts(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "a $ref oneOf alternative requires a field it declared",
			base: one(refGroup("oneOf", ``, defA+","+defB, "A", "B")), current: one(refGroup("oneOf", ``, defAx+","+defB, "A", "B"))},
		{name: "a $ref alternative requires a field its allOf member declared",
			base:    one(refGroup("oneOf", ``, `"A":{"allOf":[{"properties":{"a":{},"x":{}}}],"required":["a"]},`+defB, "A", "B")),
			current: one(refGroup("oneOf", ``, `"A":{"allOf":[{"properties":{"a":{},"x":{}}}],"required":["a","x"]},`+defB, "A", "B"))},
		{name: "a then requires a field it declared",
			base:    one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"properties":{"x":{}}}}`),
			current: one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"then":{"required":["x"],"properties":{"x":{}}}}`)},
		{name: "an else requires a field it declared",
			base:    one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"else":{"properties":{"x":{}}}}`),
			current: one(`{"type":"object","properties":{"f":{}},"if":{"required":["f"]},"else":{"required":["x"],"properties":{"x":{}}}}`)},
		{name: "a dependentSchemas entry requires a field it declared",
			base:    one(`{"type":"object","properties":{"f":{}},"dependentSchemas":{"f":{"properties":{"x":{}}}}}`),
			current: one(`{"type":"object","properties":{"f":{}},"dependentSchemas":{"f":{"required":["x"],"properties":{"x":{}}}}}`)},
		{name: "a then inside a $ref alternative requires a field the alternative declared",
			base:    one(refGroup("anyOf", ``, `"A":{"properties":{"box":{"type":"object","properties":{"x":{},"f":{}}}}},`+defK, "A", "K")),
			current: one(refGroup("anyOf", ``, `"A":{"properties":{"box":{"type":"object","properties":{"x":{},"f":{}},"if":{"required":["f"]},"then":{"required":["x"]}}}},`+defK, "A", "K"))},
	})
}

// Controls the widened rule must still refuse; the checker before
// CHAOS-12331 refuses each of them too.
func TestRequiredBaselineBranchControls(t *testing.T) {
	withZZ := branchBase()
	withZZ["resp.v1.schema.json"] = branchResponse("v1", "Needs", `"id","kind"`, `,"zz":{}`)
	tightV2 := branchResponse("v2", "NeedsV2", `"id","kind","needs"`, ``)
	runLocationCases(t, []locationCase{
		// (1) zz is new to the v2 alternative: its baseline branch never
		// declared it (the v1 branch did). The refusal names zz alone, not
		// the alternatives' unchanged fields.
		{name: "an alternative requires a field its baseline branch never declared", base: withZZ, refused: true,
			current:  map[string]string{"resp.v2.schema.json": branchResponse("v2", "NeedsV2", `"id","kind","zz"`, `,"zz":{}`)},
			mentions: []string{`event.v1.schema.json: field "zz" at instance location "/result"`},
			absent:   []string{`field "id"`, `field "kind"`, `field "needs"`}},
		// (2) the number, and separately the order, of the alternatives
		// changes in the same release as a tightening.
		{name: "an alternative is added beside a tightening", base: branchBase(), refused: true,
			current: map[string]string{"event.v1.schema.json": branchEvent("resp.v1.schema.json", "resp.v2.schema.json", "resp.v3.schema.json"),
				"resp.v3.schema.json": branchResponse("v3", "NeedsV2", `"id","kind"`, ``), "resp.v2.schema.json": tightV2},
			mentions: []string{`event.v1.schema.json: field "needs" at instance location "/result"`},
			absent:   []string{`field "id"`, `field "kind"`}},
		{name: "the alternatives are reordered beside a tightening", base: branchBase(), refused: true,
			current:  map[string]string{"event.v1.schema.json": branchEvent("resp.v2.schema.json", "resp.v1.schema.json"), "resp.v2.schema.json": tightV2},
			mentions: []string{`event.v1.schema.json: field "needs" at instance location "/result"`}},
		{name: "the alternatives are reordered beside a nested tightening", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(refGroup("anyOf", ``, `"A":`+boxX+`,`+defK, "A", "K")),
			current: one(refGroup("anyOf", ``, `"A":`+boxXRequired+`,`+defK, "K", "A"))},
		{name: "an alternative is added beside a nested tightening", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(refGroup("anyOf", ``, `"A":`+boxX+`,`+defK+`,"J":{"properties":{"j":{}}}`, "A", "K")),
			current: one(refGroup("anyOf", ``, `"A":`+boxXRequired+`,`+defK+`,"J":{"properties":{"j":{}}}`, "A", "K", "J"))},
		// Positionally each alternative only gains what the baseline
		// alternative at its index declared, but B moved: B now requires x,
		// which only A declared.
		{name: "a reordered $ref alternative gains a field only the other one declared", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(refGroup("oneOf", ``, `"A":{"properties":{"k":{},"x":{}},"required":["k"]},"B":{"properties":{"k":{}},"required":["k"]}`, "A", "B")),
			current: one(refGroup("oneOf", ``, `"A":{"properties":{"k":{},"x":{}},"required":["k"]},"B":{"properties":{"k":{},"x":{}},"required":["k","x"]}`, "B", "A"))},
		// (3) c moves from B's requirements to A's. A gains c, which its
		// baseline branch declared, but B loses c: an old body {a, b} met
		// only A and now meets B too, so it fails the oneOf.
		{name: "a required field moves from one inline alternative to another", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (2 at v1.0.0, 2 now)`}, absent: []string{`field "c"`},
			base:    one(`{"type":"object","oneOf":[{"properties":{"a":{},"c":{}},"required":["a"]},{"properties":{"b":{},"c":{}},"required":["b","c"]}]}`),
			current: one(`{"type":"object","oneOf":[{"properties":{"a":{},"c":{}},"required":["a","c"]},{"properties":{"b":{},"c":{}},"required":["b"]}]}`)},
		// Paired: A's gain of c is allowed (A declared c), so the refusal
		// names only what B lost.
		{name: "a required field moves from one $ref alternative to another", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed: alternative 2 no longer requires "c"`},
			absent:   []string{`field "a"`, `field "b"`, `field "c"`},
			base:     one(refGroup("oneOf", ``, `"A":{"properties":{"a":{},"c":{}},"required":["a"]},"B":{"properties":{"b":{},"c":{}},"required":["b","c"]}`, "A", "B")),
			current:  one(refGroup("oneOf", ``, `"A":{"properties":{"a":{},"c":{}},"required":["a","c"]},"B":{"properties":{"b":{},"c":{}},"required":["b"]}`, "A", "B"))},
		// A nested requirement inside alternative 1 whose field only
		// alternative 0 declared there.
		{name: "a nested requirement declared only in another $ref alternative", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(refGroup("anyOf", ``, `"A":`+boxX+`,"B":`+boxY, "A", "B")),
			current: one(refGroup("anyOf", ``, `"A":`+boxX+`,"B":`+boxYXRequire, "A", "B"))},
		{name: "a nested requirement declared only in another inline alternative", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(`{"type":"object","anyOf":[` + boxX + `,` + boxY + `]}`),
			current: one(`{"type":"object","anyOf":[` + boxX + `,` + boxYXRequire + `]}`)},
		{name: "a nested requirement in a retargeted alternative", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(refGroup("anyOf", ``, `"A":`+boxX+`,"B":`+boxY, "A", "B")),
			current: one(refGroup("anyOf", ``, `"A":`+boxX+`,"B":`+boxY+`,"B2":`+boxYXRequire, "B2", "A"))},
		// Inline alternatives keep today's rule: a requirement inside one is
		// met only by a declaration outside every alternative.
		{name: "an inline oneOf alternative requires a field it declared", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","oneOf":[{"properties":{"a":{},"x":{}},"required":["a"]},{"properties":{"b":{}},"required":["b"]}]}`),
			current: one(`{"type":"object","oneOf":[{"properties":{"a":{},"x":{}},"required":["a","x"]},{"properties":{"b":{}},"required":["b"]}]}`)},
		{name: "a nested requirement inside the inline alternative that declared it", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base:    one(`{"type":"object","anyOf":[` + boxX + `,{"properties":{"k":{}}}]}`),
			current: one(`{"type":"object","anyOf":[` + boxXRequired + `,{"properties":{"k":{}}}]}`)},
	})
}

// Pairing: a group pairs with the baseline group at its location only when
// keyword, alternative count, enclosing branch and every $ref target in order
// agree.
func TestRequiredBaselineBranchPairing(t *testing.T) {
	runLocationCases(t, []locationCase{
		{name: "an anyOf loses an alternative beside a tightening", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(refGroup("anyOf", ``, defA+","+defB+","+defC, "A", "B", "C")),
			current: one(refGroup("anyOf", ``, defAx+","+defB+","+defC, "A", "B"))},
		{name: "an anyOf becomes a oneOf beside a tightening", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(refGroup("anyOf", ``, defA+","+defB, "A", "B")),
			current: one(refGroup("oneOf", ``, defAx+","+defB, "A", "B"))},
		// Two oneOfs at one location: the second may not borrow the first's
		// declaration.
		{name: "a second group at the same location tightens", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: one(`{"type":"object","$defs":{"A1":{"properties":{"a":{},"x":{}},"required":["a"]},"A2":{"properties":{"a":{}},"required":["a"]},` + defB + `},` +
				`"allOf":[{"oneOf":[{"$ref":"#/$defs/A1"},{"$ref":"#/$defs/B"}]},{"oneOf":[{"$ref":"#/$defs/A2"},{"$ref":"#/$defs/B"}]}]}`),
			current: one(`{"type":"object","$defs":{"A1":{"properties":{"a":{},"x":{}},"required":["a"]},"A2":{"properties":{"a":{},"x":{}},"required":["a","x"]},` + defB + `},` +
				`"allOf":[{"oneOf":[{"$ref":"#/$defs/A1"},{"$ref":"#/$defs/B"}]},{"oneOf":[{"$ref":"#/$defs/A2"},{"$ref":"#/$defs/B"}]}]}`)},
		// The same $defs group, under a then at the baseline and
		// unconditional now: old bodies without f never met it.
		{name: "a group that left its then tightens", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{}},"$defs":{` + defA + `,` + defB + `,"D":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}},"if":{"required":["f"]},"then":{"$ref":"#/$defs/D"}}`),
			current: one(`{"type":"object","properties":{"f":{}},"$defs":{` + defAx + `,` + defB + `,"D":{"oneOf":[{"$ref":"#/$defs/A"},{"$ref":"#/$defs/B"}]}},"$ref":"#/$defs/D"}`)},
	})
}

// Reporting: an uncovered group names what changed, and never nothing.
func TestRequiredBaselineBranchReporting(t *testing.T) {
	withK := `"properties":{"k":{}},`
	runLocationCases(t, []locationCase{
		{name: "a oneOf that gains an alternative names only the new field", refused: true,
			mentions: []string{`field "zz" at instance location "/"`}, absent: []string{`field "a"`, `field "b"`},
			base:    one(`{"type":"object","oneOf":[{"properties":{"a":{}},"required":["a"]},{"properties":{"b":{}},"required":["b"]}]}`),
			current: one(`{"type":"object","oneOf":[{"properties":{"a":{}},"required":["a"]},{"properties":{"b":{}},"required":["b"]},{"properties":{"zz":{}},"required":["zz"]}]}`)},
		// An old body {a, c} met only B; A no longer requires b, so it meets
		// both and fails the oneOf. The group is not paired (its alternatives
		// are inline) and no field is new, so the refusal names the group.
		{name: "a oneOf alternative that only loses a requirement", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (2 at v1.0.0, 2 now)`},
			absent:   []string{`field "a"`, `field "c"`, `field "k"`},
			base:     one(`{"type":"object",` + withK + `"oneOf":[{"properties":{"a":{},"b":{}},"required":["k","a","b"]},{"properties":{"c":{}},"required":["k","c"]}]}`),
			current:  one(`{"type":"object",` + withK + `"oneOf":[{"properties":{"a":{},"b":{}},"required":["k","a"]},{"properties":{"c":{}},"required":["k","c"]}]}`)},
		{name: "a paired oneOf alternative that only loses a requirement", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed: alternative 1 no longer requires "b"`},
			absent:   []string{`field "a"`, `field "c"`, `field "k"`},
			base:     one(refGroup("oneOf", withK, `"A":{"properties":{"a":{},"b":{}},"required":["k","a","b"]},"B":{"properties":{"c":{}},"required":["k","c"]}`, "A", "B")),
			current:  one(refGroup("oneOf", withK, `"A":{"properties":{"a":{},"b":{}},"required":["k","a"]},"B":{"properties":{"c":{}},"required":["k","c"]}`, "A", "B"))},
		// Codex #1840 r2 P3: an old body {c} met the third alternative only;
		// a and b are not newly required, so the refusal names the group.
		{name: "a oneOf that loses an alternative", refused: true,
			mentions: []string{`a.v1.schema.json: the oneOf at instance location "/" changed its alternatives (3 at v1.0.0, 2 now)`},
			absent:   []string{`field "a"`, `field "b"`, `field "c"`},
			base:     one(`{"type":"object","oneOf":[{"properties":{"a":{}},"required":["a"]},{"properties":{"b":{}},"required":["b"]},{"properties":{"c":{}},"required":["c"]}]}`),
			current:  one(`{"type":"object","oneOf":[{"properties":{"a":{}},"required":["a"]},{"properties":{"b":{}},"required":["b"]}]}`)},
		// Every field is declared outright, so no field is new; the group
		// still refuses. The checker before CHAOS-12331 passed these
		// silently.
		{name: "a oneOf over fields the baseline declared outright loses a requirement", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (2 at v1.0.0, 2 now)`}, absent: []string{`field "a"`},
			base:    one(`{"type":"object","properties":{"a":{},"b":{},"c":{}},"oneOf":[{"required":["a","b"]},{"required":["c"]}]}`),
			current: one(`{"type":"object","properties":{"a":{},"b":{},"c":{}},"oneOf":[{"required":["a"]},{"required":["c"]}]}`)},
		{name: "a new oneOf over fields the baseline declared outright", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed its alternatives (0 at v1.0.0, 2 now)`}, absent: []string{`field "a"`, `field "b"`},
			base:    one(`{"type":"object","properties":{"a":{},"b":{}}}`),
			current: one(`{"type":"object","properties":{"a":{},"b":{}},"oneOf":[{"required":["a"]},{"required":["b"]}]}`)},
		{name: "a new oneOf names the fields the baseline did not declare outright", refused: true,
			mentions: []string{`field "zz" at instance location "/"`}, absent: []string{`field "k"`, `field "a"`},
			base:    one(`{"type":"object","properties":{"k":{},"a":{}}}`),
			current: one(`{"type":"object","properties":{"k":{},"a":{},"zz":{}},"oneOf":[{"required":["k","a"]},{"required":["zz"]}]}`)},
		{name: "a new oneOf whose alternatives require nothing", refused: true,
			mentions: []string{`the oneOf at instance location "/v" changed since v1.0.0 in a way the check cannot weigh`},
			base:     one(`{"type":"object","properties":{"v":{}}}`),
			current:  one(`{"type":"object","properties":{"v":{"oneOf":[{"type":"object"},{"type":"object","properties":{"w":{}}}]}}}`)},
	})
}

// A paired group whose alternative only lost a requirement is refused (an old
// body may now meet two alternatives of the oneOf), with one line naming the
// lost field, not a list of unchanged fields as "required now" (codex #1840
// r1 P2-5).
func TestRequiredBaselineBranchReportsALostRequirement(t *testing.T) {
	kinds := func(bRequired, bExtra string) string {
		return refGroup("oneOf", ``, `"A":{"properties":{"kind":{"const":"a"},"a":{}},"required":["kind","a"]},`+
			`"B":{"properties":{"kind":{"const":"b"},"b":{},"c":{}`+bExtra+`},"required":[`+bRequired+`]}`, "A", "B")
	}
	runLocationCases(t, []locationCase{
		{name: "a $ref alternative with a const discriminator loses a requirement", refused: true,
			mentions: []string{`a.v1.schema.json: the oneOf at instance location "/" changed: alternative 2 no longer requires "b"; a paired alternative may only gain fields its baseline branch declared (CHAOS-12331)`},
			absent:   []string{`field "a"`, `field "b"`, `field "c"`, `field "kind"`},
			base:     one(kinds(`"kind","b","c"`, ``)), current: one(kinds(`"kind","c"`, ``))},
		{name: "a $ref alternative loses a requirement and gains an undeclared one", refused: true,
			mentions: []string{`the oneOf at instance location "/" changed: alternative 2 no longer requires "b"`, `field "zz" at instance location "/" is required now`},
			absent:   []string{`field "a"`, `field "c"`, `field "kind"`},
			base:     one(kinds(`"kind","b","c"`, ``)), current: one(kinds(`"kind","c","zz"`, `,"zz":{}`))},
	})
}

// Codex #1840 r2: two $ref groups at one location are different groups even
// with the same targets in order: a declaration made beside a $ref in the
// first never covers a requirement in the second. The baseline body
// {"a":true,"b":true,"z":true} meets A in both groups (N refuses a body with
// a); requiring x in the second group's A breaks it.
func TestRequiredBaselineBranchSeparatesGroupsByPosition(t *testing.T) {
	groups := func(first0, second0 string) string {
		return `{"type":"object","properties":{"a":{},"b":{},"z":{}},"$defs":{"A":{"required":["a"]},"N":{"not":{"required":["a"]}}},"allOf":[` +
			`{"oneOf":[` + first0 + `,{"$ref":"#/$defs/N"}]},{"oneOf":[` + second0 + `,{"$ref":"#/$defs/N"}]}]}`
	}
	runLocationCases(t, []locationCase{
		{name: "a second $ref group requires a field declared beside the first group's $ref", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(groups(`{"$ref":"#/$defs/A","properties":{"x":{}}}`, `{"$ref":"#/$defs/A"}`)),
			current: one(groups(`{"$ref":"#/$defs/A","properties":{"x":{}}}`, `{"$ref":"#/$defs/A","required":["x"]}`))},
		{name: "a second $ref group requires a nested field only the first group declared", refused: true, mentions: []string{`field "x" at instance location "/box"`},
			base: one(groups(`{"$ref":"#/$defs/A","properties":{"box":{"type":"object","properties":{"x":{}}}}}`,
				`{"$ref":"#/$defs/A","properties":{"box":{"type":"object"}}}`)),
			current: one(groups(`{"$ref":"#/$defs/A","properties":{"box":{"type":"object","properties":{"x":{}}}}}`,
				`{"$ref":"#/$defs/A","properties":{"box":{"type":"object","required":["x"]}}}`))},
	})
}

// Codex #1840 r2: a property some part of the schema forbids (false) is not
// declared there, even when another part declares it. A forbid counts where
// it applies: at its own branch and every branch inside it.
func TestRequiredBaselineBranchForbiddenIsNotDeclared(t *testing.T) {
	runLocationCases(t, []locationCase{
		// The baseline body {"a":true} meets A; A now requires x, which A
		// forbids, so it breaks.
		{name: "an alternative requires a field it forbids beside an allOf member declaring it", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"A":{"properties":{"x":false},"allOf":[{"properties":{"x":{}}}],"required":["a"]},"B":{"required":["b"]}`, "A", "B")),
			current: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"A":{"properties":{"x":false},"allOf":[{"properties":{"x":{}}}],"required":["a","x"]},"B":{"required":["b"]}`, "A", "B"))},
		{name: "an alternative requires a field its allOf member forbids", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"A":{"properties":{"x":{}},"allOf":[{"properties":{"x":false}}],"required":["a"]},"B":{"required":["b"]}`, "A", "B")),
			current: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"A":{"properties":{"x":{}},"allOf":[{"properties":{"x":false}}],"required":["a","x"]},"B":{"required":["b"]}`, "A", "B"))},
		{name: "an alternative requires a field its $ref target forbids", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"F":{"properties":{"x":false}},"A":{"$ref":"#/$defs/F","properties":{"x":{}},"required":["a"]},"B":{"required":["b"]}`, "A", "B")),
			current: one(refGroup("oneOf", `"properties":{"a":{},"b":{}},`,
				`"F":{"properties":{"x":false}},"A":{"$ref":"#/$defs/F","properties":{"x":{}},"required":["a","x"]},"B":{"required":["b"]}`, "A", "B"))},
		{name: "an alternative requires a field the body forbids outright", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base: one(refGroup("oneOf", `"properties":{"a":{},"b":{},"x":false},`,
				`"A":{"properties":{"x":{}},"required":["a"]},"B":{"required":["b"]}`, "A", "B")),
			current: one(refGroup("oneOf", `"properties":{"a":{},"b":{},"x":false},`,
				`"A":{"properties":{"x":{}},"required":["a","x"]},"B":{"required":["b"]}`, "A", "B"))},
		{name: "the body requires a field it forbids beside an allOf member declaring it", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"a":{},"x":false},"allOf":[{"properties":{"x":{}}}]}`),
			current: one(`{"type":"object","required":["x"],"properties":{"a":{},"x":false},"allOf":[{"properties":{"x":{}}}]}`)},
		{name: "a then requires a field it declares and the body forbids", refused: true, mentions: []string{`field "x" at instance location "/"`},
			base:    one(`{"type":"object","properties":{"f":{},"x":false},"if":{"required":["f"]},"then":{"properties":{"x":{}}}}`),
			current: one(`{"type":"object","properties":{"f":{},"x":false},"if":{"required":["f"]},"then":{"required":["x"],"properties":{"x":{}}}}`)},
		// A forbid under a then holds only where the then applies: outside
		// it the field stays declared.
		{name: "control: a field forbidden only under a then stays declared outside it",
			base:    one(`{"type":"object","properties":{"f":{},"x":{}},"if":{"required":["f"]},"then":{"properties":{"x":false}}}`),
			current: one(`{"type":"object","required":["x"],"properties":{"f":{},"x":{}},"if":{"required":["f"]},"then":{"properties":{"x":false}}}`)},
	})
}
