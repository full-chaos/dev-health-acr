package v1

import "testing"

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

func repeatByte(b byte, n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}
