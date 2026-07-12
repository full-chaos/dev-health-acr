package v1

import "fmt"

// Validate enforces context_packet.v1.schema.json exactly, including every
// nested object: status/resolution enums, string and numeric bounds,
// required-list presence (a nil slice marshals to JSON null, which the
// schema's "type": "array" rejects), and per-item item/check/step
// validation. This is the canonical top-level validator MCP response
// wrapping (mcp_validate.go) delegates to, so a structurally-empty
// ContextPacket can never pass validation just because only its
// schema_version was inspected.
func (p ContextPacket) Validate() error {
	if p.SchemaVersion != ContextPacketSchema {
		return fmt.Errorf("schema_version must be %q", ContextPacketSchema)
	}
	if !stringLengthBetween(p.ContextPacketID, 8, 256) || !stringLengthBetween(p.RequestID, 8, 256) {
		return fmt.Errorf("context_packet_id or request_id violates v1 bounds")
	}
	if p.GeneratedAt.IsZero() {
		return fmt.Errorf("generated_at is required")
	}
	if !validPacketStatus(p.Status) {
		return fmt.Errorf("status violates v1 bounds")
	}
	if !stringLengthBetween(p.Goal, 1, 4000) {
		return fmt.Errorf("goal violates v1 bounds")
	}
	if err := p.Repository.Validate(); err != nil {
		return fmt.Errorf("repository: %w", err)
	}
	if err := p.RequestedScope.Validate(); err != nil {
		return fmt.Errorf("requested_scope: %w", err)
	}
	if err := p.ResolvedScope.Validate(); err != nil {
		return fmt.Errorf("resolved_scope: %w", err)
	}
	if !stringLengthBetween(p.QueryVersion, 1, 200) || !stringLengthBetween(p.RankingVersion, 1, 200) {
		return fmt.Errorf("query_version or ranking_version violates v1 bounds")
	}
	if !stringLengthBetween(p.Summary, 0, 4000) {
		return fmt.Errorf("summary violates v1 bounds")
	}
	if err := validatePacketItems(p.Items); err != nil {
		return err
	}
	if err := validateRequiredChecks(p.RequiredChecks); err != nil {
		return err
	}
	if err := validateRecommendedSteps(p.RecommendedNextSteps); err != nil {
		return err
	}
	if err := p.Freshness.Validate(); err != nil {
		return fmt.Errorf("freshness: %w", err)
	}
	if err := p.Coverage.Validate(); err != nil {
		return fmt.Errorf("coverage: %w", err)
	}
	if err := p.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if err := validatePacketWarnings(p.Warnings); err != nil {
		return err
	}
	if err := p.Compatibility.Validate(); err != nil {
		return fmt.Errorf("compatibility: %w", err)
	}
	if !stringLengthBetween(p.RetrievalDebugSummary, 0, 8000) {
		return fmt.Errorf("retrieval_debug_summary violates v1 bounds")
	}
	return nil
}

func validPacketStatus(status PacketStatus) bool {
	switch status {
	case PacketComplete, PacketPartial, PacketDegraded, PacketEmpty:
		return true
	default:
		return false
	}
}

func validatePacketItems(items []ContextPacketItem) error {
	if items == nil || len(items) > 50 {
		return fmt.Errorf("items violates v1 bounds")
	}
	for i, item := range items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("items[%d]: %w", i, err)
		}
	}
	return nil
}

func validatePacketWarnings(warnings []string) error {
	if warnings == nil {
		return fmt.Errorf("warnings is required")
	}
	for _, warning := range warnings {
		if !stringLengthBetween(warning, 0, 2000) {
			return fmt.Errorf("warnings violates v1 bounds")
		}
	}
	return nil
}
