package orgpurge

import (
	"context"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func realClient(t *testing.T, handler http.HandlerFunc) *entitlements.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	certificate, err := x509.ParseCertificate(server.TLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := entitlements.New(entitlements.Config{BaseURL: baseURL, TokenFile: tokenFile, CACertPath: caFile, Timeout: time.Second, MaxResponseBytes: 2048, PositiveCacheTTL: time.Minute, NegativeCacheTTL: time.Second, CacheCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

// The purge guard is driven by the real entitlement client over HTTP: only a
// 404 carrying the stable code lets Run past the Dev Health check (it then
// stops at the held lock, which is how the test sees the guard was passed).
func TestRunProceedsOnlyOnTheCodedNotFoundFromDevHealth(t *testing.T) {
	cases := map[string]struct {
		handler     http.HandlerFunc
		wantRefused bool
	}{
		"404 with the code":                    {status(404, `{"detail":"Not found","code":"acr_organization_unknown"}`), false},
		"404 without the code (older ops)":     {status(404, `{"detail":"Not found"}`), true},
		"404 from a proxy":                     {status(404, `<html></html>`), true},
		"503 outage":                           {status(503, `{"detail":"Service unavailable"}`), true},
		"500 carrying the code":                {status(500, `{"code":"acr_organization_unknown"}`), true},
		"200 organization still exists":        {status(200, `{}`), true},
		"401 token rejected, even with a code": {status(401, `{"code":"acr_organization_unknown"}`), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			purger := &Purger{DB: &sql.DB{}, Graph: noGraph{}, Set: fakeSet{}, Entitlements: realClient(t, tc.handler), Locker: heldLocker{}}
			_, err := purger.Run(context.Background(), testOrg, Options{ConfirmOrgKey: OrgKey(testOrg)})
			switch {
			case tc.wantRefused && !errors.Is(err, ErrRefused):
				t.Fatalf("Run() error = %v, want ErrRefused", err)
			case !tc.wantRefused && !errors.Is(err, ErrLocked):
				t.Fatalf("Run() error = %v, want it to pass the Dev Health check and stop at the lock (ErrLocked)", err)
			}
		})
	}
}
