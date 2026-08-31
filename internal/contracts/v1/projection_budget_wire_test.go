package v1

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// This file is a PRE-CHANGE record of the projection budget's wire payload,
// taken while five of its counters are optional in the schema but always
// emitted by Go: limitations_omitted, warnings_omitted, coverage_omitted,
// reasons_omitted and values_clamped.
//
// That combination is stable but fragile, and it is fragile in a direction
// that is invisible in review. Adding `omitempty` to any of them -- the
// obvious tidy-up once a field is "optional anyway" -- would silently stop
// emitting it at zero. Every consumer would keep decoding 0, every test
// asserting a count of 0 would keep passing, and the wire payload would
// have changed for every answer that dropped nothing: the common case.
//
// The budget is the projection's honesty record. "This field is absent"
// and "this field is zero" must not become the same statement by accident,
// so the payload is pinned before the next change rather than after it.

// projectionBudgetGolden is the exact JSON a fully-zero budget serializes
// to today. It is a fixture, not an assertion about what the numbers SHOULD
// be -- every counter is zero precisely so the only thing under test is
// which KEYS reach the wire.
const projectionBudgetGolden = `{"truncated":false,"drivers_omitted":0,"withheld_drivers_omitted":0,"cohort_members_omitted":0,"cohort_groups_omitted":0,"facts_omitted":0,"candidates_omitted":0,"evidence_refs_omitted":0,"limitations_omitted":0,"warnings_omitted":0,"coverage_omitted":0,"reasons_omitted":0,"values_clamped":0,"render_shapes_omitted":0,"full_result_omitted":false}`

// optionalBudgetCounters are the ones the schema does not require. They are
// listed here so the test fails by NAME if one stops being emitted.
//
// render_shapes_omitted (CHAOS-4415) joined them deliberately: it follows
// the same rule reasons_omitted and values_clamped already do -- a counter
// added after the contract shipped is optional in the schema (so a document
// written before it existed still validates) and NON-omitempty in Go (so
// zero and absent never become the same statement for anything this service
// writes).
var optionalBudgetCounters = []string{
	"limitations_omitted",
	"warnings_omitted",
	"coverage_omitted",
	"reasons_omitted",
	"values_clamped",
	"render_shapes_omitted",
}

func TestProjectionBudgetWirePayloadIsPinned(t *testing.T) {
	encoded, err := json.Marshal(ContextFabricProjectionBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != projectionBudgetGolden {
		t.Errorf("the zero projection budget no longer serializes to its pinned payload.\nIf a field was added or renamed this is expected -- update the fixture deliberately.\nIf a counter DISAPPEARED, an `omitempty` has made zero indistinguishable from absent on the wire.\n  pinned:   %s\n  produced: %s",
			projectionBudgetGolden, encoded)
	}
}

// TestOptionalBudgetCountersAreStillEmittedAtZero states the property the
// golden above protects, in the terms a future change would violate.
func TestOptionalBudgetCountersAreStillEmittedAtZero(t *testing.T) {
	encoded, err := json.Marshal(ContextFabricProjectionBudget{})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	for _, counter := range optionalBudgetCounters {
		if _, present := payload[counter]; !present {
			t.Errorf("%q is not emitted when it is zero, so a consumer cannot tell 'nothing was dropped' from 'this build does not report it'", counter)
		}
	}
}

// TestOptionalBudgetCountersRoundTripWhenAbsent is the read half: a stored
// or third-party document that omits the five optional counters must decode,
// validate, and re-encode to the same payload a zero budget produces.
//
// This is the lenient-read direction. The schema permits their absence, so
// the service must accept it rather than rejecting a document its own
// contract calls valid.
func TestOptionalBudgetCountersRoundTripWhenAbsent(t *testing.T) {
	pruned := map[string]any{}
	if err := json.Unmarshal([]byte(projectionBudgetGolden), &pruned); err != nil {
		t.Fatal(err)
	}
	for _, counter := range optionalBudgetCounters {
		delete(pruned, counter)
	}
	trimmed, err := json.Marshal(pruned)
	if err != nil {
		t.Fatal(err)
	}

	var decoded ContextFabricProjectionBudget
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		t.Fatalf("a document omitting the optional counters failed to decode: %v", err)
	}
	if !reflect.DeepEqual(decoded, ContextFabricProjectionBudget{}) {
		t.Errorf("omitting the optional counters did not decode to the zero budget: %+v", decoded)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) != projectionBudgetGolden {
		t.Errorf("re-encoding a document that omitted the optional counters did not restore the canonical payload:\n  want: %s\n  got:  %s", projectionBudgetGolden, reencoded)
	}
}

// TestOptionalBudgetCountersMatchTheSchema derives the optional set from the
// published schema instead of trusting the list above, so a counter that
// gains or loses `required` cannot leave this file pinning a stale claim.
func TestOptionalBudgetCountersMatchTheSchema(t *testing.T) {
	documents := schemaDocuments(t)
	node := schemaNodeAt(t, documents, "answer#$defs.ProjectionBudget")

	properties, ok := node["properties"].(map[string]any)
	if !ok {
		t.Fatal("ProjectionBudget declares no properties")
	}
	required := map[string]struct{}{}
	if raw, ok := node["required"].([]any); ok {
		for _, value := range raw {
			if name, ok := value.(string); ok {
				required[name] = struct{}{}
			}
		}
	}
	optional := make([]string, 0, len(properties))
	for name := range properties {
		if _, isRequired := required[name]; !isRequired {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	want := append([]string(nil), optionalBudgetCounters...)
	sort.Strings(want)
	if !reflect.DeepEqual(optional, want) {
		t.Errorf("the schema's optional ProjectionBudget fields have changed:\n  schema: %v\n  pinned: %v\nUpdate optionalBudgetCounters and the wire fixture together, deliberately.",
			optional, want)
	}

	// Every property the schema declares must also reach the wire, or the
	// contract is describing a field the service never emits.
	encoded, err := json.Marshal(ContextFabricProjectionBudget{})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	missing := make([]string, 0, len(properties))
	for name := range properties {
		if _, present := payload[name]; !present {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the schema declares ProjectionBudget fields the zero payload never emits: %s", strings.Join(missing, ", "))
	}
}
