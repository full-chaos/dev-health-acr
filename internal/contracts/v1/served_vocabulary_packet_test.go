package v1

import (
	"fmt"
	"testing"
)

func TestPacketServedVocabularyRefusesAnOffSetValueBuiltAtRunTime(t *testing.T) {
	off := func(set []string) string { return fmt.Sprintf("%s_%d", set[0], len(set)) }
	for _, status := range SourceWatermarkStatusVocabulary() {
		if err := (SourceWatermark{Source: "s", Status: status}).Validate(); err != nil {
			t.Errorf("watermark %s refused: %v", status, err)
		}
	}
	if (SourceWatermark{Source: "s", Status: off(SourceWatermarkStatusVocabulary())}).Validate() == nil {
		t.Error("an off-set watermark status was served")
	}
	for _, provenance := range EvidenceProvenanceVocabulary() {
		if !validEvidenceProvenance(provenance) {
			t.Errorf("provenance %s refused", provenance)
		}
	}
	if validEvidenceProvenance(off(EvidenceProvenanceVocabulary())) || validEvidenceProvenance("") {
		t.Error("an off-set or empty provenance was served")
	}
	for _, gate := range append([]string{""}, CommitGateVocabulary()...) {
		if !validCommitGate(gate) {
			t.Errorf("commit gate %q refused", gate)
		}
	}
	if validCommitGate(off(CommitGateVocabulary())) {
		t.Error("an off-set commit gate was served")
	}
}

func TestCommitGateVocabularyExcludesTheTraceOnlyRefusalGates(t *testing.T) {
	for _, traceOnly := range []string{"exact_index_unproven", "exact_index_rival"} {
		if validCommitGate(traceOnly) {
			t.Errorf("%s is a trace-only gate name and must not be a digest commit gate", traceOnly)
		}
		for _, gate := range CommitGateVocabulary() {
			if gate == traceOnly {
				t.Errorf("%s is in the digest vocabulary", traceOnly)
			}
		}
	}
}
