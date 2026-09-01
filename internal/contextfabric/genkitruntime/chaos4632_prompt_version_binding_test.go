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
	// The MODEL-OUTPUT SCHEMA is bound here alongside the prompts.
	//
	// Codex round 2 caught that the first version of this guard covered
	// only the two prompt texts, even though the P1 it was written for was
	// about BOTH DefaultInterpretationPromptVersion and
	// DefaultSchemaVersion. A later interpretationOutput change without a
	// v3 -> v4 bump would have sailed through the very guard added to stop
	// exactly that, and reuse could again serve a result produced under a
	// different output contract. A guard that covers half its own finding
	// is worse than none, because it reads as coverage.
	//
	// The schema is reflected from the Go type genkit actually sends
	// (InterpretationOutputSchema, exchange_support.go), so a field added
	// to interpretationOutput trips this even though no literal schema
	// text exists in the repository to diff.
	schema, err := InterpretationOutputSchema()
	if err != nil {
		t.Fatalf("InterpretationOutputSchema: %v", err)
	}
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
			// v13 -> v14 (CHAOS-4690 Commit F): the coverage_disclosures
			// paragraph. See DefaultSynthesisPromptVersion's own doc
			// comment for what changed and why.
			digest: "f29c3351272cffa109843d398a9fca9d3b4f2a4be51848a7b74b9309340d36a4",
		},
		{
			name:    "interpretation model-output schema",
			version: DefaultSchemaVersion,
			content: string(schema),
			digest:  "6e80b8559aad90836256efb2302c5de321bf31b7faeaff33fcc2a99b71f12d86",
		},
	} {
		t.Run(binding.name, func(t *testing.T) {
			t.Parallel()
			sum := sha256.Sum256([]byte(binding.content))
			got := hex.EncodeToString(sum[:])
			if got != binding.digest {
				t.Errorf("the %s changed but its pinned digest did not.\n  version constant: %s\n  pinned digest:    %s\n  actual digest:    %s\n\nThis is a MODEL-FACING CONTRACT CHANGE. Each version here is a conjunctive ReuseKey dimension and the reuse lookup runs BEFORE Interpret, so leaving the version unchanged means stored answers produced under the OLD contract keep being served. Bump the version constant, THEN update this digest, and record what changed in the constant's own doc comment.",
					binding.name, binding.version, binding.digest, got)
			}
		})
	}
}
