package storage

import (
	"strings"
	"testing"
	"time"
)

func TestValidOAuthClientMetadataURLDomain(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"https with path", "https://client.example.test/oauth/client.json", true},
		{"https with port and query", "https://client.example.test:8443/c.json?v=1", true},
		{"empty", "", false},
		{"http", "http://client.example.test/c.json", false},
		{"no scheme", "client.example.test/c.json", false},
		{"other scheme", "ftp://client.example.test/c.json", false},
		{"no host", "https:///c.json", false},
		{"root path", "https://client.example.test/", false},
		{"no path", "https://client.example.test", false},
		{"trailing slash", "https://client.example.test/oauth/", false},
		{"dot segment", "https://client.example.test/a/./c.json", false},
		{"dot-dot segment", "https://client.example.test/a/../c.json", false},
		{"fragment", "https://client.example.test/c.json#frag", false},
		{"user info", "https://user:pw@client.example.test/c.json", false},
		{"control character", "https://client.example.test/c\x00.json", false},
		{"newline", "https://client.example.test/c\n.json", false},
		{"invalid utf8", "https://client.example.test/\xff.json", false},
		{"oversized", "https://client.example.test/" + strings.Repeat("a", maxOAuthURILength), false},
		{"opaque", "https:client.example.test/c.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidOAuthClientMetadataURL(tc.value); got != tc.want {
				t.Fatalf("ValidOAuthClientMetadataURL(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestValidateOAuthAuthorizationRequestKindsMatchClientIDs(t *testing.T) {
	base := func() OAuthAuthorizationRequest {
		handle, device := HashOAuthSecret("handle"), HashDeviceCode("device")
		created := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
		return OAuthAuthorizationRequest{
			HandleHash: handle, DeviceCodeHash: device, RedirectURI: "https://client.example.test/cb",
			CodeChallenge: strings.Repeat("A", 43), Resource: "https://mcp.example.test/mcp",
			CreatedAt: created, ExpiresAt: created.Add(10 * time.Minute),
		}
	}
	for _, tc := range []struct {
		name     string
		kind     string
		clientID string
		want     bool
	}{
		{"metadata document", OAuthClientKindMetadataDocument, "https://client.example.test/client.json", true},
		{"metadata document with a dynamic id", OAuthClientKindMetadataDocument, "acrc_" + strings.Repeat("0", 32), false},
		{"metadata document with http", OAuthClientKindMetadataDocument, "http://client.example.test/client.json", false},
		{"dynamic", OAuthClientKindDynamic, "acrc_" + strings.Repeat("0", 32), true},
		{"dynamic with a url", OAuthClientKindDynamic, "https://client.example.test/client.json", false},
		{"unknown kind", "confidential", "https://client.example.test/client.json", false},
		{"no kind", "", "https://client.example.test/client.json", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := base()
			request.ClientKind, request.ClientID = tc.kind, tc.clientID
			if err := ValidateOAuthAuthorizationRequest(request); (err == nil) != tc.want {
				t.Fatalf("err = %v, want valid=%v", err, tc.want)
			}
		})
	}
}

func TestOAuthClientKindMetadataDocumentIsAValidRequestKind(t *testing.T) {
	if OAuthClientKindMetadataDocument != "metadata_document" {
		t.Fatalf("metadata document client kind = %q", OAuthClientKindMetadataDocument)
	}
}
