package v1

import (
	"encoding/json"
	"reflect"
	"testing"
)

// TestContextFabricInvestigationResultV2Schema_BoundsMatchV1Except is
// codex round-2 review finding 1's fix: TestSchemaAndGoBoundsAgree
// (schema_go_bound_agreement_test.go) only loads the v1 result schema, so
// it never checks v2's top-level bounds -- a v2-only mutation (e.g.
// question.maxLength) would escape that suite entirely. Rather than
// duplicating every "result#properties.X" bound-agreement entry a second
// time for a document that is BY DESIGN byte-identical to v1 except for
// schema_version and structure_needs, this proves that identity directly:
// every top-level "properties" entry other than those two must be
// STRUCTURALLY IDENTICAL between the two schema files. A mutation to any
// other v2 bound would fail here; a divergence in schema_version or
// structure_needs is the expected, deliberate difference this major
// exists for.
func TestContextFabricInvestigationResultV2Schema_BoundsMatchV1Except(t *testing.T) {
	t.Parallel()
	v1Schema := loadSchemaDocument(t, "context_fabric_investigation_result.v1.schema.json")
	v2Schema := loadSchemaDocument(t, "context_fabric_investigation_result.v2.schema.json")

	v1Properties, ok := v1Schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("v1 schema properties is not an object")
	}
	v2Properties, ok := v2Schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("v2 schema properties is not an object")
	}

	deliberatelyDifferent := map[string]bool{
		"schema_version":  true,
		"structure_needs": true,
	}

	for key, v1Value := range v1Properties {
		if deliberatelyDifferent[key] {
			continue
		}
		v2Value, exists := v2Properties[key]
		if !exists {
			t.Errorf("v2 schema is missing property %q that v1 declares", key)
			continue
		}
		if !reflect.DeepEqual(v1Value, v2Value) {
			t.Errorf("property %q differs between v1 and v2 schemas (v2's own bounds must equal v1's for every field this major does not change):\nv1 = %#v\nv2 = %#v", key, v1Value, v2Value)
		}
	}
	for key := range v2Properties {
		if deliberatelyDifferent[key] {
			continue
		}
		if _, exists := v1Properties[key]; !exists {
			t.Errorf("v2 schema declares property %q that v1 does not -- CHAOS-4042 promises identical JSON fields except schema_version/structure_needs", key)
		}
	}

	// The "required" array is likewise expected to be byte-identical
	// (same set of mandatory fields either way).
	if !reflect.DeepEqual(v1Schema["required"], v2Schema["required"]) {
		t.Errorf("required list differs between v1 and v2 schemas:\nv1 = %#v\nv2 = %#v", v1Schema["required"], v2Schema["required"])
	}
}

