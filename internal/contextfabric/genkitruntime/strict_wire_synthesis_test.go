package genkitruntime

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// The synthesis wire is the schema Anthropic compiles into a grammar. The
// first closed form (7539 bytes, depth 9) drew HTTP 400 "The compiled grammar
// is too large" (req_011CgCX1m6FWErtNJADGMY22, CHAOS-10311). There is no local
// grammar compiler, so these bounds are a proxy: interpret (3703 bytes) and
// phrase compile live, and the bound sits at 5.5 KB, 27% under the form that
// failed. The live acceptance call is the real proof.
const synthesisWireByteBound = 5500

func TestSynthesisStrictWireStaysUnderTheProxyBound(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	raw, err := json.Marshal(schema.strict)
	if err != nil {
		t.Fatal(err)
	}
	var counts wireCounts
	countWire(schema.strict, 0, &counts)
	t.Logf("synthesize strict wire: %d bytes optionals=%d unions=%d depth=%d", len(raw), counts.optionals, counts.unions, counts.depth)
	if len(raw) > synthesisWireByteBound {
		t.Errorf("synthesis strict wire is %d bytes, bound %d", len(raw), synthesisWireByteBound)
	}
	if counts.optionals != 0 || counts.unions != 0 {
		t.Errorf("synthesis strict wire has %d optional and %d union properties, want 0 and 0", counts.optionals, counts.unions)
	}
	if counts.depth > 8 {
		t.Errorf("synthesis strict wire depth %d, bound 8", counts.depth)
	}
}

