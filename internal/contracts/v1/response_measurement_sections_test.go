package v1

import (
	"encoding/json"
	"sort"
	"testing"
)

// A measurement names every top-level section of the encoded document and its
// bytes, largest first, so a refusal can say what filled the answer.
func TestMeasurementSplitsTheEncodedDocumentIntoSections(t *testing.T) {
	encoded, err := json.Marshal(map[string]any{"paths": make([]int, 400), "versions": map[string]string{"a": "b"}, "limitations": []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	sections := contextFabricSectionBytes(encoded)
	if len(sections) != 3 || sections[0].Name != "paths" {
		t.Fatalf("sections = %+v, want three with paths first", sections)
	}
	if !sort.SliceIsSorted(sections, func(i, j int) bool { return sections[i].Bytes > sections[j].Bytes }) {
		t.Fatalf("sections not largest first: %+v", sections)
	}
	var total int64
	for _, section := range sections {
		total += section.Bytes
	}
	if total >= int64(len(encoded)) || total < int64(len(encoded))/2 {
		t.Fatalf("section bytes %d are not the body of the %d-byte document", total, len(encoded))
	}
	if got := contextFabricSectionBytes([]byte(`[1,2]`)); got != nil {
		t.Fatalf("a non-object encoding has no sections, got %+v", got)
	}
}

// MeasureContextFabricResponse keeps its encoding so a refusal can split it on
// demand; the split is the one the helper computes.
func TestMeasurementSectionsAreTheEncodedDocumentsOwn(t *testing.T) {
	result := ContextFabricInvestigationResult{ResultID: "result_12345678", Limitations: []string{"a limitation"}}
	measurement, err := MeasureContextFabricResponse(result)
	if err != nil {
		t.Fatal(err)
	}
	sections := measurement.Sections()
	if len(sections) == 0 {
		t.Fatal("a measured result has sections")
	}
	var total int64
	names := map[string]bool{}
	for _, section := range sections {
		total += section.Bytes
		names[section.Name] = true
	}
	if !names["result_id"] || !names["limitations"] {
		t.Fatalf("sections = %+v, want the result's own top-level keys", sections)
	}
	if total >= measurement.Bytes {
		t.Fatalf("section bytes %d must be the body of the %d-byte document", total, measurement.Bytes)
	}
	if got := (ContextFabricResponseMeasurement{}).Sections(); got != nil {
		t.Fatalf("an empty measurement has no sections, got %+v", got)
	}
}
