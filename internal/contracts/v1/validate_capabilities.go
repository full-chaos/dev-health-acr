package v1

import "fmt"

const capabilitiesService = "dev-health-acr"

// Validate enforces capabilities.v1.schema.json exactly: the service name
// is a fixed const, every list field must be present (a nil slice
// marshals to JSON null, which the schema's "type": "array" rejects) and
// unique where required, enabled_tools entries must hold a recognized
// tool name, and every numeric limit must meet its schema minimum.
func (c Capabilities) Validate() error {
	if c.SchemaVersion != CapabilitiesSchema {
		return fmt.Errorf("schema_version must be %q", CapabilitiesSchema)
	}
	if c.Service != capabilitiesService {
		return fmt.Errorf("service must be %q", capabilitiesService)
	}
	if !stringLengthBetween(c.ServiceVersion, 1, 200) || !stringLengthBetween(c.MinimumSidecarVersion, 1, 200) {
		return fmt.Errorf("capabilities version fields violate v1 bounds")
	}
	if len(c.SupportedSchemaVersions) < 1 || !uniqueStrings(c.SupportedSchemaVersions) {
		return fmt.Errorf("supported_schema_versions violates v1 bounds")
	}
	if c.EnabledTools == nil || !uniqueStrings(c.EnabledTools) {
		return fmt.Errorf("enabled_tools violates v1 bounds")
	}
	for _, tool := range c.EnabledTools {
		if !validEnabledTool(tool) {
			return fmt.Errorf("enabled_tools contains unsupported tool %q", tool)
		}
	}
	if err := c.Limits.Validate(); err != nil {
		return fmt.Errorf("limits: %w", err)
	}
	if c.GeneratedAt.IsZero() {
		return fmt.Errorf("generated_at is required")
	}
	return nil
}

// Validate enforces capabilities.v1.schema.json's "limits" object: every
// field has a schema minimum of 1 and no schema-defined maximum.
func (l CapabilityLimits) Validate() error {
	if l.MaxItems < 1 || l.MaxOutputTokens < 1 || l.MaxSerializedBytes < 1 || l.RequestsPerMinute < 1 {
		return fmt.Errorf("capability limits violate v1 bounds")
	}
	return nil
}

func validEnabledTool(name string) bool {
	switch name {
	// CHAOS-3746 added investigate_question and investigation_result. This
	// set is CLOSED and is validated by acr-mcp on the startup path
	// (sidecar.validateCapabilities), so a name the hosted API advertises
	// but this set omits does not degrade -- it refuses to boot the
	// sidecar entirely.
	case "context_for_task", "source_evidence", "investigate_question", "investigation_result", "record_episode":
		return true
	default:
		return false
	}
}
