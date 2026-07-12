package sidecar

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// These tests prove parity between validateCapabilities/
// validateContextPacket/validateExpandedEvidence (api_client_validate.go,
// now a thin delegator to internal/contracts/v1's canonical Validate()
// methods) and the canonical JSON Schema those methods are tested against.
// Every mutation table below reproduces a representative example from the
// second Oracle finding this file exists to close: an invalid service
// constant or tool enum, a missing query/ranking version, a malformed
// nested required_check/freshness/coverage entry, or an incomplete
// EvidenceRef. For each mutation, the exact same serialized payload is
// checked against both the hosted API client (via a fixture HTTP server)
// and the raw JSON Schema, so a future change that weakens
// api_client_validate.go relative to the schema fails here first.

// contractFixturePath mirrors internal/contracts/v1/contracts_test.go's
// fixturePath so both packages load the identical golden fixtures under
// contracts/examples/v1.
func contractFixturePath(t *testing.T, name string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "contracts", "examples", "v1", name)
}

func loadContractFixture[T any](t *testing.T, name string) T {
	t.Helper()
	raw, err := os.ReadFile(contractFixturePath(t, name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return value
}

// assertSchemaRejects proves a mutation table case is a genuine
// JSON-Schema-invalid payload, not an incidental Go-side-only check, so
// the paired client-rejection assertion is real parity evidence.
func assertSchemaRejects(t *testing.T, schemaName string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if err := contractcheck.ValidateSerialized("", schemaName, encoded); err == nil {
		t.Fatal("expected the canonical JSON Schema to reject this mutation too")
	}
}

// assertSchemaAccepts is assertSchemaRejects's inverse, used by the golden
// fixture acceptance tests to prove client acceptance is not broader than
// the schema's.
func assertSchemaAccepts(t *testing.T, schemaName string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if err := contractcheck.ValidateSerialized("", schemaName, encoded); err != nil {
		t.Fatalf("canonical JSON Schema rejected a golden fixture: %v", err)
	}
}

// assertSchemaAcceptsRaw is assertSchemaAccepts's raw-bytes counterpart:
// it validates already-serialized wire bytes directly instead of
// marshaling a Go value first. This is required for cases where the wire
// bytes themselves are the point of the test (for example, bytes that are
// not valid UTF-8) and re-marshaling a decoded Go string would silently
// normalize away exactly what the test needs to exercise.
func assertSchemaAcceptsRaw(t *testing.T, schemaName string, raw []byte) {
	t.Helper()
	if err := contractcheck.ValidateSerialized("", schemaName, raw); err != nil {
		t.Fatalf("canonical JSON Schema rejected a raw payload: %v", err)
	}
}

// assertSchemaRejectsRaw is assertSchemaRejects's raw-bytes counterpart;
// see assertSchemaAcceptsRaw's doc comment for why raw bytes matter.
func assertSchemaRejectsRaw(t *testing.T, schemaName string, raw []byte) {
	t.Helper()
	if err := contractcheck.ValidateSerialized("", schemaName, raw); err == nil {
		t.Fatal("expected the canonical JSON Schema to reject this raw payload too")
	}
}

func TestClientRejectsCapabilitiesSchemaInvalidMutations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contractsv1.Capabilities)
	}{
		{name: "wrong_service_constant", mutate: func(v *contractsv1.Capabilities) { v.Service = "other-service" }},
		{name: "unsupported_enabled_tool", mutate: func(v *contractsv1.Capabilities) { v.EnabledTools = []string{"unknown_tool"} }},
		{name: "nil_enabled_tools", mutate: func(v *contractsv1.Capabilities) { v.EnabledTools = nil }},
		{name: "nil_supported_schema_versions", mutate: func(v *contractsv1.Capabilities) { v.SupportedSchemaVersions = nil }},
		{name: "duplicate_supported_schema_versions", mutate: func(v *contractsv1.Capabilities) {
			v.SupportedSchemaVersions = []string{"capabilities.v1", "capabilities.v1"}
		}},
		{name: "wrong_schema_version", mutate: func(v *contractsv1.Capabilities) { v.SchemaVersion = "capabilities.v0" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := loadContractFixture[contractsv1.Capabilities](t, "capabilities.v1.json")
			tc.mutate(&fixture)
			assertSchemaRejects(t, "capabilities.v1.schema.json", fixture)
			if _, err := serveCapabilities(t, fixture); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("a schema-invalid capabilities response was accepted at the endpoint boundary: %v", err)
			}
		})
	}
}

