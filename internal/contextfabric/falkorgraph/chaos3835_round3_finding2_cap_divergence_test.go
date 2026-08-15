package falkorgraph

import (
	"strings"
	"testing"
)

// This file is the CHAOS-3835 codex ROUND-3 review finding-2 proof (P2,
// id_only.go:169-170 in the round-2-fixed revision): ciRunSearchText caps
// pipeline_name/branch at capPipelineName/capBranch before composing them
// into the embedded text, but the id-only decision classified the UNCAPPED
// property value. A pipeline_name shaped like "run-<196 digits>nightly" is
// NOT pure-identifier when read uncapped (the trailing "nightly" survives),
// so the round-2 code embedded it -- except ciRunSearchText's own cap
// truncates the composed text to "run-<196 digits>", which IS pure
// identifier noise: "nightly" never actually reaches the embedder. The
// classifier's verdict and the composer's actual bytes had silently
// diverged -- the SECOND instance of this class (the first was
// retrievalHandles, closed in round 2).
//
// Fix: id_only.go now calls ciRunPipelineNameField/ciRunBranchField --
// the SAME extraction+cap functions ciRunSearchText itself calls
// (search_text.go) -- rather than re-deriving a parallel, uncapped read of
// the same properties. Both call sites can no longer disagree about what
// the row actually embeds; the class this closes (not just this instance)
// is enforced by construction rather than by a coupling comment.

// TestIsPureIdentifierCIRunSkipsWhenCappedPipelineNameIsPureIdentifierNoise
// is the finding-2 proof: a pipeline_name whose semantic tail is truncated
// away by capPipelineName must classify as id-only, because that's what
// actually gets embedded -- even though the UNCAPPED property value
// contains a real word.
//
// Mutation check: reverting the id_only.go fix (reading
// propText(entity, "pipeline_name") directly instead of
// ciRunPipelineNameField(entity)) makes this test fail -- the uncapped
// "nightly" tail makes the row read as NOT id-only. Verified live against
// the round-2-only code.
func TestIsPureIdentifierCIRunSkipsWhenCappedPipelineNameIsPureIdentifierNoise(t *testing.T) {
	t.Parallel()
	// capPipelineName is 200; "run-" + 196 nines is exactly 200 runes, so
	// capRunes truncates everything from "nightly" onward.
	name := "run-" + strings.Repeat("9", capPipelineName-4) + "nightly"
	entity := ciRunEntity(name, "", nil, nil)

	capped := ciRunPipelineNameField(entity)
	if strings.Contains(capped, "nightly") {
		t.Fatalf("fixture error: capped pipeline_name still contains the semantic tail, got %q", capped)
	}
	if !isPureIdentifierCIRun(entity) {
		t.Fatalf("a pipeline_name whose semantic tail is truncated away by capPipelineName must classify as id-only (round-3 finding 2); capped value = %q", capped)
	}
}

// TestIsPureIdentifierCIRunEmbedsWhenSemanticContentSurvivesTheCap is the
// other side: an over-cap pipeline_name (total length beyond
// capPipelineName) whose semantic content sits at the FRONT -- inside the
// surviving 200-rune window -- must still embed. This is what keeps the
// finding-2 fix from being a blunt "long names skip" rule: only names that
// become pure-identifier-shaped AFTER capping skip.
func TestIsPureIdentifierCIRunEmbedsWhenSemanticContentSurvivesTheCap(t *testing.T) {
	t.Parallel()
	name := "nightly-smoke " + strings.Repeat("9", capPipelineName+50)
	entity := ciRunEntity(name, "", nil, nil)

	capped := ciRunPipelineNameField(entity)
	if !strings.Contains(capped, "nightly-smoke") {
		t.Fatalf("fixture error: capped pipeline_name lost its semantic head, got %q", capped)
	}
	if isPureIdentifierCIRun(entity) {
		t.Fatalf("semantic content that survives truncation must still embed; capped value = %q", capped)
	}
}

// TestCiRunPipelineNameFieldAndBranchFieldAreTheComposersOwnFunctions pins
// the single-authority shape of the fix: ciRunSearchText's OWN output for
// pipeline_name/branch must be byte-identical to what
// ciRunPipelineNameField/ciRunBranchField return, because both are the
// exact same function call, not two independently-maintained expressions
// that merely happen to agree today.
func TestCiRunPipelineNameFieldAndBranchFieldAreTheComposersOwnFunctions(t *testing.T) {
	t.Parallel()
	entity := ciRunEntity("run-"+strings.Repeat("9", capPipelineName+20), strings.Repeat("a", capBranch+20), nil, nil)

	composed := ciRunSearchText(entity)
	nameField := ciRunPipelineNameField(entity)
	branchField := ciRunBranchField(entity)

	if nameField == "" || !strings.Contains(composed, nameField) {
		t.Fatalf("ciRunSearchText's composed text must contain ciRunPipelineNameField's exact capped value; field=%q composed=%q", nameField, composed)
	}
	if branchField == "" || !strings.Contains(composed, branchField) {
		t.Fatalf("ciRunSearchText's composed text must contain ciRunBranchField's exact capped value; field=%q composed=%q", branchField, composed)
	}
}
