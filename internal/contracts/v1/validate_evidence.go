package v1

import (
	"fmt"
	"math"
)

// Validate enforces evidence_ref.v1.schema.json's nested "source" object
// bounds: all four identity fields are required and length-bounded, and
// safe_uri, if present, must be a well-formed URI within its length bound.
func (s EvidenceSource) Validate() error {
	if !stringLengthBetween(s.System, 1, 100) || !stringLengthBetween(s.EntityType, 1, 100) {
		return fmt.Errorf("source.system or source.entity_type violates v1 bounds")
	}
	if !stringLengthBetween(s.EntityID, 1, 1024) || !stringLengthBetween(s.DisplayLabel, 1, 1000) {
		return fmt.Errorf("source.entity_id or source.display_label violates v1 bounds")
	}
	if !optionalURI(s.SafeURI, 2048) {
		return fmt.Errorf("source.safe_uri violates v1 bounds")
	}
	return nil
}

// Validate enforces evidence_ref.v1.schema.json exactly: every required
// field must be present and within bounds, provenance and availability
// must hold one of the schema's enum values, and confidence must be a
// finite number in [0,1]. A canonical, single source of truth for this
// evidence-shape validation so callers (MCP response validators today; the
// hosted API and sidecar clients tomorrow) never re-derive these bounds.
func (r EvidenceRef) Validate() error {
	if r.SchemaVersion != EvidenceRefSchema {
		return fmt.Errorf("schema_version must be %q", EvidenceRefSchema)
	}
	if !stringLengthBetween(r.EvidenceRefID, 8, 256) {
		return fmt.Errorf("evidence_ref_id violates v1 bounds")
	}
	if err := r.Source.Validate(); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if !validEvidenceProvenance(r.Provenance) {
		return fmt.Errorf("provenance violates v1 bounds")
	}
	if math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) || r.Confidence < 0 || r.Confidence > 1 {
		return fmt.Errorf("confidence violates v1 bounds")
	}
	if !stringLengthBetween(r.Citation, 1, 2000) {
		return fmt.Errorf("citation violates v1 bounds")
	}
	if r.ObservedAt.IsZero() {
		return fmt.Errorf("observed_at is required")
	}
	if !stringLengthBetween(r.SourceVersion, 0, 512) || !stringLengthBetween(r.SnapshotHash, 0, 256) || !stringLengthBetween(r.ContentDigest, 0, 256) {
		return fmt.Errorf("evidence optional metadata fields violate v1 bounds")
	}
	if !validEvidenceAvailability(r.Availability) {
		return fmt.Errorf("availability violates v1 bounds")
	}
	return nil
}

func validEvidenceProvenance(value string) bool {
	switch value {
	case "native", "explicit_text", "heuristic", "derived":
		return true
	default:
		return false
	}
}

func validEvidenceAvailability(value EvidenceAvailability) bool {
	switch value {
	case EvidenceAvailable, EvidenceStale, EvidenceRedacted, EvidenceDeleted, EvidenceUnauthorized:
		return true
	default:
		return false
	}
}

// Validate enforces expanded_evidence.v1.schema.json: the nested evidence
// ref must itself validate, resolved_at and structured_fields are
// required-present (a nil map marshals to JSON null, which the schema's
// "type": "object" rejects), and availability must hold a recognized
// value independent of the nested evidence's own availability.
func (e ExpandedEvidence) Validate() error {
	if e.SchemaVersion != ExpandedEvidenceSchema {
		return fmt.Errorf("schema_version must be %q", ExpandedEvidenceSchema)
	}
	if err := e.Evidence.Validate(); err != nil {
		return fmt.Errorf("evidence: %w", err)
	}
	if e.ResolvedAt.IsZero() {
		return fmt.Errorf("resolved_at is required")
	}
	if !validEvidenceAvailability(e.Availability) {
		return fmt.Errorf("availability violates v1 bounds")
	}
	if !stringLengthBetween(e.Excerpt, 0, 1000) {
		return fmt.Errorf("excerpt violates v1 bounds")
	}
	if e.Structured == nil {
		return fmt.Errorf("structured_fields is required")
	}
	if !stringLengthBetween(e.RedactionReason, 0, 1000) {
		return fmt.Errorf("redaction_reason violates v1 bounds")
	}
	return nil
}
