package v1

import "testing"

// CHAOS-7072 (S1a): the hosted API advertises data_catalog, find_subjects
// and run_operation (design CHAOS-7036 E.5). enabled_tools is a CLOSED set
// validated on acr-mcp's startup path, so each new name must be accepted by
// the Go validator AND the published schema, or an entitled deployment with
// the data tools composed refuses to boot the sidecar.
func TestCapabilitiesAcceptsTheDirectDataToolNames(t *testing.T) {
	names := []string{"data_catalog", "find_subjects", "run_operation"}
	for _, name := range names {
		if !validEnabledTool(name) {
			t.Errorf("enabled_tools rejects %q, which the hosted API can advertise", name)
		}
	}
	capabilities := loadFixture[Capabilities](t, "capabilities.v1.json")
	capabilities.EnabledTools = append([]string{"context_for_task", "source_evidence"}, names...)
	if err := capabilities.Validate(); err != nil {
		t.Fatalf("capabilities advertising the direct data tools failed validation: %v", err)
	}
	assertSchemaParity(t, "capabilities.v1.schema.json", capabilities)
}
