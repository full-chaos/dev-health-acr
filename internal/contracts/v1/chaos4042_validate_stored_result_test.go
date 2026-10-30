package v1

import (
	"encoding/json"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
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
		result := withOneV2AnchorOption(validContextFabricContractResult())
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

// withOneV2AnchorOption gives result the one membership-verify anchor option
// a v2 result must carry (CHAOS-11975).
func withOneV2AnchorOption(result ContextFabricInvestigationResult) ContextFabricInvestigationResult {
	result.StructureNeeds = &ContextFabricStructureNeeds{
		Missing: []ContextFabricStructureNeedKind{ContextFabricStructureNeedSubjectAnchor},
		AnchorOptions: []ContextFabricAnchorOption{ContextFabricAnchorOptionV2{
			ReceiptID: "ancr_confirm00001", OptionID: "opt_repo_a", Label: "repoA",
			Kind: ContextFabricSubjectRepository, CanonicalID: "repository_widget_svc_a",
			MatchedTermHash: "aa11bb22cc33dd44ee55ff66", OfferSource: ContextFabricStructureOfferEngine,
		}.ToV1Wire()},
	}
	return result
}

// CHAOS-11975: a v2-stamped result with no membership-verify anchor option
// (a v1-shaped body labelled v2) is refused on the write and the stored path
// alike; the same body labelled v1 is valid.
func TestAV2ResultWithoutAnAnchorOptionIsRefused(t *testing.T) {
	t.Parallel()
	bare := validContextFabricContractResult()
	bare.SchemaVersion = ContextFabricInvestigationResultSchemaV2
	if err := bare.ValidateV2(); err == nil {
		t.Fatal("ValidateV2 accepted a v2 result with no structure_needs")
	}
	if err := ValidateStoredResult(bare); err == nil {
		t.Fatal("ValidateStoredResult accepted a v2 result with no structure_needs")
	}
	empty := withOneV2AnchorOption(validContextFabricContractResult())
	empty.SchemaVersion = ContextFabricInvestigationResultSchemaV2
	empty.StructureNeeds.AnchorOptions = nil
	if err := empty.ValidateV2(); err == nil {
		t.Fatal("ValidateV2 accepted a v2 result whose structure_needs has no anchor option")
	}
	full := withOneV2AnchorOption(validContextFabricContractResult())
	full.SchemaVersion = ContextFabricInvestigationResultSchemaV2
	if err := full.ValidateV2(); err != nil {
		t.Fatalf("ValidateV2 refused a v2 result with one anchor option: %v", err)
	}
	if err := validContextFabricContractResult().Validate(); err != nil {
		t.Fatalf("the v1 body is not valid as v1: %v", err)
	}

	// The published schemas, on the wire. Go omits an empty anchor_options
	// slice, so the empty-array cell is built in JSON: a v2 body whose
	// structure_needs carries "anchor_options": [] is refused by the v2
	// result and response schemas (minItems). A body with no
	// anchor_options key, or no structure_needs, is still ACCEPTED by the
	// schemas in this release -- the contract's required-field policy
	// (contractcheck -required-baseline) makes a field optional for one
	// release before it becomes required -- and refused by the Go
	// validator above. The two accepted cells flip to refused when the
	// next release adds the required entries.
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	emptied := func(mutate func(map[string]any)) []byte {
		var copy map[string]any
		_ = json.Unmarshal(encoded, &copy)
		mutate(copy)
		out, _ := json.Marshal(copy)
		return out
	}
	cells := []struct {
		name   string
		body   []byte
		accept bool
	}{
		{"one anchor option", encoded, true},
		{"empty anchor_options", emptied(func(d map[string]any) { d["structure_needs"].(map[string]any)["anchor_options"] = []any{} }), false},
		{"no anchor_options (required next release)", emptied(func(d map[string]any) { delete(d["structure_needs"].(map[string]any), "anchor_options") }), true},
		{"no structure_needs (required next release)", emptied(func(d map[string]any) { delete(d, "structure_needs") }), true},
	}
	for _, schema := range []string{"context_fabric_investigation_result.v2.schema.json", "context_fabric_investigation_response.v2.schema.json"} {
		for _, cell := range cells {
			// The response schema is the result schema plus an optional
			// synthesis_input, so a result body is a response body.
			err := contractcheck.ValidateSerialized("", schema, cell.body)
			if (err == nil) != cell.accept {
				t.Errorf("%s / %s: schema error = %v, want accepted=%t", schema, cell.name, err, cell.accept)
			}
		}
	}
}
