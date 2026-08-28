package contractcheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// decodeJSONNumber mirrors decodeJSONFile's own json.Decoder.UseNumber()
// convention, from a literal string, so a hand-built test instance decodes
// numbers the same way the engine's validateNumber (schema.go) expects --
// a plain float64 literal would never match its json.Number type switch.
func decodeJSONNumber(t *testing.T, literal string) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader([]byte(literal)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode literal: %v", err)
	}
	return value
}

// loadCohortMemberRegistry loads the repository's real schemas (the same
// path validateMCPSchemaDefsSync/TestRepositoryContracts use) and resolves
// the CohortMember $def directly, so this test exercises the ACTUAL
// published schema's allOf/if-then behavior -- not a hand-copied
// approximation of it.
func loadCohortMemberRegistry(t *testing.T) (*schemaRegistry, map[string]any) {
	t.Helper()
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	check := &repositoryCheck{root: root, out: &bytes.Buffer{}, quiet: true}
	if err := check.loadSchemas(); err != nil {
		t.Fatal(err)
	}
	common, ok := check.registry.byName["context_fabric_common.v1.schema.json"]
	if !ok {
		t.Fatal("context_fabric_common.v1.schema.json not loaded")
	}
	defs, ok := common["$defs"].(map[string]any)
	if !ok {
		t.Fatal("context_fabric_common.v1.schema.json missing $defs")
	}
	cohortMember, ok := defs["CohortMember"].(map[string]any)
	if !ok {
		t.Fatal("context_fabric_common.v1.schema.json missing $defs.CohortMember")
	}
	return check.registry, cohortMember
}

const cohortMemberBaseFields = `
  "subject": {"kind": "team", "canonical_id": "team:CHAOS", "label": "CHAOS"},
  "rank": 1,
  "inclusion_reasons": ["matched"]`

// TestSchemaRejectsCohortMemberCompletenessDriverCountMismatch is codex R3
// finding 4, exercised against the PUBLISHED schema's own allOf/if-then
// (CohortMember.allOf, context_fabric_common.v1.schema.json), not just the
// Go validator -- "complete" requires exactly 5 drivers.
func TestSchemaRejectsCohortMemberCompletenessDriverCountMismatch(t *testing.T) {
	t.Parallel()
	registry, cohortMember := loadCohortMemberRegistry(t)

	instance := decodeJSONNumber(t, `{`+cohortMemberBaseFields+`,
  "data_completeness": "complete",
  "drivers": [{"signal": "health.compounding_risk", "value": 0.4, "weight": 25, "weight_contributed": 10, "window": "current"}]
}`)
	err := registry.validateAt("context_fabric_common.v1.schema.json", cohortMember, instance, "$", map[string]bool{})
	if err == nil {
		t.Fatal("schema accepted data_completeness=complete with only 1 driver, want an error")
	}
}

// TestSchemaAcceptsCohortMemberCompletenessDriverCountMatch is the positive
// counterpart -- 2 drivers with degraded (<=2) must validate cleanly.
func TestSchemaAcceptsCohortMemberCompletenessDriverCountMatch(t *testing.T) {
	t.Parallel()
	registry, cohortMember := loadCohortMemberRegistry(t)

	instance := decodeJSONNumber(t, `{`+cohortMemberBaseFields+`,
  "data_completeness": "degraded",
  "drivers": [
    {"signal": "health.compounding_risk", "value": 0.4, "weight": 25, "weight_contributed": 10, "window": "current"},
    {"signal": "readiness.coverage_gap", "value": 0.4, "weight": 15, "weight_contributed": 6, "window": "current"}
  ]
}`)
	err := registry.validateAt("context_fabric_common.v1.schema.json", cohortMember, instance, "$", map[string]bool{})
	if err != nil {
		t.Fatalf("schema rejected a valid degraded/2-driver member: %v", err)
	}
}

// TestSchemaAcceptsLegacyCohortMemberWithoutDrivers proves the same
// allOf/if-then does not break a PR1-era row that omits "drivers"
// entirely -- properties constraints never apply to an absent property.
func TestSchemaAcceptsLegacyCohortMemberWithoutDrivers(t *testing.T) {
	t.Parallel()
	registry, cohortMember := loadCohortMemberRegistry(t)

	instance := decodeJSONNumber(t, `{`+cohortMemberBaseFields+`,
  "data_completeness": "complete"
}`)
	err := registry.validateAt("context_fabric_common.v1.schema.json", cohortMember, instance, "$", map[string]bool{})
	if err != nil {
		t.Fatalf("schema rejected a PR1-era member with no drivers key: %v", err)
	}
}

// TestSchemaRejectsCohortMemberDriverForNonInvestmentMixSignal is codex R2's
// finding, exercised against the published schema: threshold_labels must
// be empty for any signal other than investment_mix.
func TestSchemaRejectsCohortMemberDriverForNonInvestmentMixSignal(t *testing.T) {
	t.Parallel()
	registry, cohortMember := loadCohortMemberRegistry(t)

	instance := decodeJSONNumber(t, `{`+cohortMemberBaseFields+`,
  "data_completeness": "degraded",
  "drivers": [{"signal": "health.compounding_risk", "value": 0.4, "weight": 25, "weight_contributed": 10, "window": "current", "threshold_labels": ["investment_mix.mix_concentrated"]}]
}`)
	err := registry.validateAt("context_fabric_common.v1.schema.json", cohortMember, instance, "$", map[string]bool{})
	if err == nil {
		t.Fatal("schema accepted a non-investment_mix driver carrying a threshold label, want an error")
	}
	if !strings.Contains(err.Error(), "threshold_labels") {
		t.Fatalf("error = %v, want it to name threshold_labels", err)
	}
}
