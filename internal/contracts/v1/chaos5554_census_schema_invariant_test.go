package v1

import (
	"encoding/json"
	"testing"

	"github.com/xeipuuv/gojsonschema"
)

// chaos5554CensusRecordBase is a FactScopeCensusRecord instance every
// property of which is present and legal, independent of the
// population_measured / authorized_population_count invariant under test --
// so every case below is valid or invalid only because of the two fields it
// overrides, never because some OTHER field happened to be missing or
// malformed.
func chaos5554CensusRecordBase() map[string]any {
	return map[string]any{
		"requirement_kind":            "fact_status",
		"origin_kind":                 "project",
		"policy":                      "project_work_item_status_v1",
		"basis":                       "direct",
		"axis":                        "current",
		"outcome":                     "expanded",
		"target_limit":                200,
		"population_measured":         true,
		"authorized_population_count": 3,
		"admitted_count":              3,
		"truncated":                   false,
	}
}

// TestChaos5554_SchemaEnforcesTheCensusMeasurementInvariant is the
// SCHEMA-LEVEL pin CHAOS-5554 asks for.
//
// TestChaos5405_TheCensusCannotContradictItselfAboutMeasurement (this
// package) already proves the Go struct validator refuses a census row that
// contradicts itself about measurement. What CHAOS-5554 found is that
// nothing validated the PUBLISHED SCHEMA the same way: a schema-only
// consumer -- an offline MCP client, or ask-dev's own synced copy before its
// Go validator ever runs -- accepted `population_measured: false` beside a
// count, or `true` beside a null count, because the schema declared only
// each field's own type, never the agreement between them. This test builds
// real JSON bytes and validates them against the PUBLISHED schema
// (gojsonschema, the same library and pattern as
// TestCHAOS4809ProjectedBasisValidatesAgainstThePublishedSchema and
// TestOutputTimeContextSchemaEnforcesPerAxisShape), not the Go struct.
//
// The invariant, from authorized_population_count's own schema description
// and PR #490's body ("writes the authorized population only when the
// traversal says the census completed, so an unmeasured population
// serializes null and a measured empty one serializes 0"):
//
//	population_measured == true  <=> authorized_population_count is a
//	                                 non-null integer (0 legal)
//	population_measured == false <=> authorized_population_count is null
//	                                 (never 0, never any integer)
//
// Runs the SAME table against BOTH hand-maintained copies of
// FactScopeCensusRecord: context_fabric_common.v1's canonical $defs entry,
// and context_fabric_answer_projection.v1's own local duplicate, which by
// design (PR #490 RISK-NOTES) carries no cross-file $ref at all. The two are
// independently maintained -- neither is generated from the other -- so
// both must independently enforce the same rule, and a fix applied to only
// one is exactly the class-sweep gap this table is here to catch.
func TestChaos5554_SchemaEnforcesTheCensusMeasurementInvariant(t *testing.T) {
	documents := schemaDocuments(t)
	subschemas := map[string]map[string]any{
		"common.v1 (canonical)":             schemaNodeAt(t, documents, "common#$defs.FactScopeCensusRecord"),
		"answer_projection.v1 (local copy)": schemaNodeAt(t, documents, "answer#$defs.FactScopeCensusRecord"),
	}

	cases := []struct {
		name      string
		mutate    func(map[string]any)
		wantValid bool
	}{
		// -- population_measured=true (the "then" branch): count must be
		// a non-null integer; 0 is the canonical measured-zero boundary
		// this record exists to distinguish from "not measured". --
		{
			name:      "measured=true, count=3 (canonical, positive)",
			mutate:    func(r map[string]any) {},
			wantValid: true,
		},
		{
			name:      "measured=true, count=0 (canonical, measured zero)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = 0 },
			wantValid: true,
		},
		{
			name:      "measured=true, count=null (CONTRADICTION: a completed census reporting no measurement)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = nil },
			wantValid: false,
		},
		{
			name:      "measured=true, count absent (base object's own required, not this invariant)",
			mutate:    func(r map[string]any) { delete(r, "authorized_population_count") },
			wantValid: false,
		},
		{
			name:      "measured=true, count=\"3\" (wrong scalar type)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = "3" },
			wantValid: false,
		},
		{
			name:      "measured=true, count=[] (wrong container type)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = []any{} },
			wantValid: false,
		},
		{
			name:      "measured=true, count=2.5 (fractional where integral)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = 2.5 },
			wantValid: false,
		},
		{
			name:      "measured=true, count=-1 (boundary-1, pre-existing minimum:0)",
			mutate:    func(r map[string]any) { r["authorized_population_count"] = -1 },
			wantValid: false,
		},

		// -- population_measured=false (the "else" branch): count must be
		// null; any integer, including 0, contradicts the census not
		// having completed. --
		{
			name: "measured=false, count=null (canonical, unmeasured)",
			mutate: func(r map[string]any) {
				r["population_measured"] = false
				r["authorized_population_count"] = nil
			},
			wantValid: true,
		},
		{
			name: "measured=false, count=0 (CONTRADICTION: same class as above, zero end)",
			mutate: func(r map[string]any) {
				r["population_measured"] = false
				r["authorized_population_count"] = 0
			},
			wantValid: false,
		},
		{
			name: "measured=false, count=5 (CONTRADICTION: same class, non-zero)",
			mutate: func(r map[string]any) {
				r["population_measured"] = false
				r["authorized_population_count"] = 5
			},
			wantValid: false,
		},
		{
			name: "measured=false, count absent (base object's own required, not this invariant)",
			mutate: func(r map[string]any) {
				r["population_measured"] = false
				delete(r, "authorized_population_count")
			},
			wantValid: false,
		},

		// -- population_measured's own domain: absent/wrong-type falls
		// through to the "else" branch (the "if" itself fails to match),
		// so the base object's OWN required/type check is what actually
		// rejects these -- pinned here so a future change to this if/then
		// cannot silently make either of these legal by accident. --
		{
			name:      "population_measured absent (base object's own required)",
			mutate:    func(r map[string]any) { delete(r, "population_measured") },
			wantValid: false,
		},
		{
			name:      "population_measured=\"true\" (wrong scalar type)",
			mutate:    func(r map[string]any) { r["population_measured"] = "true" },
			wantValid: false,
		},
	}

	for subName, subschema := range subschemas {
		subschema := subschema
		t.Run(subName, func(t *testing.T) {
			loader := gojsonschema.NewGoLoader(subschema)
			for _, tc := range cases {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					record := chaos5554CensusRecordBase()
					tc.mutate(record)
					encoded, err := json.Marshal(record)
					if err != nil {
						t.Fatalf("marshal instance: %v", err)
					}
					result, err := gojsonschema.Validate(loader, gojsonschema.NewBytesLoader(encoded))
					if err != nil {
						t.Fatalf("validate %s: %v", encoded, err)
					}
					if result.Valid() != tc.wantValid {
						t.Errorf("instance %s: valid=%v (want %v), errors=%v", encoded, result.Valid(), tc.wantValid, result.Errors())
					}
				})
			}
		})
	}
}
