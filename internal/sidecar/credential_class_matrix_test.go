package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

// Every credential class the platform issues, other than the ACR client
// credential, must be refused wherever acr-mcp looks for its bearer. Values
// are built at run time from a prefix and a fill byte, so no secret-shaped
// literal exists in the tree.

func matrixSecret(fill byte) string {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = fill
	}
	return base64.RawURLEncoding.EncodeToString(secret)
}

func matrixJWT(t *testing.T, header, claims map[string]any) string {
	t.Helper()
	segment := func(value any) string {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(encoded)
	}
	signature := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 64)))
	return segment(header) + "." + segment(claims) + "." + signature
}

type matrixClass struct {
	name  string
	value func(t *testing.T) string
}

func nonACRCredentialClasses() []matrixClass {
	return []matrixClass{
		{"user_access_token", func(t *testing.T) string {
			return matrixJWT(t, map[string]any{"alg": "EdDSA", "typ": "at+jwt"}, map[string]any{"sub": "user-1", "org_id": "org-1"})
		}},
		{"ops_access_token_hs256", func(t *testing.T) string {
			return matrixJWT(t, map[string]any{"alg": "HS256", "typ": "JWT"}, map[string]any{"sub": "user-1"})
		}},
		{"effective_principal_envelope", func(t *testing.T) string {
			return matrixJWT(t, map[string]any{"alg": "EdDSA", "typ": "jwt"}, map[string]any{"sub": "principal-1"})
		}},
		{"workload_access_token", func(t *testing.T) string {
			return matrixJWT(t, map[string]any{"alg": "EdDSA", "typ": "at+jwt"}, map[string]any{"sub": "workload-1", "aud": "acr-api"})
		}},
		{"opaque_refresh_credential", func(*testing.T) string { return matrixSecret(7) }},
		{"internal_svc_acr_token", func(*testing.T) string { return "svc_acr_" + matrixSecret(8) }},
		{"internal_svc_worker_token", func(*testing.T) string { return "svc_worker_" + matrixSecret(9) }},
		{"external_push_ingest_token", func(*testing.T) string { return "fcpush_" + matrixSecret(10) }},
		{"license_key", func(*testing.T) string { return "dhlic_" + matrixSecret(11) + "." + matrixSecret(12) }},
	}
}

type matrixSource struct {
	name    string
	install func(t *testing.T, value string)
}

func matrixSources() []matrixSource {
	return []matrixSource{
		{"environment", func(t *testing.T, value string) {
			t.Setenv(TokenEnvironment, value)
			t.Setenv(TokenFileEnvironment, "")
			t.Setenv("HOME", t.TempDir())
		}},
		{"file", func(t *testing.T, value string) {
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenFileEnvironment, writeTokenFile(t, value+"\n"))
			t.Setenv("HOME", t.TempDir())
		}},
		{"keyring", func(t *testing.T, value string) {
			t.Setenv(TokenEnvironment, "")
			t.Setenv(TokenKeyringServiceEnvironment, "acr-sidecar-test")
			t.Setenv(TokenKeyringAccountEnvironment, "agent-a")
			t.Setenv(TokenFileEnvironment, "")
			t.Setenv("HOME", t.TempDir())
			stubKeyringLookup(t, func(context.Context, string, string) (string, bool, error) {
				return value, true, nil
			})
		}},
	}
}

func TestEveryNonACRCredentialClassIsRefusedFromEverySource(t *testing.T) {
	for _, source := range matrixSources() {
		for _, class := range nonACRCredentialClasses() {
			t.Run(source.name+"/"+class.name, func(t *testing.T) {
				value := class.value(t)
				if auth.IsTokenShapeValid(value) {
					t.Fatalf("fixture for %s is itself a valid ACR credential", class.name)
				}
				source.install(t, value)

				result, err := LoadCredential()

				if !errors.Is(err, ErrCredentialShapeInvalid) {
					t.Fatalf("LoadCredential error = %v, want ErrCredentialShapeInvalid", err)
				}
				if result.Token != "" {
					t.Fatal("LoadCredential returned a token for a non-ACR credential class")
				}
				if strings.Contains(err.Error(), value) {
					t.Fatal("the refusal echoes the credential value")
				}
			})
		}
	}
}

func TestEveryNonACRCredentialClassFailsEnumerationClosed(t *testing.T) {
	for _, source := range matrixSources() {
		for _, class := range nonACRCredentialClasses() {
			t.Run(source.name+"/"+class.name, func(t *testing.T) {
				value := class.value(t)
				source.install(t, value)

				material, err := CollectCredentialMaterial()

				if !errors.Is(err, ErrCredentialShapeInvalid) {
					t.Fatalf("CollectCredentialMaterial error = %v, want ErrCredentialShapeInvalid", err)
				}
				if len(material) != 0 {
					t.Fatalf("enumeration returned %d locations for a non-ACR credential class", len(material))
				}
				if strings.Contains(err.Error(), value) {
					t.Fatal("the enumeration refusal echoes the credential value")
				}
			})
		}
	}
}

func TestEverySourceAcceptsTheACRCredentialControl(t *testing.T) {
	control := validTestToken(21)
	for _, source := range matrixSources() {
		t.Run(source.name, func(t *testing.T) {
			source.install(t, control)

			result, err := LoadCredential()

			if err != nil {
				t.Fatalf("LoadCredential rejected the ACR credential control: %v", err)
			}
			if result.Token != control {
				t.Fatal("LoadCredential returned a different token than the source held")
			}
			material, err := CollectCredentialMaterial()
			if err != nil {
				t.Fatalf("CollectCredentialMaterial rejected the ACR credential control: %v", err)
			}
			if len(material) != 1 || material[0].Token != control {
				t.Fatalf("enumeration = %d locations, want exactly the control credential", len(material))
			}
		})
	}
}
