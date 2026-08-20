package main

import (
	"strings"
	"testing"
)

// TestFrozenQuestionHashesFromEnv_Unset is the ordinary "no known frozen
// questions" case: unset means an empty, valid exclusion set, not an error.
func TestFrozenQuestionHashesFromEnv_Unset(t *testing.T) {
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", "")
	got, err := frozenQuestionHashesFromEnv()
	if err != nil {
		t.Fatalf("frozenQuestionHashesFromEnv() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("frozenQuestionHashesFromEnv() = %v, want empty", got)
	}
}

// TestFrozenQuestionHashesFromEnv_ValidHashes proves the ordinary
// well-formed case parses correctly.
func TestFrozenQuestionHashesFromEnv_ValidHashes(t *testing.T) {
	h1 := strings.Repeat("a", 64)
	h2 := strings.Repeat("0", 64)
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", h1+","+h2)
	got, err := frozenQuestionHashesFromEnv()
	if err != nil {
		t.Fatalf("frozenQuestionHashesFromEnv() error = %v, want nil", err)
	}
	if !got[h1] || !got[h2] {
		t.Fatalf("frozenQuestionHashesFromEnv() = %v, want both %q and %q present", got, h1, h2)
	}
}

// TestFrozenQuestionHashesFromEnv_MalformedEntry_FailsLoudly is the codex
// adversarial review pin (medium finding, fixed): a malformed entry (not
// exactly 64 lowercase hex chars) must be a HARD error, never silently
// accepted as an exclusion that can never actually match anything.
func TestFrozenQuestionHashesFromEnv_MalformedEntry_FailsLoudly(t *testing.T) {
	cases := []string{
		"not-a-hash",
		strings.Repeat("a", 63), // too short
		strings.Repeat("a", 65), // too long
		strings.Repeat("A", 64), // uppercase -- must be lowercase canonical form
		strings.Repeat("g", 64), // non-hex character
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", c)
			_, err := frozenQuestionHashesFromEnv()
			if err == nil {
				t.Fatalf("frozenQuestionHashesFromEnv() with malformed entry %q: error = nil, want an error", c)
			}
		})
	}
}
