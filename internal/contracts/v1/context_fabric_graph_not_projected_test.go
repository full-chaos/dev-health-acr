package v1

import (
	"encoding/json"
	"testing"
)

// Round-trip pin for ContextFabricSubjectResolution.GraphNotProjected
// (CHAOS-4077), mirroring context_fabric_retrieval_degraded_test.go's exact
// shape -- both fields are additive-optional v1 additions to the same
// contract unit and must behave identically on the wire; a contract field
// with no round-trip pin is exactly where an omitempty or replay
// regression hides.

func resolutionWithGraphNotProjected(graphNotProjected bool) ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates:        []ContextFabricSubjectCandidate{},
		Committed:         []ContextFabricSubjectRef{},
		GraphNotProjected: graphNotProjected,
	}
}

func TestCHAOS4077_GraphNotProjectedRoundTripsBothWays(t *testing.T) {
	for _, graphNotProjected := range []bool{true, false} {
		encoded, err := json.Marshal(resolutionWithGraphNotProjected(graphNotProjected))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded ContextFabricSubjectResolution
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.GraphNotProjected != graphNotProjected {
			t.Fatalf("GraphNotProjected round-tripped %v as %v", graphNotProjected, decoded.GraphNotProjected)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("a resolution with GraphNotProjected=%v must validate: %v", graphNotProjected, err)
		}
	}
}

// false must be OMITTED from the wire form, not written as
// "graph_not_projected": false -- the same immutability concern
// RetrievalDegraded's own test documents: an InvestigationResult is stored
// verbatim and CHAOS-3782's answer reuse keys on it, so a field that
// materializes on re-serialization changes the stored bytes of results
// that are supposed to be identical.
func TestCHAOS4077_ProjectedOrgResolutionOmitsTheFieldEntirely(t *testing.T) {
	encoded, err := json.Marshal(resolutionWithGraphNotProjected(false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["graph_not_projected"]; present {
		t.Fatalf("a projected-org resolution must omit graph_not_projected from the wire form, got %s", encoded)
	}
	// A never-projected one must of course carry it.
	encoded, err = json.Marshal(resolutionWithGraphNotProjected(true))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["graph_not_projected"]; !present {
		t.Fatalf("a never-projected resolution must carry graph_not_projected, got %s", encoded)
	}
}

// A resolution persisted before CHAOS-4077 has no such key and must still
// decode and validate on replay. Absent decodes to false, which means "not
// reported" -- and because a projected-org resolution also serializes to
// nothing, the two are indistinguishable on the wire BY DESIGN: neither is
// a claim that the graph IS projected, which is the only claim this field
// ever makes (the same convention RetrievalDegraded already established).
func TestCHAOS4077_PreCHAOS4077SnapshotDecodesAndValidates(t *testing.T) {
	legacy := `{"candidates":[],"committed":[]}`
	var replayed ContextFabricSubjectResolution
	if err := json.Unmarshal([]byte(legacy), &replayed); err != nil {
		t.Fatalf("a pre-CHAOS-4077 resolution must decode: %v", err)
	}
	if replayed.GraphNotProjected {
		t.Fatal("an absent graph_not_projected must decode as false, never as never-projected")
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("a pre-CHAOS-4077 resolution must still validate: %v", err)
	}
	// And re-serializing it must not introduce the key, so replaying a
	// stored result does not change its bytes.
	encoded, err := json.Marshal(replayed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["graph_not_projected"]; present {
		t.Fatalf("replaying a pre-CHAOS-4077 resolution must not introduce the key, got %s", encoded)
	}
}

// The field must not be reachable as anything but a boolean -- a string or
// a number would otherwise decode into a surprising zero value.
func TestCHAOS4077_NonBooleanGraphNotProjectedIsRejected(t *testing.T) {
	for _, malformed := range []string{`"true"`, `1`, `null`, `{}`} {
		body := `{"candidates":[],"committed":[],"graph_not_projected":` + malformed + `}`
		var decoded ContextFabricSubjectResolution
		err := json.Unmarshal([]byte(body), &decoded)
		if malformed == `null` {
			// JSON null into a bool is a documented no-op in encoding/json,
			// leaving the zero value; it is not a wire error and must not
			// be asserted as one.
			if err != nil {
				t.Fatalf("null must decode as the zero value, got %v", err)
			}
			if decoded.GraphNotProjected {
				t.Fatal("null must leave GraphNotProjected false")
			}
			continue
		}
		if err == nil {
			t.Fatalf("graph_not_projected=%s must fail to decode as a boolean", malformed)
		}
	}
}
