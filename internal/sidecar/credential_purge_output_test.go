package sidecar

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Both halves of a keyring address come from operator-supplied environment
// variables, so a dedup key built by joining them on a separator collides
// whenever that separator appears inside either half. These two addresses are
// genuinely different entries that used to hash to the same "keyring:a:b:c"
// string key, so the second was dropped from the purge entirely -- leaving a
// live credential in the OS secret store while logout reported success.
func TestCredentialPurgeTargetsKeepsAddressesThatCollideUnderAJoinedStringKey(t *testing.T) {
	// Given
	t.Setenv(TokenKeyringServiceEnvironment, "a:b")
	t.Setenv(TokenKeyringAccountEnvironment, "c")
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "token"))
	current := CredentialResult{Source: "keyring", keyringService: "a", keyringAccount: "b:c"}

	// When
	targets := credentialPurgeTargets(current, true)

	// Then
	wantLocations := []string{
		credentialKeyringLocation("a", "b:c"),
		credentialKeyringLocation("a:b", "c"),
	}
	for _, want := range wantLocations {
		found := false
		for _, target := range targets {
			if target.location == want {
				found = true
			}
		}
		if !found {
			got := make([]string, 0, len(targets))
			for _, target := range targets {
				got = append(got, target.location)
			}
			t.Fatalf("purge targets = %v, want %q included; a separator inside an address collapsed two distinct entries", got, want)
		}
	}
}

// The same address must still be deduplicated: the captured credential and the
// configured address are usually the same entry, and purging it twice turns an
// idempotent cleanup into a spurious second failure.
func TestCredentialPurgeTargetsStillDeduplicatesIdenticalAddresses(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
	t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
	t.Setenv(TokenFileEnvironment, path)
	current := CredentialResult{Source: "keyring", keyringService: "acr-sidecar-test", keyringAccount: "agent-a", filePath: path}

	// When
	targets := credentialPurgeTargets(current, true)

	// Then
	if len(targets) != 2 {
		got := make([]string, 0, len(targets))
		for _, target := range targets {
			got = append(got, target.location)
		}
		t.Fatalf("purge targets = %v, want exactly the one file and the one keyring entry", got)
	}
}

// Cleanup locations are operator-facing but not operator-trusted: a path comes
// from ACR_API_TOKEN_FILE and a keyring address from ACR_API_TOKEN_KEYRING_*,
// so a location can carry terminal control sequences that forge log lines,
// unbounded length, or -- if a credential was ever pasted into the wrong
// variable -- bearer text. Each must still name the exact place that failed.
func TestSafeCredentialCleanupLocationsRedactsBoundsAndQuotesEveryLocation(t *testing.T) {
	token := validTestToken(70)
	cases := []struct {
		name        string
		location    string
		wantAbsent  []string
		wantPresent []string
	}{
		{
			name:        "newline forging a log line",
			location:    "/home/agent/.acr/token\nlogout successful",
			wantAbsent:  []string{"\n"},
			wantPresent: []string{`\n`, "/home/agent/.acr/token"},
		},
		{
			name:        "terminal escape",
			location:    "/home/agent/\x1b[2J\x1b[Htoken",
			wantAbsent:  []string{"\x1b"},
			wantPresent: []string{`\x1b`},
		},
		{
			name:        "token shaped content",
			location:    "keyring service " + token + " account agent-a",
			wantAbsent:  []string{token, token[len(auth.TokenPrefix):]},
			wantPresent: []string{redactedTokenMarker, "account agent-a"},
		},
		{
			name:        "oversized path",
			location:    "/home/agent/" + strings.Repeat("a", 4096) + "/token",
			wantAbsent:  []string{strings.Repeat("a", maxCleanupLocationBytes+1)},
			wantPresent: []string{"...", "/home/agent/"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			err := &CredentialPurgeError{Failures: []*CredentialCleanupError{{Location: testCase.location, cause: errors.New("cleanup failed")}}}

			// When
			locations := SafeCredentialCleanupLocations(err)

			// Then
			if len(locations) != 1 {
				t.Fatalf("safe locations = %v, want exactly one", locations)
			}
			rendered := locations[0]
			if len(rendered) > maxCleanupLocationBytes+16 {
				t.Fatalf("rendered location is %d bytes, want bounded near %d", len(rendered), maxCleanupLocationBytes)
			}
			for _, absent := range testCase.wantAbsent {
				if strings.Contains(rendered, absent) {
					t.Fatalf("rendered location %q still contains %q", rendered, absent)
				}
			}
			for _, present := range testCase.wantPresent {
				if !strings.Contains(rendered, present) {
					t.Fatalf("rendered location %q lost the operator-useful text %q", rendered, present)
				}
			}
			if !strings.HasPrefix(rendered, `"`) || !strings.HasSuffix(rendered, `"`) {
				t.Fatalf("rendered location %q is not quoted, so its boundaries are ambiguous", rendered)
			}
		})
	}
}

// The list itself is bounded, and what was dropped is stated rather than
// silently omitted: an operator told about eight locations must not assume
// those were all of them.
func TestSafeCredentialCleanupLocationsBoundsTheListAndNamesWhatItOmitted(t *testing.T) {
	// Given
	failures := make([]*CredentialCleanupError, 0, maxReportedCleanupLocations+3)
	for index := 0; index < maxReportedCleanupLocations+3; index++ {
		failures = append(failures, &CredentialCleanupError{Location: "/home/agent/token" + string(rune('a'+index)), cause: errors.New("cleanup failed")})
	}

	// When
	locations := SafeCredentialCleanupLocations(&CredentialPurgeError{Failures: failures})

	// Then
	if len(locations) != maxReportedCleanupLocations+1 {
		t.Fatalf("safe locations = %d entries, want %d plus an omission notice", len(locations), maxReportedCleanupLocations)
	}
	if !strings.Contains(locations[len(locations)-1], "and 3 more") {
		t.Fatalf("last entry = %q, want the omitted count named", locations[len(locations)-1])
	}
}

func TestSafeCredentialCleanupLocationsReturnsNothingForAnUnreportedError(t *testing.T) {
	if locations := SafeCredentialCleanupLocations(errors.New("unrelated")); locations != nil {
		t.Fatalf("safe locations = %v, want none for an error that reported no location", locations)
	}
	if locations := SafeCredentialCleanupLocations(nil); locations != nil {
		t.Fatalf("safe locations = %v, want none for a nil error", locations)
	}
}

// A purge of a location whose parent no other local user can write must still
// succeed on every platform, so the new parent guard cannot be satisfied by
// simply refusing everything.
func TestPurgeCredentialMaterialRemovesACredentialUnderARestrictedParent(t *testing.T) {
	// Given
	home := t.TempDir()
	parent := filepath.Join(home, ".acr")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "token")
	if err := os.WriteFile(path, []byte(fileToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, path)
	stubKeyringDeleter(t, func(context.Context, string, string) error {
		t.Fatal("a disabled keyring must never reach the OS secret store")
		return nil
	})
	current, err := LoadCredential()
	if err != nil {
		t.Fatal(err)
	}

	// When
	purgeErr := PurgeCredentialMaterial(current)

	// Then
	if purgeErr != nil {
		t.Fatalf("purge of a credential under a restricted parent failed: %v", purgeErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("credential file remains after purge: %v", statErr)
	}
}
