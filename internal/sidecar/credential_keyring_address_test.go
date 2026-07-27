package sidecar

import (
	"context"
	"testing"
)

// The keyring service and account used to be derived twice -- once inside
// loadFromKeyring and once in credentialKeyringAddress. That is a latent
// divergence with no symptom until the two disagree, and the consequence is
// silent: a read resolving one account while a delete resolves another leaves a
// live credential in the store that logout reported as removed. Each copy's own
// tests would have passed throughout.
//
// The derivation is pure, so every case is checked here without touching any
// secret store, and the parity test below proves the resolution path actually
// uses this derivation rather than a second copy of it.
func TestDeriveCredentialKeyringAddress(t *testing.T) {
	cases := []struct {
		name        string
		environment map[string]string
		wantService string
		wantAccount string
		wantOK      bool
	}{
		{
			name:        "nothing configured addresses nothing",
			environment: map[string]string{},
			wantService: defaultKeyringService,
			wantAccount: "",
			wantOK:      false,
		},
		{
			name:        "api url alone gives the default service and the normalized origin",
			environment: map[string]string{APIURLEnvironment: "https://ACR.Example.COM/api/v1/"},
			wantService: defaultKeyringService,
			wantAccount: "https://acr.example.com",
			wantOK:      true,
		},
		{
			name:        "origin keeps a non-default port",
			environment: map[string]string{APIURLEnvironment: "https://acr.example.com:8443/path"},
			wantService: defaultKeyringService,
			wantAccount: "https://acr.example.com:8443",
			wantOK:      true,
		},
		{
			name:        "explicit account wins over the api url",
			environment: map[string]string{APIURLEnvironment: "https://acr.example.com", TokenKeyringAccountEnvironment: "  agent-a  "},
			wantService: defaultKeyringService,
			wantAccount: "agent-a",
			wantOK:      true,
		},
		{
			name:        "explicit service does not by itself change the account",
			environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp", APIURLEnvironment: "https://acr.example.com"},
			wantService: "acr-mcp",
			wantAccount: "https://acr.example.com",
			wantOK:      true,
		},
		{
			// The per-user fallback is reachable only behind an explicit
			// service. Without one it would point every ACR install on the host
			// at a single shared entry under the default service.
			name:        "explicit service without an api url falls back to the os user",
			environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp", "USER": "chris"},
			wantService: "acr-mcp",
			wantAccount: "chris",
			wantOK:      true,
		},
		{
			name:        "explicit service falls back to USERNAME when USER is unset",
			environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp", "USERNAME": "chris"},
			wantService: "acr-mcp",
			wantAccount: "chris",
			wantOK:      true,
		},
		{
			name:        "explicit service with no user at all still addresses a fixed account",
			environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp"},
			wantService: "acr-mcp",
			wantAccount: "acr-agent",
			wantOK:      true,
		},
		{
			name:        "a malformed api url addresses nothing rather than something partial",
			environment: map[string]string{APIURLEnvironment: "not a url"},
			wantService: defaultKeyringService,
			wantAccount: "",
			wantOK:      false,
		},
		{
			name:        "a schemeless api url addresses nothing",
			environment: map[string]string{APIURLEnvironment: "//acr.example.com"},
			wantService: defaultKeyringService,
			wantAccount: "",
			wantOK:      false,
		},
		{
			name:        "a hostless api url addresses nothing",
			environment: map[string]string{APIURLEnvironment: "https:///path"},
			wantService: defaultKeyringService,
			wantAccount: "",
			wantOK:      false,
		},
		{
			name:        "whitespace-only settings are treated as unset",
			environment: map[string]string{TokenKeyringServiceEnvironment: "   ", TokenKeyringAccountEnvironment: "\t", APIURLEnvironment: " "},
			wantService: defaultKeyringService,
			wantAccount: "",
			wantOK:      false,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			service, account, ok := deriveCredentialKeyringAddress(func(name string) string {
				return testCase.environment[name]
			})

			// Then
			if service != testCase.wantService {
				t.Fatalf("service = %q, want %q", service, testCase.wantService)
			}
			if account != testCase.wantAccount {
				t.Fatalf("account = %q, want %q", account, testCase.wantAccount)
			}
			if ok != testCase.wantOK {
				t.Fatalf("configured = %v, want %v", ok, testCase.wantOK)
			}
		})
	}
}

// Parity: the address the lookup path actually queries must be the address the
// shared derivation produces. Without this the table above could pass while
// resolution used a second, divergent copy -- which is exactly the state this
// change removed.
func TestLoadFromKeyringQueriesTheSharedDerivedAddress(t *testing.T) {
	cases := []struct {
		name        string
		environment map[string]string
	}{
		{name: "default service with api url origin", environment: map[string]string{APIURLEnvironment: "https://ACR.Example.com:8443/api"}},
		{name: "explicit service and account", environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp", TokenKeyringAccountEnvironment: "agent-a"}},
		{name: "explicit service with per-user fallback", environment: map[string]string{TokenKeyringServiceEnvironment: "acr-mcp", "USER": "chris"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			for _, name := range []string{APIURLEnvironment, TokenKeyringServiceEnvironment, TokenKeyringAccountEnvironment, "USER", "USERNAME"} {
				t.Setenv(name, testCase.environment[name])
			}
			t.Setenv(TokenEnvironment, "")
			wantService, wantAccount, wantConfigured := deriveCredentialKeyringAddress(func(name string) string {
				return testCase.environment[name]
			})
			if !wantConfigured {
				t.Fatal("test case does not address a keyring entry, so it proves nothing about parity")
			}
			queried := false
			stubKeyringLookup(t, func(_ context.Context, service, account string) (string, bool, error) {
				queried = true
				if service != wantService || account != wantAccount {
					t.Errorf("lookup queried service=%q account=%q, want service=%q account=%q", service, account, wantService, wantAccount)
				}
				return keyringToken, true, nil
			})

			// When
			result, ok, err := loadFromKeyring()

			// Then
			if err != nil || !ok {
				t.Fatalf("loadFromKeyring() = (%v, %v), want a resolved credential", ok, err)
			}
			if !queried {
				t.Fatal("loadFromKeyring resolved without querying the seam, so no address was proven")
			}
			if result.keyringService != wantService || result.keyringAccount != wantAccount {
				t.Fatalf("captured address = %q/%q, want %q/%q; a captured address that differs from the queried one purges the wrong entry", result.keyringService, result.keyringAccount, wantService, wantAccount)
			}
		})
	}
}
