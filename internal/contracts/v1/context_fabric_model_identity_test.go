package v1

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

// TestContextFabricVersionSet_ModelIdentityOptional_LegacyPayloadValidates is
// the probe for Codex round-2 finding #2: a result persisted before
// CHAOS-3782 (or by any writer with answer reuse disabled) has no
// model_identity captured at all. InvestigationResultStore.Get() calls
// Validate() on every read, so treating ModelIdentity as REQUIRED here would
// mean every pre-existing stored result becomes permanently unreadable the
// moment this field's validation ships. Before the fix this failed with
// "version metadata violates v1 bounds"; it must now pass.
func TestContextFabricVersionSet_ModelIdentityOptional_LegacyPayloadValidates(t *testing.T) {
	t.Parallel()
	result := validContextFabricContractResult()
	result.Versions.ModelIdentity = ""

	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a legacy result with no model_identity", err)
	}
}

// TestContextFabricVersionSet_ModelIdentityAcceptsFullWidthProviderModel is
// the probe for Codex round-2 finding #8: model_identity is
// "<provider>/<model>", and each half is independently bounded at 256 bytes
// by modelprovider.Config and ContextFabricOrgModelConfig -- so the true
// worst case a valid, already-billed model call can produce is
// 256 + 1 + 256 = 513 bytes. validVersion's shared 256-byte bound (correct
// for every OTHER VersionSet field) would reject that value even though it
// was never out of bounds anywhere it came from.
func TestContextFabricVersionSet_ModelIdentityAcceptsFullWidthProviderModel(t *testing.T) {
	t.Parallel()
	provider := repeatByte('p', 256)
	model := repeatByte('m', 256)
	fullWidth := provider + "/" + model
	if len(fullWidth) != 513 {
		t.Fatalf("test fixture length = %d, want 513", len(fullWidth))
	}

	result := validContextFabricContractResult()
	result.Versions.ModelIdentity = fullWidth
	if err := result.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil for a 513-byte provider/model identity", err)
	}

	result.Versions.ModelIdentity = fullWidth + "x"
	if err := result.Validate(); err == nil {
		t.Fatalf("Validate() error = nil, want a bounds error for a 514-byte model identity")
	}
}

// TestContextFabricVersionSet_ModelIdentityOmitemptyRoundTripsThroughSchema
// is the probe for Codex round-3 finding 2: without omitempty, decoding a
// legacy payload that never carried model_identity yields the Go zero
// value "", and re-marshaling emits "model_identity":"" -- a PRESENT empty
// string, which the schema's minLength:1 (contracts/jsonschema/v1/
// context_fabric_common.v1.schema.json, $defs.VersionSet) rejects even
// though the field's ABSENCE is allowed. Before the omitempty fix, the
// re-marshal step below produced a payload ValidateSerialized rejected;
// it must now pass, with model_identity genuinely absent from the wire
// JSON.
func TestContextFabricVersionSet_ModelIdentityOmitemptyRoundTripsThroughSchema(t *testing.T) {
	t.Parallel()

	// Given a legacy payload decoded with no model_identity captured at
	// all (result.Validate() already treats this as legitimate -- see
	// TestContextFabricVersionSet_ModelIdentityOptional_LegacyPayloadValidates
	// above).
	result := validContextFabricContractResult()
	result.Versions.ModelIdentity = ""

	// When re-marshaled, as any read-then-re-serve path does.
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("Unmarshal() into a map error = %v", err)
	}
	versions, ok := wire["versions"].(map[string]any)
	if !ok {
		t.Fatalf("wire payload versions = %T, want an object", wire["versions"])
	}
	if _, present := versions["model_identity"]; present {
		t.Fatal(`wire payload carries "model_identity" for an empty value; want it omitted entirely`)
	}

	// Then the re-marshaled wire payload validates against the canonical
	// JSON Schema -- not just Go's own Validate().
	if err := contractcheck.ValidateSerialized("", "context_fabric_investigation_result.v1.schema.json", encoded); err != nil {
		t.Fatalf("ValidateSerialized() error = %v, want a legacy-shaped result with no model_identity to validate", err)
	}
}

func repeatByte(b byte, n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}
