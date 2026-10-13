package graphrank

import (
	"slices"
	"testing"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestRefusalGateNamesStayOutOfTheDigestVocabulary(t *testing.T) {
	for _, name := range []string{exactLabelRefusalCommitGate, exactLabelRivalCommitGate} {
		if slices.Contains(contractsv1.CommitGateVocabulary(), name) {
			t.Errorf("trace-only gate %q is in the digest vocabulary", name)
		}
	}
	if len(contractsv1.CommitGateVocabulary()) == 0 {
		t.Fatal("the digest vocabulary is empty; the measurement did not happen")
	}
}
