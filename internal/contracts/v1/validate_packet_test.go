package v1

import (
	"strings"
	"testing"
	"time"
)

func TestContextPacketValidate_matches_v1_boundaries(t *testing.T) {
	base := loadFixture[ContextPacket](t, "context_packet.v1.json")
	if err := base.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "context_packet.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*ContextPacket)
	}{
		{name: "schema_version", mutate: func(v *ContextPacket) { v.SchemaVersion = "wrong" }},
		{name: "context_packet_id_too_short", mutate: func(v *ContextPacket) { v.ContextPacketID = "short" }},
		{name: "request_id_too_short", mutate: func(v *ContextPacket) { v.RequestID = "short" }},
		{name: "generated_at_zero", mutate: func(v *ContextPacket) { v.GeneratedAt = time.Time{} }},
		{name: "status_invalid", mutate: func(v *ContextPacket) { v.Status = "unknown" }},
		{name: "goal_empty", mutate: func(v *ContextPacket) { v.Goal = "" }},
		{name: "repository_slug_invalid", mutate: func(v *ContextPacket) { v.Repository.Slug = "invalid-slug" }},
		{name: "requested_scope_commit_invalid", mutate: func(v *ContextPacket) { v.RequestedScope.CommitSHA = "bad" }},
		{name: "resolved_scope_repo_id_empty", mutate: func(v *ContextPacket) { v.ResolvedScope.RepoID = "" }},
		{name: "resolved_scope_repo_slug_invalid", mutate: func(v *ContextPacket) { v.ResolvedScope.RepoSlug = "invalid-slug" }},
		{name: "resolved_scope_resolution_invalid", mutate: func(v *ContextPacket) { v.ResolvedScope.Resolution = "guessed" }},
		{name: "resolved_scope_fallback_reasons_nil", mutate: func(v *ContextPacket) { v.ResolvedScope.FallbackReasons = nil }},
		{name: "query_version_empty", mutate: func(v *ContextPacket) { v.QueryVersion = "" }},
		{name: "ranking_version_empty", mutate: func(v *ContextPacket) { v.RankingVersion = "" }},
		{name: "summary_too_long", mutate: func(v *ContextPacket) { v.Summary = strings.Repeat("s", 4001) }},
		{name: "items_nil", mutate: func(v *ContextPacket) { v.Items = nil }},
		{name: "items_too_many", mutate: func(v *ContextPacket) { v.Items = make([]ContextPacketItem, 51) }},
		{name: "items_nested_invalid", mutate: func(v *ContextPacket) { v.Items[0].Title = "" }},
		{name: "required_checks_nil", mutate: func(v *ContextPacket) { v.RequiredChecks = nil }},
		{name: "required_checks_nested_invalid", mutate: func(v *ContextPacket) { v.RequiredChecks[0].CheckID = "" }},
		{name: "recommended_next_steps_nil", mutate: func(v *ContextPacket) { v.RecommendedNextSteps = nil }},
		{name: "recommended_next_steps_nested_invalid", mutate: func(v *ContextPacket) { v.RecommendedNextSteps[0].Label = "" }},
		{name: "freshness_as_of_zero", mutate: func(v *ContextPacket) { v.Freshness.AsOf = time.Time{} }},
		{name: "freshness_stale_after_seconds_negative", mutate: func(v *ContextPacket) { v.Freshness.StaleAfterSeconds = -1 }},
		{name: "freshness_watermarks_nil", mutate: func(v *ContextPacket) { v.Freshness.Watermarks = nil }},
		{name: "freshness_watermarks_status_invalid", mutate: func(v *ContextPacket) { v.Freshness.Watermarks[0].Status = "unknown" }},
		{name: "coverage_sources_considered_nil", mutate: func(v *ContextPacket) { v.Coverage.SourcesConsidered = nil }},
		{name: "coverage_sources_available_nil", mutate: func(v *ContextPacket) { v.Coverage.SourcesAvailable = nil }},
		{name: "coverage_sources_unavailable_nil", mutate: func(v *ContextPacket) { v.Coverage.SourcesUnavailable = nil }},
		{name: "coverage_degraded_reasons_nil", mutate: func(v *ContextPacket) { v.Coverage.DegradedReasons = nil }},
		{name: "budget_max_items_zero", mutate: func(v *ContextPacket) { v.Budget.MaxItems = 0 }},
		{name: "budget_items_used_negative", mutate: func(v *ContextPacket) { v.Budget.ItemsUsed = -1 }},
		{name: "warnings_nil", mutate: func(v *ContextPacket) { v.Warnings = nil }},
		{name: "compatibility_service_version_empty", mutate: func(v *ContextPacket) { v.Compatibility.ServiceVersion = "" }},
		{name: "compatibility_supported_schema_versions_nil", mutate: func(v *ContextPacket) {
			v.Compatibility.SupportedSchemaVersions = nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Reload fresh for every case: Items/RequiredChecks/etc. are
			// slices, and mutating a shared copy's element fields would
			// also corrupt the base fixture's backing array.
			value := loadFixture[ContextPacket](t, "context_packet.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid context_packet")
			}
		})
	}
}

// TestContextPacketValidateRejectsOtherwiseEmptyValue locks the Oracle
// gate finding directly: a structurally-empty ContextPacket (only
// schema_version set) must never pass canonical validation, even though
// the old mcp_validate.go response validator only inspected
// structured.schema_version and left every other required field
// unchecked.
func TestContextPacketValidateRejectsOtherwiseEmptyValue(t *testing.T) {
	empty := ContextPacket{SchemaVersion: ContextPacketSchema}
	if err := empty.Validate(); err == nil {
		t.Fatal("validator accepted an otherwise-empty context_packet value")
	}
}
