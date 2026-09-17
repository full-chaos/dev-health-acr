package contextfabric

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// withExtensionsKey returns fixture's encoded snapshot with its extensions key
// set to raw, or removed when raw is empty.
func withExtensionsKey(t *testing.T, fixture *PersistedSemanticState, raw string) []byte {
	t.Helper()
	encoded, err := EncodeSemanticState(fixture)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	delete(document, "extensions")
	if raw != "" {
		document["extensions"] = json.RawMessage(raw)
	}
	column, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	return column
}

// TestSemanticStateExtensionsReadDomain executes every shape the extensions
// key can take in a stored column. A member this build knows nothing about
// never fails the read, and the rest of the reading is exactly the reading
// of the same row without the key.
func TestSemanticStateExtensionsReadDomain(t *testing.T) {
	fixture := semanticFixture(t)
	baseline, status := DecodeSemanticState(withExtensionsKey(t, fixture, ""))
	if status != SemanticStateReadAvailable {
		t.Fatalf("fixture defect: the row without extensions reads %s", status)
	}
	baselineBytes, err := EncodeSemanticState(baseline)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	// Padding that lands the whole column on the given length.
	padFor := func(total int) string {
		probe := withExtensionsKey(t, fixture, `{"pad":""}`)
		return `{"pad":"` + strings.Repeat("x", total-len(probe)) + `"}`
	}
	nul := `\` + "u0000" // the JSON escape for NUL
	for _, cell := range []struct {
		name    string
		raw     string
		want    SemanticStateReadStatus
		members map[string]string
	}{
		{"key absent", "", SemanticStateReadAvailable, nil},
		{"one unknown member", `{"future_member":{"state":"bound","nested":{"deeper":[1,2]}}}`, SemanticStateReadAvailable, map[string]string{"future_member": `{"state":"bound","nested":{"deeper":[1,2]}}`}},
		{"members of every JSON type", `{"o":{},"a":[],"s":"x","n":-1.5,"b":false,"z":null}`, SemanticStateReadAvailable, map[string]string{"o": `{}`, "a": `[]`, "s": `"x"`, "n": `-1.5`, "b": `false`, "z": `null`}},
		{"an empty member name", `{"":1}`, SemanticStateReadAvailable, map[string]string{"": `1`}},
		{"a duplicated member name keeps the last value", `{"m":1,"m":2}`, SemanticStateReadAvailable, map[string]string{"m": `2`}},
		{"whitespace and key order inside a member", `{"m": { "b" : 1 , "a" : 2 } }`, SemanticStateReadAvailable, map[string]string{"m": `{"b":1,"a":2}`}},
		{"a column exactly at the cap", padFor(SemanticStateMaxEncodedBytes), SemanticStateReadAvailable, map[string]string{"pad": ""}},
		{"a column one byte over the cap", padFor(SemanticStateMaxEncodedBytes + 1), SemanticStateReadOversized, nil},
		{"an empty object is never written", `{}`, SemanticStateReadMalformed, nil},
		{"null is never written", `null`, SemanticStateReadMalformed, nil},
		{"wrong container type: array", `[{"m":1}]`, SemanticStateReadMalformed, nil},
		{"wrong scalar type: string", `"m"`, SemanticStateReadMalformed, nil},
		{"wrong scalar type: number", `1`, SemanticStateReadMalformed, nil},
		{"wrong scalar type: bool", `true`, SemanticStateReadMalformed, nil},
		{"a NUL in a member value", `{"m":"a` + nul + `b"}`, SemanticStateReadMalformed, nil},
		{"a NUL in a member name", `{"a` + nul + `b":1}`, SemanticStateReadMalformed, nil},
		{"invalid UTF-8 in a member name", "{\"a\xffb\":1}", SemanticStateReadMalformed, nil},
	} {
		t.Run(cell.name, func(t *testing.T) {
			state, status := DecodeSemanticState(withExtensionsKey(t, fixture, cell.raw))
			t.Logf("cell %-48s read=%s", cell.name, status)
			if status != cell.want {
				t.Fatalf("read = %s, want %s", status, cell.want)
			}
			if status != SemanticStateReadAvailable {
				if state != nil {
					t.Fatalf("a %s read returned a snapshot", status)
				}
				return
			}
			if len(state.Extensions) != len(cell.members) {
				t.Fatalf("members = %v, want %v", state.Extensions, cell.members)
			}
			for name, want := range cell.members {
				got, ok := state.Extensions[name]
				if !ok {
					t.Fatalf("member %q missing", name)
				}
				if name == "pad" {
					continue
				}
				var compact bytes.Buffer
				if err := json.Compact(&compact, got); err != nil || compact.String() != want {
					t.Fatalf("member %q = %s, want %s", name, got, want)
				}
			}
			rest := *state
			rest.Extensions = nil
			restBytes, err := EncodeSemanticState(&rest)
			if err != nil || !bytes.Equal(restBytes, baselineBytes) {
				t.Fatalf("the rest of the reading differs from the row without extensions (err %v)", err)
			}
		})
	}
}

// TestSemanticStateExtensionsRoundTrip: a writer's members encode under the
// key and read back unchanged; an empty map writes no key; a member that is
// not JSON, or one that pushes the snapshot over the cap, is refused at
// capture.
func TestSemanticStateExtensionsRoundTrip(t *testing.T) {
	state := semanticFixture(t)
	state.Extensions = SemanticStateExtensions{"shadow_member": json.RawMessage(`{"state":"bound","graph_epoch":7}`)}
	encoded, err := EncodeSemanticState(state)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, status := DecodeSemanticState(encoded)
	if status != SemanticStateReadAvailable || !SemanticStatesEqual(decoded, state) {
		t.Fatalf("round trip: status %s, equal %v", status, SemanticStatesEqual(decoded, state))
	}

	empty := semanticFixture(t)
	empty.Extensions = SemanticStateExtensions{}
	emptyBytes, err := EncodeSemanticState(empty)
	if err != nil || bytes.Contains(emptyBytes, []byte(`"extensions"`)) {
		t.Fatalf("an empty map wrote the key (err %v)", err)
	}
	if decoded, status := DecodeSemanticState(emptyBytes); status != SemanticStateReadAvailable || decoded.Extensions != nil {
		t.Fatalf("an empty map read back as %s %+v", status, decoded)
	}

	broken := semanticFixture(t)
	broken.Extensions = SemanticStateExtensions{"m": json.RawMessage(`{`)}
	if _, err := EncodeSemanticState(broken); err == nil {
		t.Fatalf("a member that is not JSON was encoded")
	}
	huge := semanticFixture(t)
	huge.Extensions = SemanticStateExtensions{"m": json.RawMessage(`"` + strings.Repeat("x", SemanticStateMaxEncodedBytes) + `"`)}
	if _, err := EncodeSemanticState(huge); err == nil {
		t.Fatalf("a snapshot over the cap was encoded")
	}
}

// TestSemanticStateExtensionMembersAreNeverNull: a snapshot with no members
// publishes an empty list.
func TestSemanticStateExtensionMembersAreNeverNull(t *testing.T) {
	for _, extensions := range []SemanticStateExtensions{nil, {}} {
		if got := semanticStateExtensionMembers(extensions); got == nil || len(got) != 0 {
			t.Fatalf("members of %v = %#v, want an empty non-nil list", extensions, got)
		}
	}
}

// columnWithMember returns fixture's encoded snapshot carrying one extension
// member whose value is exactly raw, spliced in as bytes so an invalid value
// reaches the reader unchanged.
func columnWithMember(t *testing.T, fixture *PersistedSemanticState, raw []byte) []byte {
	t.Helper()
	encoded, err := EncodeSemanticState(fixture)
	if err != nil {
		t.Fatalf("fixture defect: %v", err)
	}
	const placeholder = `"MEMBER_VALUE_PLACEHOLDER"`
	column := withExtensionsKey(t, fixture, `{"m":`+placeholder+`}`)
	if !bytes.Contains(column, []byte(placeholder)) || len(encoded) == 0 {
		t.Fatalf("fixture defect: no placeholder")
	}
	return bytes.Replace(column, []byte(placeholder), raw, 1)
}

// TestSemanticStateExtensionValueContract executes the member value domain on
// both sides: a value the writer admits reads available and equal, and a
// value the writer refuses reads malformed when found stored.
func TestSemanticStateExtensionValueContract(t *testing.T) {
	fixture := semanticFixture(t)
	nested := func(depth int) string {
		return strings.Repeat("[", depth) + strings.Repeat("]", depth)
	}
	bs := `\`
	for _, cell := range []struct {
		name  string
		raw   string
		admit bool
		why   string
	}{
		{"an integer", `42`, true, ""},
		{"a negative fraction", `-0.5`, true, ""},
		{"zero", `0`, true, ""},
		{"trailing fractional zeros", `1.500`, true, ""},
		{"64 digits", strings.Repeat("9", 64), true, ""},
		{"64 digits across the point", strings.Repeat("9", 32) + "." + strings.Repeat("9", 32), true, ""},
		{"65 digits across the point", "9." + strings.Repeat("9", 64), false, "more than 64 digits"},
		{"one integer digit and 63 fraction digits", "9." + strings.Repeat("9", 63), true, ""},
		{"65 digits", strings.Repeat("9", 65), false, "more than 64 digits"},
		{"a number beyond float64 range", "1" + strings.Repeat("0", 400), false, "more than 64 digits"},
		{"an exponent", `1e400`, false, "not a plain decimal"},
		{"an upper-case exponent", `1E2`, false, "not a plain decimal"},
		{"a fractional exponent", `1.5e-3`, false, "not a plain decimal"},
		{"negative zero", `-0`, false, "not a plain decimal"},
		{"negative zero with a fraction", `-0.000`, false, "not a plain decimal"},
		{"a leading zero", `01`, false, "not one valid JSON value"},
		{"an empty object", `{}`, true, ""},
		{"an empty array", `[]`, true, ""},
		{"a string", `"x"`, true, ""},
		{"null", `null`, true, ""},
		{"a literal replacement character", "\"�\"", true, ""},
		{"a surrogate pair escape", `"` + bs + `ud83d` + bs + `ude00"`, true, ""},
		{"an escaped backslash before u", `"` + bs + bs + `ud800"`, true, ""},
		{"a lone high surrogate escape", `"` + bs + `ud800"`, false, "lone UTF-16 surrogate"},
		{"a lone low surrogate escape", `"` + bs + `udc00"`, false, "lone UTF-16 surrogate"},
		{"a high surrogate before a non-surrogate escape", `"` + bs + `ud800` + bs + `u0041"`, false, "lone UTF-16 surrogate"},
		{"a lone surrogate in a key", `{"` + bs + `udfff":1}`, false, "lone UTF-16 surrogate"},
		{"invalid UTF-8 in a string", "\"a\xffb\"", false, "not valid UTF-8"},
		{"invalid UTF-8 in a key", "{\"a\xff\":1}", false, "not valid UTF-8"},
		{"a NUL escape in a string", `"a` + bs + `u0000"`, false, "NUL"},
		{"a NUL escape in a key", `{"` + bs + `u0000":1}`, false, "NUL"},
		{"nesting at the bound", nested(SemanticStateExtensionMaxDepth), true, ""},
		{"nesting one past the bound", nested(SemanticStateExtensionMaxDepth + 1), false, "nests deeper"},
		{"a repeated key", `{"a":1,"a":1}`, false, "repeats a key"},
		{"a repeated key in a nested object", `{"o":{"a":1,"a":2}}`, false, "repeats a key"},
		{"the same key in sibling objects", `[{"a":1},{"a":2}]`, true, ""},
		{"a repeated key after a nested object", `{"o":{},"o":1}`, false, "repeats a key"},
		{"an upper-case lone surrogate escape", `"` + bs + `uDC00"`, false, "lone UTF-16 surrogate"},
		{"an upper-case surrogate pair escape", `"` + bs + `uD83D` + bs + `uDE00"`, true, ""},
		{"the escape just below the surrogate range", `"` + bs + `ud7ff"`, true, ""},
		{"the escape just above the surrogate range", `"` + bs + `ue000"`, true, ""},
		{"the highest escape", `"` + bs + `uffff"`, true, ""},
		{"the lowest escape", `"` + bs + `u0001"`, true, ""},
		{"the first high surrogate", `"` + bs + `ud800"`, false, "lone UTF-16 surrogate"},
		{"the last high surrogate", `"` + bs + `udbff"`, false, "lone UTF-16 surrogate"},
		{"the first low surrogate", `"` + bs + `udc00"`, false, "lone UTF-16 surrogate"},
		{"the last low surrogate", `"` + bs + `udfff"`, false, "lone UTF-16 surrogate"},
		{"the boundary pair", `"` + bs + `udbff` + bs + `udfff"`, true, ""},
		{"the same key at two depths", `{"a":{"a":1}}`, true, ""},
		{"a key equal to a string value", `{"a":"a","b":"a"}`, true, ""},
	} {
		t.Run(cell.name, func(t *testing.T) {
			state := semanticFixture(t)
			state.Extensions = SemanticStateExtensions{"m": json.RawMessage(cell.raw)}
			_, writeErr := EncodeSemanticState(state)
			read, status := DecodeSemanticState(columnWithMember(t, fixture, []byte(cell.raw)))
			t.Logf("cell %-48s write_err=%v read=%s", cell.name, writeErr, status)
			if cell.admit {
				if writeErr != nil || status != SemanticStateReadAvailable {
					t.Fatalf("an admitted value: write_err=%v read=%s", writeErr, status)
				}
				if !semanticStateExtensionValuesEqual(read.Extensions["m"], json.RawMessage(cell.raw)) {
					t.Fatalf("the value read back is not the value written: %s", read.Extensions["m"])
				}
				return
			}
			// The rejection names the member and the reason, so a refusal
			// that another check happens to produce is not mistaken for
			// this contract's.
			if !errors.Is(writeErr, ErrSemanticStateRejected) || !strings.Contains(writeErr.Error(), cell.why) || !strings.Contains(writeErr.Error(), `extension member "m"`) {
				t.Fatalf("a refused value: write_err=%v, want the member named and %q", writeErr, cell.why)
			}
			if status != SemanticStateReadMalformed || read != nil {
				t.Fatalf("a refused value found stored read %s", status)
			}
		})
	}
}

// TestARefusalNamesABoundedMemberName: the name in a refusal is cut to the
// bound and carries no control character, even though a writer's own name is
// a constant.
func TestARefusalNamesABoundedMemberName(t *testing.T) {
	long := strings.Repeat("n", semanticStateExtensionMaxNameBytes+40)
	state := semanticFixture(t)
	state.Extensions = SemanticStateExtensions{long + "\r\ninjected": json.RawMessage(`1e2`)}
	_, err := EncodeSemanticState(state)
	if err == nil {
		t.Fatalf("the member was admitted")
	}
	if strings.Contains(err.Error(), "injected") || strings.Contains(err.Error(), "\n") || strings.Contains(err.Error(), "\r") {
		t.Fatalf("the error carries the whole name: %v", err)
	}
	if !strings.Contains(err.Error(), strings.Repeat("n", semanticStateExtensionMaxNameBytes)) {
		t.Fatalf("the error does not name the member: %v", err)
	}
}

// TestSemanticStateExtensionEquality: members are equal as JSON values.
func TestSemanticStateExtensionEquality(t *testing.T) {
	for _, cell := range []struct {
		a, b  string
		equal bool
	}{
		{`{"a":1,"b":2}`, `{"b":2,"a":1}`, true},
		{`{"a": [1, 2]}`, `{"a":[1,2]}`, true},
		{`1.50`, `1.5`, true},
		{`1`, `1.000`, true},
		{`0`, `0.0`, true},
		{`123456789012345678901234567890`, `123456789012345678901234567891`, false},
		{`"` + `\` + `u00e9"`, `"é"`, true},
		{`[1,2]`, `[2,1]`, false},
		{`1e2`, `100`, false},
		{`1`, `"1"`, false},
		{`null`, `false`, false},
		{`{"a":1}`, `{"a":1,"b":null}`, false},
		{`{}`, `[]`, false},
	} {
		if got := semanticStateExtensionValuesEqual(json.RawMessage(cell.a), json.RawMessage(cell.b)); got != cell.equal {
			t.Errorf("%s vs %s: equal=%v, want %v", cell.a, cell.b, got, cell.equal)
		}
	}
	one := SemanticStateExtensions{"m": json.RawMessage(`{"a":1}`)}
	for name, cell := range map[string]struct {
		a, b  SemanticStateExtensions
		equal bool
	}{
		"nil and empty":        {nil, SemanticStateExtensions{}, true},
		"a member and none":    {one, nil, false},
		"another member name":  {one, SemanticStateExtensions{"n": json.RawMessage(`{"a":1}`)}, false},
		"the same member":      {one, SemanticStateExtensions{"m": json.RawMessage(`{ "a" : 1.0 }`)}, true},
		"an extra member":      {one, SemanticStateExtensions{"m": json.RawMessage(`{"a":1}`), "n": json.RawMessage(`1`)}, false},
		"an unreadable member": {SemanticStateExtensions{"m": json.RawMessage(`{`)}, SemanticStateExtensions{"m": json.RawMessage(`{`)}, false},
	} {
		if got := semanticStateExtensionsEqual(cell.a, cell.b); got != cell.equal {
			t.Errorf("%s: equal=%v, want %v", name, got, cell.equal)
		}
	}
	a, b := semanticFixture(t), semanticFixture(t)
	a.Extensions = SemanticStateExtensions{"m": json.RawMessage(`{"x":1,"y":[1.0]}`)}
	b.Extensions = SemanticStateExtensions{"m": json.RawMessage(`{"y":[1],"x":1.00}`)}
	if !SemanticStatesEqual(a, b) {
		t.Fatalf("snapshots whose members differ only in rendering are not equal")
	}
	b.Extensions["m"] = json.RawMessage(`{"y":[2],"x":1}`)
	if SemanticStatesEqual(a, b) {
		t.Fatalf("snapshots whose members differ in value are equal")
	}
	b.Extensions = a.Extensions
	b.Family = QuestionFamilyUnclassified
	if SemanticStatesEqual(a, b) {
		t.Fatalf("snapshots that differ outside the members are equal")
	}
}

// TestSemanticStateExtensionsNeverDecideAContinuation: two readings that
// differ only in their members produce no continuation difference, while a
// difference in the reading itself still does.
func TestSemanticStateExtensionsNeverDecideAContinuation(t *testing.T) {
	carried, fresh := semanticFixture(t), semanticFixture(t)
	carried.Extensions = SemanticStateExtensions{"m": json.RawMessage(`{"state":"bound"}`)}
	fresh.Extensions = SemanticStateExtensions{"m": json.RawMessage(`{"state":"contested"}`), "n": json.RawMessage(`1`)}
	for field, differs := range semanticStateDifferences(carried, fresh) {
		if differs {
			t.Errorf("readings that differ only in members differ on %s", field)
		}
	}
	control := semanticFixture(t)
	control.ScopeAnchor.Kind = SubjectProject
	if !semanticStateDifferences(carried, control)[ContinuationConflictFieldScopeAnchor] {
		t.Fatalf("control: a scope anchor difference was not reported")
	}
}

// TestAStoredCensusOutsideItsDomainNeverErasesTheReading: the other raw
// member of the snapshot, the work-item census, is read under the same number
// and UTF-8 rules: a census value beyond float64 range or carrying invalid
// UTF-8 makes the census unavailable while the reading stays available.
func TestAStoredCensusOutsideItsDomainNeverErasesTheReading(t *testing.T) {
	fixture := semanticFixture(t)
	digest := strings.Repeat("ab", 32)
	valid := `{"version":"` + WorkItemTupleCensusVersion + `","state":"exact","value":1,"retained":1,"requested_repository_scope":["repo"],"authorization_digest":"` + digest + `"}`
	for _, cell := range []struct {
		name       string
		census     string
		wantCensus WorkItemTupleCensusReadStatus
	}{
		{"a valid census", valid, WorkItemTupleCensusReadAvailable},
		{"a census beyond float64 range", strings.Replace(valid, `"value":1`, `"value":1e400`, 1), WorkItemTupleCensusReadMalformed},
		{"a census with invalid UTF-8", strings.Replace(valid, `["repo"]`, "[\"re\xffpo\"]", 1), WorkItemTupleCensusReadMalformed},
		{"a census of a later version", strings.Replace(valid, WorkItemTupleCensusVersion, "work-item-census.v9", 1), WorkItemTupleCensusReadUnsupportedVersion},
	} {
		t.Run(cell.name, func(t *testing.T) {
			encoded, err := EncodeSemanticState(fixture)
			if err != nil {
				t.Fatalf("fixture defect: %v", err)
			}
			column := bytes.Replace(encoded, []byte(`{`), []byte(`{"work_item_census":`+cell.census+`,`), 1)
			state, status := DecodeSemanticState(column)
			t.Logf("cell %-32s read=%s", cell.name, status)
			if status != SemanticStateReadAvailable || state.WorkItemCensus == nil {
				t.Fatalf("read=%s census present=%v, want the reading available with its census kept", status, state != nil && state.WorkItemCensus != nil)
			}
			if got := ValidateWorkItemTupleCensus(state.WorkItemCensus); got != cell.wantCensus {
				t.Fatalf("census status = %s, want %s", got, cell.wantCensus)
			}
		})
	}
}
