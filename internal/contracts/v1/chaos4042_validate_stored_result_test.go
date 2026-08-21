package v1

import (
	"testing"
)

// TestValidateStoredResult_DispatchesOnSchemaVersion is CHAOS-4042 PR3's
// own proof that the WIRE-level dispatcher (validate_context_fabric_
// result.go) mirrors contextfabric.ValidateStoredResult's dispatch exactly
// -- codex xhigh review finding, confirmed and fixed: before this, every
// caller that revalidated a result already read back off the wire called
// r.ValidateStored() directly, which hardcodes the v1 schema_version
// constant and rejects a v2-stamped result outright.
func TestValidateStoredResult_DispatchesOnSchemaVersion(t *testing.T) {
	t.Parallel()

	t.Run("v1-stamped result dispatches to ValidateStored", func(t *testing.T) {
		result := validContextFabricContractResult()
		result.SchemaVersion = ContextFabricInvestigationResultSchema
		if err := ValidateStoredResult(result); err != nil {
			t.Fatalf("ValidateStoredResult() error = %v, want nil for a valid v1 result", err)
		}
	})

	t.Run("v2-stamped result dispatches to ValidateStoredV2, not rejected as v1", func(t *testing.T) {
		result := validContextFabricContractResult()
		result.SchemaVersion = ContextFabricInvestigationResultSchemaV2
		if err := ValidateStoredResult(result); err != nil {
			t.Fatalf("ValidateStoredResult() error = %v, want nil for a valid v2 result -- a v2-stamped result must never be rejected by the v1-only validator", err)
		}
		// The regression this guards against: calling ValidateStored()
		// directly on a v2-stamped result must fail (schema_version
		// mismatch), proving the dispatcher's branch is load-bearing, not
		// redundant with what ValidateStored() would have done anyway.
		if err := result.ValidateStored(); err == nil {
			t.Fatal("result.ValidateStored() error = nil, want an error for a v2-stamped result -- if this ever starts passing, the dispatcher above stops being necessary and this test's premise is stale")
		}
	})

	t.Run("unrecognized schema_version fails closed", func(t *testing.T) {
		result := validContextFabricContractResult()
		result.SchemaVersion = "context_fabric_investigation_result.v99"
		if err := ValidateStoredResult(result); err == nil {
			t.Fatal("ValidateStoredResult() error = nil, want an error for an unrecognized schema_version")
		}
	})
}
