package synthesisprompt_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/synthesisprompt"
)

// CHAOS-12549: the commit gate keeps a subject only when the answer stands behind
// it, so an answer that claims one side of a comparison serves that side alone.
// The prompt has to ask for every side.

// The prompt before this rule, for a later run that compares the two.
const (
	preRulePromptVersion = "context-fabric-synthesis.v28"
	preRuleSystemSHA256  = "317c42bc219a1b882b76e528ba7a93899536457a94ff0e0c38e617f9509159c3"
)

func TestSynthesisPromptAsksAComparisonToClaimEverySide(t *testing.T) {
	t.Parallel()
	prompt := synthesisprompt.System()
	for _, term := range []string{
		"When the question compares subjects",
		"restate the same measures for every compared subject",
		"When a compared subject has no fact for a measure, state that as a limitation",
		"never carry another subject's value across",
	} {
		if !strings.Contains(prompt, term) {
			t.Errorf("the synthesis prompt does not carry %q", term)
		}
	}
}

func TestClientRulesAskAComparisonToClaimEverySide(t *testing.T) {
	t.Parallel()
	for _, rule := range synthesisprompt.ClientRules() {
		if strings.Contains(rule, "When the question compares subjects") && strings.Contains(rule, "every compared subject") {
			return
		}
	}
	t.Fatal("no client rule asks a comparison to claim every side")
}

// The served contract moves with the text, so a draft written from the old
// prompt is refused and the refusal names the current one.
func TestTheContractMovedWithTheRule(t *testing.T) {
	t.Parallel()
	if synthesisprompt.PromptVersion == preRulePromptVersion {
		t.Fatalf("the prompt version is still %q", preRulePromptVersion)
	}
	system := sha256.Sum256([]byte(synthesisprompt.System()))
	digest := hex.EncodeToString(system[:])
	if digest == preRuleSystemSHA256 {
		t.Fatal("the system prompt digest did not move")
	}
	assembly := synthesisprompt.ClientAssembly()
	if assembly.PromptVersion != synthesisprompt.PromptVersion || assembly.SystemSHA256 != digest {
		t.Fatalf("the served contract reads %q %q, want %q %q", assembly.PromptVersion, assembly.SystemSHA256, synthesisprompt.PromptVersion, digest)
	}
}
