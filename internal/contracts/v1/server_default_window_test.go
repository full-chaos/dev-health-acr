package v1

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/xeipuuv/gojsonschema"
)

// TestServerDefaultWindowFormsOnTheWire (CHAOS-11601): the Go validator and
// the published result schema agree on the effective window's three forms.
// as_of is legal only with no start, end or relative_id; server_default is a
// provenance the write path accepts.
func TestServerDefaultWindowFormsOnTheWire(t *testing.T) {
	asOf := time.Date(2026, 10, 30, 18, 4, 5, 0, time.UTC)
	start, end := asOf.Add(-90*24*time.Hour), asOf
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	loader := gojsonschema.NewSchemaLoader()
	if err := loader.AddSchemas(gojsonschema.NewReferenceLoader("file://" + filepath.Join(root, "contracts/jsonschema/v1/context_fabric_common.v1.schema.json"))); err != nil {
		t.Fatalf("add common schema: %v", err)
	}
	schema, err := loader.Compile(gojsonschema.NewReferenceLoader("file://" + filepath.Join(root, "contracts/jsonschema/v1/context_fabric_investigation_result.v1.schema.json")))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for _, tc := range []struct {
		name   string
		window ContextFabricEffectiveEvidenceWindow
		valid  bool
	}{
		{"snapshot", ContextFabricEffectiveEvidenceWindow{AsOf: &asOf, WindowClass: ContextFabricWindowClassStateSnapshot, Provenance: ContextFabricWindowServerDefault}, true},
		{"default trailing", ContextFabricEffectiveEvidenceWindow{Start: &start, End: &end, RelativeID: ContextFabricRelativeWindowTrailing90D, Provenance: ContextFabricWindowServerDefault}, true},
		{"as_of with start and end", ContextFabricEffectiveEvidenceWindow{AsOf: &asOf, Start: &start, End: &end, Provenance: ContextFabricWindowServerDefault}, false},
		{"as_of with relative_id", ContextFabricEffectiveEvidenceWindow{AsOf: &asOf, RelativeID: ContextFabricRelativeWindowAllTime, Provenance: ContextFabricWindowServerDefault}, false},
		{"neither form", ContextFabricEffectiveEvidenceWindow{WindowClass: ContextFabricWindowClassStateSnapshot, Provenance: ContextFabricWindowServerDefault}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := resultWithValueAt(t, resultV1, "")
			window := tc.window
			r.EffectiveEvidenceWindow = &window
			if err := r.Validate(); (err == nil) != tc.valid {
				t.Errorf("Validate() = %v, want valid=%v", err, tc.valid)
			}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			report, err := schema.Validate(gojsonschema.NewBytesLoader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			if report.Valid() != tc.valid {
				t.Errorf("schema valid = %v, want %v: %v", report.Valid(), tc.valid, report.Errors())
			}
		})
	}
}