func TestContextFabricAnchorOptionV2_Validate(t *testing.T) {
	t.Parallel()
	base := ContextFabricAnchorOptionV2{
		ReceiptID: "ancr_confirm00001", OptionID: "opt_repo_a", Label: "repoA",
		Kind: ContextFabricSubjectRepository, CanonicalID: "repository_widget_svc_a",
		MatchedTermHash: "aa11bb22cc33dd44ee55ff66",
		OfferSource:     ContextFabricStructureOfferEngine,
	}
	cases := []struct {
		name    string
		mutate  func(*ContextFabricAnchorOptionV2)
		wantErr bool
	}{
		{"well formed", func(o *ContextFabricAnchorOptionV2) {}, false},
		{"missing namespace prefix", func(o *ContextFabricAnchorOptionV2) { o.ReceiptID = "kindr_confirm00001" }, true},
		{"empty canonical_id", func(o *ContextFabricAnchorOptionV2) { o.CanonicalID = "" }, true},
		{"empty matched_term_hash", func(o *ContextFabricAnchorOptionV2) { o.MatchedTermHash = "" }, true},
		{"wrong-length matched_term_hash", func(o *ContextFabricAnchorOptionV2) { o.MatchedTermHash = "tooshort" }, true},
		{"non-hex matched_term_hash", func(o *ContextFabricAnchorOptionV2) { o.MatchedTermHash = "matchedtermhash000000012" }, true},
		{"uppercase-hex matched_term_hash", func(o *ContextFabricAnchorOptionV2) { o.MatchedTermHash = "AA11BB22CC33DD44EE55FF66" }, true},
		{"invalid offer_source", func(o *ContextFabricAnchorOptionV2) { o.OfferSource = "bogus" }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opt := base
			tc.mutate(&opt)
			if err := opt.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestContextFabricAnchorOptionV2_ToV1Wire proves the wire-shape
// equivalence this file's own package doc comment claims: converting a v2
// option to its wire form and JSON-encoding it produces IDENTICAL bytes to
// encoding an equivalently-populated v1 ContextFabricAnchorOption directly
// -- the two types genuinely differ only in Go-level name, never in wire
// shape.
func TestContextFabricAnchorOptionV2_ToV1Wire(t *testing.T) {
	t.Parallel()
	v2 := ContextFabricAnchorOptionV2{
		ReceiptID: "ancr_confirm00001", OptionID: "opt_repo_a", Label: "repoA",
		Kind: ContextFabricSubjectRepository, CanonicalID: "repository_widget_svc_a",
		MatchedTermHash: "aa11bb22cc33dd44ee55ff66",
		OfferSource:     ContextFabricStructureOfferEngine,
		PriorVersionID:  "prior_version_1", PriorEntryID: "prior_entry_1",
	}
	wire := v2.ToV1Wire()
	if err := wire.Validate(); err != nil {
		t.Fatalf("converted wire value fails v1 Validate(): %v", err)
	}
	v1Equivalent := ContextFabricAnchorOption{
		ReceiptID: v2.ReceiptID, OptionID: v2.OptionID, Label: v2.Label,
		Kind: v2.Kind, CanonicalID: v2.CanonicalID, MatchedTermHash: v2.MatchedTermHash,
		OfferSource: v2.OfferSource, PriorVersionID: v2.PriorVersionID, PriorEntryID: v2.PriorEntryID,
	}
	wireEncoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal converted wire value: %v", err)
	}
	v1Encoded, err := json.Marshal(v1Equivalent)
	if err != nil {
		t.Fatalf("marshal v1 equivalent: %v", err)
	}
	if string(wireEncoded) != string(v1Encoded) {
		t.Errorf("ToV1Wire() JSON = %s, want byte-identical to an equivalently-populated v1 AnchorOption %s", wireEncoded, v1Encoded)
	}
}

// TestContextFabricInvestigationResultV2_GoldenDecodesAndValidates is
// CHAOS-4042's contract parity proof: the published v2 golden example
// strictly decodes into the SAME Go struct v1 results use, and validates
// under ValidateV2() -- proving the "JSON fields may remain identical,
// only meaning differs" design actually holds against real wire bytes, not
// just in the doc comments.
func TestContextFabricInvestigationResultV2_GoldenDecodesAndValidates(t *testing.T) {
	t.Parallel()
	encoded := contextFabricGolden(t, "context_fabric_investigation_result.v2.json")
	var result ContextFabricInvestigationResult
	if err := decodeContextFabricStrict(encoded, &result); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	if result.SchemaVersion != ContextFabricInvestigationResultSchemaV2 {
		t.Fatalf("schema_version = %q, want %q", result.SchemaVersion, ContextFabricInvestigationResultSchemaV2)
	}
	if err := result.ValidateV2(); err != nil {
		t.Fatalf("ValidateV2() error = %v", err)
	}
	if result.StructureNeeds == nil || len(result.StructureNeeds.AnchorOptions) != 2 {
		t.Fatalf("expected the golden example to carry 2 anchor_options, got %+v", result.StructureNeeds)
	}
	first, second := result.StructureNeeds.AnchorOptions[0], result.StructureNeeds.AnchorOptions[1]
	if first.MatchedTermHash != second.MatchedTermHash {
		t.Errorf("expected both anchor_options to share matched_term_hash (the ambiguous-term membership scenario this major exists for), got %q and %q", first.MatchedTermHash, second.MatchedTermHash)
	}
	if first.CanonicalID == second.CanonicalID {
		t.Errorf("expected the two anchor_options to name DISTINCT claimants, got the same canonical_id %q twice", first.CanonicalID)
	}
}

// TestContextFabricInvestigationResultV2_CrossVersionRejection is the
// ruling's binding constraint made executable: "a v1 offer must never
// acquire v2 membership semantics" and the converse. Validate() (v1) and
// ValidateV2() must never both accept the same result.
func TestContextFabricInvestigationResultV2_CrossVersionRejection(t *testing.T) {
	t.Parallel()

	t.Run("v2 golden example is rejected by the v1 validator", func(t *testing.T) {
		t.Parallel()
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_result.v2.json"), &result); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if err := result.Validate(); err == nil {
			t.Error("Validate() accepted a context_fabric_investigation_result.v2 payload; v1 must reject cross-version results")
		}
	})

	t.Run("v1 golden example is rejected by the v2 validator", func(t *testing.T) {
		t.Parallel()
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_result.v1.json"), &result); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if err := result.ValidateV2(); err == nil {
			t.Error("ValidateV2() accepted a context_fabric_investigation_result.v1 payload; v2 must reject cross-version results (never reinterpret a v1 receipt as v2)")
		}
	})

	// codex round-2 review finding: the ValidateStored*/ValidateStoredV2
	// pair (the read-back-from-durable-storage entrypoints) needs its own
	// cross-version assertions -- ValidateV2/Validate alone leave
	// ValidateStoredV2 free to drift into accepting a v1-stamped stored
	// row without any test catching it, which is exactly the "reinterpret
	// a persisted v1 receipt" risk the ruling's do-not list names.
	t.Run("v2 golden example is rejected by the v1 stored validator", func(t *testing.T) {
		t.Parallel()
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_result.v2.json"), &result); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if err := result.ValidateStored(); err == nil {
			t.Error("ValidateStored() accepted a context_fabric_investigation_result.v2 payload; v1's stored entrypoint must reject cross-version results")
		}
	})

	t.Run("v1 golden example is rejected by the v2 stored validator", func(t *testing.T) {
		t.Parallel()
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_result.v1.json"), &result); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if err := result.ValidateStoredV2(); err == nil {
			t.Error("ValidateStoredV2() accepted a context_fabric_investigation_result.v1 payload; v2's stored entrypoint must never reinterpret a persisted v1 result")
		}
	})

	t.Run("v2 golden example is accepted by the v2 stored validator", func(t *testing.T) {
		t.Parallel()
		var result ContextFabricInvestigationResult
		if err := decodeContextFabricStrict(contextFabricGolden(t, "context_fabric_investigation_result.v2.json"), &result); err != nil {
			t.Fatalf("decode golden: %v", err)
		}
		if err := result.ValidateStoredV2(); err != nil {
			t.Errorf("ValidateStoredV2() error = %v, want nil for a genuine v2 stored result", err)
		}
	})
}
