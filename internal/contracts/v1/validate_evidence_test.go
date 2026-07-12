package v1

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestEvidenceRefValidate_matches_v1_boundaries(t *testing.T) {
	base := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
	if err := base.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "evidence_ref.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*EvidenceRef)
	}{
		{name: "schema_version", mutate: func(v *EvidenceRef) { v.SchemaVersion = "wrong" }},
		{name: "evidence_ref_id_too_short", mutate: func(v *EvidenceRef) { v.EvidenceRefID = "short" }},
		{name: "source_system_empty", mutate: func(v *EvidenceRef) { v.Source.System = "" }},
		{name: "source_entity_type_empty", mutate: func(v *EvidenceRef) { v.Source.EntityType = "" }},
		{name: "source_entity_id_empty", mutate: func(v *EvidenceRef) { v.Source.EntityID = "" }},
		{name: "source_display_label_empty", mutate: func(v *EvidenceRef) { v.Source.DisplayLabel = "" }},
		{name: "source_safe_uri_invalid", mutate: func(v *EvidenceRef) { v.Source.SafeURI = "not-a-uri" }},
		{name: "provenance_invalid", mutate: func(v *EvidenceRef) { v.Provenance = "guessed" }},
		{name: "confidence_negative", mutate: func(v *EvidenceRef) { v.Confidence = -0.1 }},
		{name: "confidence_over_one", mutate: func(v *EvidenceRef) { v.Confidence = 1.1 }},
		{name: "confidence_nan", mutate: func(v *EvidenceRef) { v.Confidence = math.NaN() }},
		{name: "confidence_inf", mutate: func(v *EvidenceRef) { v.Confidence = math.Inf(1) }},
		{name: "citation_empty", mutate: func(v *EvidenceRef) { v.Citation = "" }},
		{name: "citation_too_long", mutate: func(v *EvidenceRef) { v.Citation = strings.Repeat("c", 2001) }},
		{name: "observed_at_zero", mutate: func(v *EvidenceRef) { v.ObservedAt = time.Time{} }},
		{name: "availability_invalid", mutate: func(v *EvidenceRef) { v.Availability = "gone" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := loadFixture[EvidenceRef](t, "evidence_ref.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid evidence_ref")
			}
		})
	}
}

func TestEvidenceRefValidateRejectsOtherwiseEmptyValue(t *testing.T) {
	empty := EvidenceRef{SchemaVersion: EvidenceRefSchema}
	if err := empty.Validate(); err == nil {
		t.Fatal("validator accepted an otherwise-empty evidence_ref value")
	}
}

func TestExpandedEvidenceValidate_matches_v1_boundaries(t *testing.T) {
	base := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
	if err := base.Validate(); err != nil {
		t.Fatalf("golden fixture: %v", err)
	}
	assertSchemaParity(t, "expanded_evidence.v1.schema.json", base)

	cases := []struct {
		name   string
		mutate func(*ExpandedEvidence)
	}{
		{name: "schema_version", mutate: func(v *ExpandedEvidence) { v.SchemaVersion = "wrong" }},
		{name: "nested_evidence_invalid", mutate: func(v *ExpandedEvidence) { v.Evidence.EvidenceRefID = "" }},
		{name: "resolved_at_zero", mutate: func(v *ExpandedEvidence) { v.ResolvedAt = time.Time{} }},
		{name: "availability_invalid", mutate: func(v *ExpandedEvidence) { v.Availability = "gone" }},
		{name: "excerpt_too_long", mutate: func(v *ExpandedEvidence) { v.Excerpt = strings.Repeat("e", 1001) }},
		{name: "structured_fields_nil", mutate: func(v *ExpandedEvidence) { v.Structured = nil }},
		{name: "redaction_reason_too_long", mutate: func(v *ExpandedEvidence) { v.RedactionReason = strings.Repeat("r", 1001) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := loadFixture[ExpandedEvidence](t, "expanded_evidence.v1.json")
			tc.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("validator accepted schema-invalid expanded_evidence")
			}
		})
	}
}

// TestExpandedEvidenceValidateRejectsOtherwiseEmptyValue locks the Oracle
// gate finding directly: a structurally-empty ExpandedEvidence value (only
// schema_version set) must never pass canonical validation, even though
// the old mcp_validate.go response validator only inspected
// structured.schema_version.
func TestExpandedEvidenceValidateRejectsOtherwiseEmptyValue(t *testing.T) {
	empty := ExpandedEvidence{SchemaVersion: ExpandedEvidenceSchema}
	if err := empty.Validate(); err == nil {
		t.Fatal("validator accepted an otherwise-empty expanded_evidence value")
	}
}