func TestClientAcceptsGoldenCapabilities(t *testing.T) {
	fixture := loadContractFixture[contractsv1.Capabilities](t, "capabilities.v1.json")
	assertSchemaAccepts(t, "capabilities.v1.schema.json", fixture)
	if _, err := serveCapabilities(t, fixture); err != nil {
		t.Fatalf("the golden capabilities fixture was rejected at the endpoint boundary: %v", err)
	}
}

func TestClientRejectsContextPacketSchemaInvalidMutations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contractsv1.ContextPacket)
	}{
		{name: "missing_query_version", mutate: func(v *contractsv1.ContextPacket) { v.QueryVersion = "" }},
		{name: "missing_ranking_version", mutate: func(v *contractsv1.ContextPacket) { v.RankingVersion = "" }},
		{name: "status_invalid", mutate: func(v *contractsv1.ContextPacket) { v.Status = "bogus" }},
		{name: "resolved_scope_resolution_invalid", mutate: func(v *contractsv1.ContextPacket) { v.ResolvedScope.Resolution = "guessed" }},
		{name: "required_check_missing_check_id", mutate: func(v *contractsv1.ContextPacket) { v.RequiredChecks[0].CheckID = "" }},
		{name: "freshness_watermark_invalid_status", mutate: func(v *contractsv1.ContextPacket) { v.Freshness.Watermarks[0].Status = "unknown" }},
		{name: "coverage_degraded_reasons_nil", mutate: func(v *contractsv1.ContextPacket) { v.Coverage.DegradedReasons = nil }},
		{name: "coverage_sources_considered_duplicate", mutate: func(v *contractsv1.ContextPacket) {
			v.Coverage.SourcesConsidered = []string{"github", "github"}
		}},
		{name: "recommended_step_missing_label", mutate: func(v *contractsv1.ContextPacket) { v.RecommendedNextSteps[0].Label = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := loadContractFixture[contractsv1.ContextPacket](t, "context_packet.v1.json")
			tc.mutate(&fixture)
			assertSchemaRejects(t, "context_packet.v1.schema.json", fixture)
			if _, err := serveContextPacket(t, fixture); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("a schema-invalid context packet response was accepted at the endpoint boundary: %v", err)
			}
		})
	}
}

func TestClientAcceptsGoldenContextPacket(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ContextPacket](t, "context_packet.v1.json")
	assertSchemaAccepts(t, "context_packet.v1.schema.json", fixture)
	if _, err := serveContextPacket(t, fixture); err != nil {
		t.Fatalf("the golden context packet fixture was rejected at the endpoint boundary: %v", err)
	}
}

func TestClientRejectsEvidenceSchemaInvalidMutations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*contractsv1.ExpandedEvidence)
	}{
		{name: "invalid_provenance", mutate: func(v *contractsv1.ExpandedEvidence) { v.Evidence.Provenance = "guessed" }},
		{name: "missing_source_entity_id", mutate: func(v *contractsv1.ExpandedEvidence) { v.Evidence.Source.EntityID = "" }},
		{name: "missing_citation", mutate: func(v *contractsv1.ExpandedEvidence) { v.Evidence.Citation = "" }},
		{name: "nested_evidence_ref_id_too_short", mutate: func(v *contractsv1.ExpandedEvidence) { v.Evidence.EvidenceRefID = "short" }},
		{name: "confidence_out_of_range", mutate: func(v *contractsv1.ExpandedEvidence) { v.Evidence.Confidence = 1.5 }},
		{name: "top_level_availability_invalid", mutate: func(v *contractsv1.ExpandedEvidence) { v.Availability = "gone" }},
		{name: "excerpt_too_long", mutate: func(v *contractsv1.ExpandedEvidence) { v.Excerpt = strings.Repeat("e", 1001) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := loadContractFixture[contractsv1.ExpandedEvidence](t, "expanded_evidence.v1.json")
			tc.mutate(&fixture)
			assertSchemaRejects(t, "expanded_evidence.v1.schema.json", fixture)
			if _, err := serveEvidence(t, fixture); !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("a schema-invalid evidence response was accepted at the endpoint boundary: %v", err)
			}
		})
	}
}

func TestClientAcceptsGoldenEvidence(t *testing.T) {
	fixture := loadContractFixture[contractsv1.ExpandedEvidence](t, "expanded_evidence.v1.json")
	assertSchemaAccepts(t, "expanded_evidence.v1.schema.json", fixture)
	if _, err := serveEvidence(t, fixture); err != nil {
		t.Fatalf("the golden evidence fixture was rejected at the endpoint boundary: %v", err)
	}
}
