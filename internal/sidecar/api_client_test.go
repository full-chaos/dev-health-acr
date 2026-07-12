package sidecar

import (
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// testBearerCanary is a fake, but shape-valid, secret used to prove it
// never leaks into error strings, logs, or anywhere other than the
// outgoing Authorization header. It must be shape-valid so the api_client
// credential-shape guard does not reject it before the request is sent. It
// is never a real credential.
var testBearerCanary = validTestToken(0xCA)

func fixedCredentialSource(token string) CredentialSource {
	return func() (CredentialResult, error) {
		return CredentialResult{Token: token, Source: "test-fixture"}, nil
	}
}

// writeJSONFixture and mustReadAll are shared by every api_client_*_test.go
// file's httptest handlers.
func writeJSONFixture(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func mustReadAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	data, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// newFixtureConfig builds a Config that trusts an httptest.NewTLSServer's
// self-signed certificate through the CA bundle seam, so the client goes
// through the exact same TLS verification path production traffic would,
// with no InsecureSkipVerify anywhere.
func newFixtureConfig(t *testing.T, server *httptest.Server) Config {
	t.Helper()
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		APIBaseURL:          base,
		Timeout:             5 * time.Second,
		MaxResponseBytes:    defaultMaxResponseBytes,
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
		ClientName:          "test-sidecar",
		ClientVersion:       "1.0.0",
		SidecarVersion:      "1.0.0",
	}
	if server.Certificate() != nil {
		caPath := filepath.Join(t.TempDir(), "fixture-ca.pem")
		certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
		if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.CACertPath = caPath
	} else {
		cfg.AllowInsecureLoopback = true
	}
	return cfg
}

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	if _, err := NewClient(Config{}, nil); err == nil {
		t.Fatal("an empty configuration was accepted")
	}
}

func TestNewClientDefaultsCredentialSourceToLoadCredential(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.credential == nil {
		t.Fatal("expected a default credential source")
	}
}

func TestClientBuildURLEscapesEvidenceIDAndPreventsTraversal(t *testing.T) {
	base, err := url.Parse("https://acr.example.com")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{baseURL: base, cfg: Config{APIBaseURL: base}}

	built, err := client.buildURL(evidencePathPrefix + url.PathEscape("../../../etc/passwd"))
	if err != nil {
		t.Fatal(err)
	}
	if built.Scheme != "https" || built.Host != "acr.example.com" {
		t.Fatalf("path traversal attempt changed the origin: %#v", built)
	}
	if !strings.HasPrefix(built.EscapedPath(), evidencePathPrefix) {
		t.Fatalf("escaped id escaped the fixed path prefix: %s", built.EscapedPath())
	}
	if !strings.Contains(built.EscapedPath(), "%2F") {
		t.Fatalf("embedded slash was not percent-escaped: %s", built.EscapedPath())
	}
}

func TestClientBuildURLEscapesQueryAndFragmentCharacters(t *testing.T) {
	base, err := url.Parse("https://acr.example.com")
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{baseURL: base, cfg: Config{APIBaseURL: base}}
	raw := "weird id?with=query&stuff#fragment"

	built, err := client.buildURL(evidencePathPrefix + url.PathEscape(raw))
	if err != nil {
		t.Fatal(err)
	}
	if built.RawQuery != "" {
		t.Fatalf("query string leaked from the evidence id: %q", built.RawQuery)
	}
	if built.Fragment != "" {
		t.Fatalf("fragment leaked from the evidence id: %q", built.Fragment)
	}
	if got := strings.TrimPrefix(built.Path, evidencePathPrefix); got != raw {
		t.Fatalf("round-trip mismatch: got %q want %q", got, raw)
	}
}

// validContextPacketRequest returns a minimal ContextPacketRequest that
// satisfies contractsv1.ContextPacketRequest.Validate() before the
// client's own automatic field stamping (SchemaVersion, RequestID,
// Client) runs.
func validContextPacketRequest() contractsv1.ContextPacketRequest {
	return contractsv1.ContextPacketRequest{
		Goal:       "investigate flaky checkout tests",
		Repository: contractsv1.RepositoryRef{Slug: "acme/widgets"},
		Scope:      contractsv1.RequestedScope{Branch: "main"},
		Options: contractsv1.PacketOptions{
			MaxItems:           10,
			MaxOutputTokens:    2000,
			MaxSerializedBytes: 65536,
		},
	}
}
