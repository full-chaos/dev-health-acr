package v1

import (
	"testing"
	"time"
)

func TestCapabilitiesValidate_matches_v1_boundaries(t *testing.T) {
	base := loadFixture[Capabilities](t, "capabilities.v1.json")
	if err := base.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "capabilities.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*Capabilities)
	}{
		{name: "schema_version", mutate: func(v *Capabilities) { v.SchemaVersion = "wrong" }},
		{name: "service_wrong", mutate: func(v *Capabilities) { v.Service = "other-service" }},
		{name: "service_version_empty", mutate: func(v *Capabilities) { v.ServiceVersion = "" }},
		{name: "minimum_sidecar_version_empty", mutate: func(v *Capabilities) { v.MinimumSidecarVersion = "" }},
		{name: "supported_schema_versions_nil", mutate: func(v *Capabilities) { v.SupportedSchemaVersions = nil }},
		{name: "supported_schema_versions_duplicate", mutate: func(v *Capabilities) {
			v.SupportedSchemaVersions = []string{"a", "a"}
		}},
		{name: "enabled_tools_nil", mutate: func(v *Capabilities) { v.EnabledTools = nil }},
		{name: "enabled_tools_unsupported", mutate: func(v *Capabilities) { v.EnabledTools = []string{"unknown_tool"} }},
		{name: "enabled_tools_duplicate", mutate: func(v *Capabilities) {
			v.EnabledTools = []string{"context_for_task", "context_for_task"}
		}},
		{name: "limits_max_items_zero", mutate: func(v *Capabilities) { v.Limits.MaxItems = 0 }},
		{name: "limits_max_output_tokens_zero", mutate: func(v *Capabilities) { v.Limits.MaxOutputTokens = 0 }},
		{name: "limits_max_serialized_bytes_zero", mutate: func(v *Capabilities) { v.Limits.MaxSerializedBytes = 0 }},
		{name: "limits_requests_per_minute_zero", mutate: func(v *Capabilities) { v.Limits.RequestsPerMinute = 0 }},
		{name: "generated_at_zero", mutate: func(v *Capabilities) { v.GeneratedAt = time.Time{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := loadFixture[Capabilities](t, "capabilities.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid capabilities")
			}
		})
	}
}

func TestCapabilitiesValidateRejectsOtherwiseEmptyValue(t *testing.T) {
	empty := Capabilities{SchemaVersion: CapabilitiesSchema}
	if err := empty.Validate(); err == nil {
		t.Fatal("validator accepted an otherwise-empty capabilities value")
	}
}
