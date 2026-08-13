package v1

import (
	"encoding/json"
	"testing"
)

// Round-trip pin for ContextFabricSubjectResolution.RetrievalDegraded
// (CHAOS-3778 / codex round-1 F4), mirroring
// context_fabric_match_mechanism_test.go's shape. Both fields are
// additive-optional v1 additions to the same contract unit and must behave
// identically on the wire; a contract field with no round-trip pin is exactly
// where an omitempty or replay regression hides.

func resolutionWithDegraded(degraded bool) ContextFabricSubjectResolution {
	return ContextFabricSubjectResolution{
		Candidates: []ContextFabricSubjectCandidate{candidateWithMechanisms(ContextFabricMatchLexical)},
		Committed: []ContextFabricSubjectRef{{
			Kind: ContextFabricSubjectProject, CanonicalID: "project_ask_dev", Label: "Ask Dev",
		}},
		RetrievalDegraded: degraded,
	}
}

func TestF4_RetrievalDegradedRoundTripsBothWays(t *testing.T) {
	for _, degraded := range []bool{true, false} {
		encoded, err := json.Marshal(resolutionWithDegraded(degraded))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var decoded ContextFabricSubjectResolution
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if decoded.RetrievalDegraded != degraded {
			t.Fatalf("RetrievalDegraded round-tripped %v as %v", degraded, decoded.RetrievalDegraded)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("a resolution with RetrievalDegraded=%v must validate: %v", degraded, err)
		}
	}
}

// false must be OMITTED from the wire form, not written as
// "retrieval_degraded": false.
//
// This is the same immutability concern match_mechanisms has: an
// InvestigationResult is stored verbatim and CHAOS-3782's answer reuse keys on
// it, so a field that materializes on re-serialization changes the stored
// bytes of results that are supposed to be identical. It is also what makes
// the field genuinely additive -- a consumer that has never heard of it sees
// no new key on a healthy answer.
func TestF4_HealthyResolutionOmitsTheFieldEntirely(t *testing.T) {
	encoded, err := json.Marshal(resolutionWithDegraded(false))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["retrieval_degraded"]; present {
		t.Fatalf("a healthy resolution must omit retrieval_degraded from the wire form, got %s", encoded)
	}
	// A degraded one must of course carry it.
	encoded, err = json.Marshal(resolutionWithDegraded(true))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["retrieval_degraded"]; !present {
		t.Fatalf("a degraded resolution must carry retrieval_degraded, got %s", encoded)
	}
}

// A resolution persisted before CHAOS-3778 has no such key and must still
// decode and validate on replay. Absent decodes to false, which means "not
// reported" -- and because a healthy resolution also serializes to nothing,
// the two are indistinguishable on the wire BY DESIGN: neither is a claim that
// retrieval was degraded, which is the only claim this field ever makes.
func TestF4_PreCHAOS3778SnapshotDecodesAndValidates(t *testing.T) {
	legacy := `{"candidates":[{"receipt_id":"receipt_12345678","subject":{"kind":"project","canonical_id":"project_ask_dev","label":"Ask Dev"},"state":"proposed","match_reasons":["Hybrid graph search matched the subject."],"confidence":0.6,"evidence_ref_ids":["evidence_project_identity"]}],"committed":[{"kind":"project","canonical_id":"project_ask_dev","label":"Ask Dev"}]}`
	var replayed ContextFabricSubjectResolution
	if err := json.Unmarshal([]byte(legacy), &replayed); err != nil {
		t.Fatalf("a pre-CHAOS-3778 resolution must decode: %v", err)
	}
	if replayed.RetrievalDegraded {
		t.Fatal("an absent retrieval_degraded must decode as false, never as degraded")
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("a pre-CHAOS-3778 resolution must still validate: %v", err)
	}
	// And re-serializing it must not introduce the key, so replaying a stored
	// result does not change its bytes.
	encoded, err := json.Marshal(replayed)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, present := raw["retrieval_degraded"]; present {
		t.Fatalf("replaying a pre-CHAOS-3778 resolution must not introduce the key, got %s", encoded)
	}
}

// The field must not be reachable as anything but a boolean -- a string or a
// number would otherwise decode into a surprising zero value.
func TestF4_NonBooleanRetrievalDegradedIsRejected(t *testing.T) {
	for _, malformed := range []string{`"true"`, `1`, `null`, `{}`} {
		body := `{"candidates":[],"committed":[],"retrieval_degraded":` + malformed + `}`
		var decoded ContextFabricSubjectResolution
		err := json.Unmarshal([]byte(body), &decoded)
		if malformed == `null` {
			// JSON null into a bool is a documented no-op in encoding/json,
			// leaving the zero value; it is not a wire error and must not be
			// asserted as one.
			if err != nil {
				t.Fatalf("null must decode as the zero value, got %v", err)
			}
			if decoded.RetrievalDegraded {
				t.Fatal("null must leave RetrievalDegraded false")
			}
			continue
		}
		if err == nil {
			t.Fatalf("retrieval_degraded=%s must fail to decode as a boolean", malformed)
		}
	}
}
