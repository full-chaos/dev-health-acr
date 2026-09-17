package contextfabric

import (
	"bytes"
	"encoding/json"
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