func TestSynthesisFoldTagsAreClosedEnums(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	properties := schema.strict["properties"].(map[string]any)
	claim := properties["claimed_facts"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	cases := []struct {
		name  string
		props map[string]any
		tag   string
		want  []any
	}{
		{"findings", properties, "list", []any{"remaining_work", "readiness_gaps", "conflicts"}},
		{"rows", claim, "series", []any{"table", "time_series"}},
		{"tables", claim, "series", []any{"table", "time_series"}},
	}
	for _, c := range cases {
		node, ok := c.props[c.name].(map[string]any)
		if !ok {
			t.Fatalf("wire has no folded %s", c.name)
		}
		item := node["items"].(map[string]any)
		tag := item["properties"].(map[string]any)[c.tag].(map[string]any)
		if !reflect.DeepEqual(tag["enum"], c.want) || tag["type"] != "string" {
			t.Errorf("%s.%s = %v, want a closed enum %v", c.name, c.tag, tag, c.want)
		}
		if !reflect.DeepEqual(item["required"].([]any), sortedKeys(item["properties"].(map[string]any))) {
			t.Errorf("%s: tag or member not required", c.name)
		}
	}
	for _, gone := range []string{"remaining_work", "readiness_gaps", "conflicts"} {
		if _, ok := properties[gone]; ok {
			t.Errorf("wire still carries %s beside findings", gone)
		}
	}
	for _, gone := range []string{"time_series_rows", "table", "time_series_table"} {
		if _, ok := claim[gone]; ok {
			t.Errorf("wire still carries claim.%s beside the folded lists", gone)
		}
	}
}

func sortedKeys(m map[string]any) []any {
	out := make([]any, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].(string) < out[i].(string) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func normalized(t *testing.T, text string) any {
	t.Helper()
	var out any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// recorded is a producer-shaped synthesis reply: the served fields of the
// real investigation result in contracts/examples/v1/
// context_fabric_investigation_result_render_shapes.v1.json (six claimed facts,
// legacy and time_series tables, a dual-table claim, scalar kinds, drivers,
// one remaining_work finding). The .wire.json file is the same reply in the
// shape a model sends under the strict schema.
func TestRecordedSynthesisReplyRoundTripsThroughTheStrictWire(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	wire := readFixture(t, "synthesis_reply_recorded.wire.json")
	original := readFixture(t, "synthesis_reply_recorded.json")

	strict, err := gojsonschema.NewSchema(gojsonschema.NewGoLoader(schema.strict))
	if err != nil {
		t.Fatal(err)
	}
	if res, err := strict.Validate(gojsonschema.NewStringLoader(wire)); err != nil || !res.Valid() {
		t.Fatalf("the wire fixture is not a reply the strict schema allows: %v %v", err, res.Errors())
	}

	restored := restoreStrictReply(schema.wire, wire)
	if !reflect.DeepEqual(normalized(t, restored), normalized(t, original)) {
		t.Fatalf("restored reply differs from the recorded reply\nrestored: %s\nrecorded: %s", restored, original)
	}

	var fromWire, fromOriginal synthesisOutput
	decodeRestored(t, schema, wire, &fromWire)
	if err := json.Unmarshal([]byte(original), &fromOriginal); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromWire, fromOriginal) {
		t.Fatalf("decoded wire reply differs from the decoded recorded reply")
	}
	rows, series, tables, seriesTables := 0, 0, 0, 0
	for _, claim := range fromWire.ClaimedFacts {
		rows += len(claim.Rows)
		series += len(claim.TimeSeriesRows)
		if claim.Table != nil {
			tables++
		}
		if claim.TimeSeriesTable != nil {
			seriesTables++
		}
	}
	if len(fromWire.ClaimedFacts) != 6 || rows == 0 || series == 0 || tables == 0 || seriesTables == 0 || len(fromWire.RemainingWork) == 0 || len(fromWire.Drivers) == 0 {
		t.Fatalf("recorded reply lost content: claims=%d rows=%d time_series_rows=%d tables=%d time_series_tables=%d remaining_work=%d drivers=%d",
			len(fromWire.ClaimedFacts), rows, series, tables, seriesTables, len(fromWire.RemainingWork), len(fromWire.Drivers))
	}
}

// A finding goes back to the list its tag names, in item order, and the same
// holds for rows and tables: nothing moves to another list, nothing is lost.
func TestFoldedListsRestoreToTheirOwnProperty(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	var reply map[string]any
	if err := json.Unmarshal([]byte(readFixture(t, "synthesis_reply_recorded.wire.json")), &reply); err != nil {
		t.Fatal(err)
	}
	finding := func(id, list string) map[string]any {
		return map[string]any{"finding_id": id, "kind": "narrative", "summary": "s " + id, "subjects": []any{}, "claimed_fact_ids": []any{}, "evidence_ref_ids": []any{}, "list": list}
	}
	reply["findings"] = []any{
		finding("f_c1", "conflicts"), finding("f_r1", "remaining_work"), finding("f_g1", "readiness_gaps"),
		finding("f_c2", "conflicts"), finding("f_r2", "remaining_work"),
	}
	raw, _ := json.Marshal(reply)
	var got synthesisOutput
	decodeRestored(t, schema, string(raw), &got)
	ids := func(list any) []string {
		var out []string
		raw, _ := json.Marshal(list)
		var items []struct {
			ID string `json:"finding_id"`
		}
		_ = json.Unmarshal(raw, &items)
		for _, item := range items {
			out = append(out, item.ID)
		}
		return out
	}
	for name, want := range map[string][]string{"conflicts": {"f_c1", "f_c2"}, "remaining_work": {"f_r1", "f_r2"}, "readiness_gaps": {"f_g1"}} {
		var list any
		switch name {
		case "conflicts":
			list = got.Conflicts
		case "remaining_work":
			list = got.RemainingWork
		default:
			list = got.ReadinessGaps
		}
		if !reflect.DeepEqual(ids(list), want) {
			t.Errorf("%s = %v, want %v", name, ids(list), want)
		}
	}
}

func TestFoldedListsWithAnUnknownTagStayForTheLocalCheck(t *testing.T) {
	schema := strictSchemas(t)["synthesize"]
	var reply map[string]any
	if err := json.Unmarshal([]byte(readFixture(t, "synthesis_reply_recorded.wire.json")), &reply); err != nil {
		t.Fatal(err)
	}
	reply["findings"] = []any{map[string]any{"finding_id": "f", "kind": "k", "summary": "s", "subjects": []any{}, "claimed_fact_ids": []any{}, "evidence_ref_ids": []any{}, "list": "limitations"}}
	raw, _ := json.Marshal(reply)
	if got := restoreStrictReply(schema.wire, string(raw)); got != string(raw) {
		t.Errorf("a reply with an unknown list tag was rewritten:\n%s", got)
	}
}
