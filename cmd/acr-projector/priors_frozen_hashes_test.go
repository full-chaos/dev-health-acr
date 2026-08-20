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

// TestRequireFrozenQuestionHashes_EmptyWithoutAttestation_Refuses is the
// codex adversarial review round 2 pin (high finding, repro-confirmed and
// fixed): curation must REFUSE (a hard error) rather than warn-and-proceed
// when the frozen-hash manifest is empty and the operator has not
// explicitly attested there is none to exclude.
func TestRequireFrozenQuestionHashes_EmptyWithoutAttestation_Refuses(t *testing.T) {
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", "")
	_, err := requireFrozenQuestionHashes(false)
	if err == nil {
		t.Fatal("requireFrozenQuestionHashes(false) with an empty manifest: error = nil, want an error")
	}
}

// TestRequireFrozenQuestionHashes_EmptyWithAttestation_Proceeds proves the
// explicit override actually works: an operator who deliberately attests
// no frozen corpus exists in this environment is not blocked.
func TestRequireFrozenQuestionHashes_EmptyWithAttestation_Proceeds(t *testing.T) {
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", "")
	got, err := requireFrozenQuestionHashes(true)
	if err != nil {
		t.Fatalf("requireFrozenQuestionHashes(true) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("requireFrozenQuestionHashes(true) = %v, want empty", got)
	}
}

// TestRequireFrozenQuestionHashes_ConfiguredHashes_NeverNeedsAttestation
// proves the ordinary, well-configured case is unaffected by the
// attestation flag either way.
func TestRequireFrozenQuestionHashes_ConfiguredHashes_NeverNeedsAttestation(t *testing.T) {
	h := strings.Repeat("a", 64)
	t.Setenv("ACR_CONTEXT_FABRIC_STRUCTURE_PRIORS_FROZEN_QUESTION_HASHES", h)
	got, err := requireFrozenQuestionHashes(false)
	if err != nil {
		t.Fatalf("requireFrozenQuestionHashes(false) with a configured manifest: error = %v, want nil", err)
	}
	if !got[h] {
		t.Fatalf("requireFrozenQuestionHashes(false) = %v, want %q present", got, h)
	}
}
