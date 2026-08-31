package genkitruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// CHAOS-4632, codex round 1 finding 1 (P1): bind each versioned model-facing
// contract to a DIGEST of its own content, so changing the content without
// bumping the version is a build failure rather than a silent reuse defect.
//
// WHY THIS GUARD DID NOT EXIST AND SHOULD HAVE. Both version constants
// already carry doc comments stating the rule in as many words --
// DefaultInterpretationPromptVersion's v9 note says "any change to the
// interpolated fact-kind list is a prompt content change and must bump this
// version", and DefaultSchemaVersion's v2 note reasons at length about
// version drift. But NOTHING ENFORCED EITHER. A doc comment is not a gate,
// and this slice proved it: the interpret prompt gained five paragraphs and
// the model-output schema gained five fields, every test in the repository
// stayed green, and both constants sat unchanged.
//
// The consequence is not cosmetic. Both constants are CONJUNCTIVE ReuseKey
// dimensions (ports.go; answer_reuse.go:376,403), and the reuse lookup runs
// BEFORE Interpret -- so an unbumped version means a stored answer produced
// under the OLD prompt keeps being served after deployment, and precisely
// the questions whose interpretation the new instructions were meant to
// change get answered from a cache that predates them. That is the class
// CHAOS-3862 closed, and TestCHAOS3862_PromptVersionChangeInvalidatesStoredAnswerReuse
// proves the mechanism works -- it just could not know the version had gone
// stale.
//
// HOW TO RESPOND WHEN THIS FAILS. Do NOT update the digest on its own. Bump
// the version constant, THEN update the digest, and say in the constant's
// doc comment what changed and why -- exactly as v9 and v2 each did. The
// digest is a tripwire for a decision, not a lockfile to be refreshed.
//
// The digest covers the RENDERED prompt, not the source text, so a change
// reaching it through an interpolated vocabulary (which is how v9 itself
// came about, via contextFabricFactKindList) trips it too.
func TestVersionedModelContractsAreBoundToTheirContent(t *testing.T) {
	t.Parallel()
	for _, binding := range []struct {
		name    string
		version string
		content string
		digest  string
	}{
		{
			name:    "interpretation system prompt",
			version: DefaultInterpretationPromptVersion,
			content: interpretationSystemPrompt,
			digest:  "88d4d0d6503ab428214597600ed91e72856e453a79e6083b68fdde53eeb10e65",
		},
		{
			name:    "synthesis system prompt",
			version: DefaultSynthesisPromptVersion,
			content: synthesisSystemPrompt,
			digest:  "81965d011760f32602f666927f7e8e29585c97046eb3e0689904f8a6c849e410",
		},
	} {
		t.Run(binding.name, func(t *testing.T) {
			t.Parallel()
			sum := sha256.Sum256([]byte(binding.content))
			got := hex.EncodeToString(sum[:])
			if got != binding.digest {
				t.Errorf("the %s changed but its pinned digest did not.\n  version constant: %s\n  pinned digest:    %s\n  actual digest:    %s\n\nThis is a MODEL-FACING CONTRACT CHANGE. Both prompt versions are conjunctive ReuseKey dimensions and the reuse lookup runs BEFORE Interpret, so leaving the version unchanged means stored answers produced under the OLD prompt keep being served. Bump the version constant, THEN update this digest, and record what changed in the constant's own doc comment.",
					binding.name, binding.version, binding.digest, got)
			}
		})
	}
}
