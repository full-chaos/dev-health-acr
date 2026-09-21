package sidecar

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// A derived client shares the pool but never falls back to the process
// credential chain: a nil source and a nil base are refused, and the derived
// client sends exactly the bearer its own source returns.
func TestWithCredentialSourceBindsOnlyTheGivenSource(t *testing.T) {
	var got []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	base, _ := url.Parse(server.URL)
	cfg := Config{APIBaseURL: base, Timeout: 2 * time.Second, MaxResponseBytes: 1 << 16, MaxRequestBodyBytes: 1 << 16, AllowInsecureLoopback: true, ClientName: "t", ClientVersion: "1.0.0", SidecarVersion: "1.0.0"}
	refusing := errors.New("no process credential")
	shared, err := NewClient(cfg, func() (CredentialResult, error) { return CredentialResult{}, refusing })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := shared.WithCredentialSource(nil); err == nil {
		t.Fatal("a nil credential source was accepted")
	}
	if _, err := (*Client)(nil).WithCredentialSource(func() (CredentialResult, error) { return CredentialResult{}, nil }); err == nil {
		t.Fatal("a nil base client was accepted")
	}
	token := "fcacr_" + base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	derived, err := shared.WithCredentialSource(func() (CredentialResult, error) { return CredentialResult{Token: token, Source: "caller"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if derived.http != shared.http {
		t.Fatal("derived client does not share the connection pool")
	}
	_, _ = derived.Capabilities(context.Background())
	if _, err := shared.Capabilities(context.Background()); !errors.Is(err, refusing) {
		t.Fatalf("base client call: %v, want its own refusal", err)
	}
	if len(got) != 1 || got[0] != "Bearer "+token {
		t.Fatalf("hosted API saw %q, want exactly the derived bearer once", got)
	}
}

// Reachable sends no credential and classifies a non-2xx liveness answer.
func TestReachableSendsNoCredential(t *testing.T) {
	status := http.StatusOK
	var sawAuth bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = sawAuth || r.Header.Get("Authorization") != ""
		if r.URL.Path != hostedLivenessPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
	}))
	base, _ := url.Parse(server.URL)
	cfg := Config{APIBaseURL: base, Timeout: 2 * time.Second, MaxResponseBytes: 1 << 16, MaxRequestBodyBytes: 1 << 16, AllowInsecureLoopback: true, ClientName: "t", ClientVersion: "1.0.0", SidecarVersion: "1.0.0"}
	client, err := NewClient(cfg, func() (CredentialResult, error) { return CredentialResult{}, errors.New("unused") })
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reachable(context.Background()); err != nil {
		t.Fatalf("live: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := client.Reachable(context.Background()); !errors.Is(err, ErrHostedNotLive) {
		t.Fatalf("not live: %v", err)
	}
	server.Close()
	if err := client.Reachable(context.Background()); !errors.Is(err, ErrTransportUnavailable) {
		t.Fatalf("down: %v", err)
	}
	if sawAuth {
		t.Fatal("liveness probe sent a credential")
	}
}
