package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// LoadCredential answers "which credential wins". Logout needs "which
// credentials exist", and using the former meant every lower-precedence
// credential was deleted locally while it stayed live on the server.
func TestCollectCredentialMaterialEnumeratesEveryConfiguredLocationInPrecedenceOrder(t *testing.T) {
	// Given
	environmentToken := validTestToken(80)
	keyringTokenValue := validTestToken(81)
	fileTokenValue := validTestToken(82)
	service := "acr-sidecar-test"
	account := "agent-a"
	address := KeyringAddress{Service: service, Account: account}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(fileTokenValue+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(TokenEnvironment, environmentToken)
	t.Setenv(TokenKeyringDisabledEnvironment, "false")
	t.Setenv(TokenKeyringServiceEnvironment, service)
	t.Setenv(TokenKeyringAccountEnvironment, account)
	t.Setenv(TokenFileEnvironment, path)
	installTestMemoryKeyring(t, map[KeyringAddress]string{address: keyringTokenValue})

	// When
	material, err := CollectCredentialMaterial()

	// Then
	if err != nil {
		t.Fatalf("collect credential material: %v", err)
	}
	if len(material) != 3 {
		t.Fatalf("collected %d locations, want the environment, keyring, and file", len(material))
	}
	wantSources := []string{"environment", "keyring", "file"}
	wantTokens := []string{environmentToken, keyringTokenValue, fileTokenValue}
	for index := range wantSources {
		if material[index].Source != wantSources[index] {
			t.Fatalf("material[%d].Source = %q, want %q", index, material[index].Source, wantSources[index])
		}
		if material[index].Token != wantTokens[index] {
			t.Fatalf("material[%d] carries the wrong credential", index)
		}
	}
	if material[1].keyringService != service || material[1].keyringAccount != account {
		t.Fatalf("keyring material lost its address: %+v", material[1])
	}
	if material[2].filePath != path {
		t.Fatalf("file material lost its path: %+v", material[2])
	}
}

// The same credential routinely sits in more than one location after a login.
// Revoking it twice turns the second attempt into a failure against a
// credential the server already retired, which would abort a logout that had
// in fact succeeded.
func TestDistinctCredentialTokensCollapsesRepeatsAndKeepsFirstSeenOrder(t *testing.T) {
	shared := validTestToken(83)
	other := validTestToken(84)
	material := []CredentialResult{
		{Token: shared, Source: "environment"},
		{Token: other, Source: "keyring"},
		{Token: shared, Source: "file"},
		{Token: "", Source: "file"},
	}

	tokens := DistinctCredentialTokens(material)

	if len(tokens) != 2 || tokens[0] != shared || tokens[1] != other {
		t.Fatalf("distinct tokens = %d entries, want exactly the two distinct credentials in first-seen order", len(tokens))
	}
}

// Enumeration must fail closed. The caller's next step is to delete local
// material, and doing that around a location it could not read conclusively is
// how a live credential is stranded with nothing left pointing at it.
func TestCollectCredentialMaterialFailsClosedForEveryUnreadableLocation(t *testing.T) {
	service := "acr-sidecar-test"
	account := "agent-a"
	address := KeyringAddress{Service: service, Account: account}
	cases := []struct {
		name  string
		setUp func(t *testing.T)
	}{
		{
			name: "malformed environment credential",
			setUp: func(t *testing.T) {
				t.Setenv(TokenEnvironment, "not-an-acr-token")
			},
		},
		{
			name: "unreadable keyring disable flag",
			setUp: func(t *testing.T) {
				t.Setenv(TokenKeyringDisabledEnvironment, "sometimes")
			},
		},
		{
			name: "operational keyring failure",
			setUp: func(t *testing.T) {
				t.Setenv(TokenKeyringDisabledEnvironment, "false")
				keyring := installTestMemoryKeyring(t, nil)
				keyring.FailLookup(address, errors.New("secret collection is locked"))
			},
		},
		{
			name: "unreadable token file",
			setUp: func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "token")
				if err := os.WriteFile(path, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv(TokenFileEnvironment, path)
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringDisabledEnvironment, "true")
			t.Setenv(TokenKeyringServiceEnvironment, service)
			t.Setenv(TokenKeyringAccountEnvironment, account)
			t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "absent"))
			testCase.setUp(t)

			// When
			material, err := CollectCredentialMaterial()

			// Then
			if err == nil {
				t.Fatalf("collect returned %d locations and no error for an unreadable source", len(material))
			}
			if material != nil {
				t.Fatal("collect returned partial material alongside a fail-closed error")
			}
		})
	}
}

// Nothing configured is a conclusive answer, not an unknown: there is no
// credential and no error.
func TestCollectCredentialMaterialReturnsNothingWhenNoLocationIsConfigured(t *testing.T) {
	// Given
	t.Setenv("HOME", t.TempDir())
	t.Setenv(TokenEnvironment, "")
	t.Setenv(TokenKeyringDisabledEnvironment, "true")
	t.Setenv(TokenFileEnvironment, filepath.Join(t.TempDir(), "absent"))

	// When
	material, err := CollectCredentialMaterial()

	// Then
	if err != nil {
		t.Fatalf("collect with nothing configured returned %v, want no error", err)
	}
	if len(material) != 0 {
		t.Fatalf("collect returned %d locations, want none", len(material))
	}
}
