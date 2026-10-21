package factoracle

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheVenueClickHouseClientNeverDialsTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		dir, err := os.MkdirTemp("", "venue-dsn-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		dsnFile := filepath.Join(dir, "dsn")
		dsn := "http://oracle:" + proxycapture.Canary + "@upstream.invalid:8123/default"
		if err := os.WriteFile(dsnFile, []byte(dsn), 0o600); err != nil {
			panic(err)
		}
		conn, err := openVenueClickHouse(VenueConfig{ClickHouseDSNFile: dsnFile})
		if err != nil {
			panic(err)
		}
		defer conn.Close()
		proxycapture.MustAttemptDial(conn.Ping(ctx))
	}).AssertNothingReached(t)
}

func TestVenueClickHouseOptionsPinTheHTTPTransportWithoutAProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	opts, err := venueClickHouseOptions("http://u:p@upstream.invalid:8123/default?http_proxy=http%3A%2F%2F127.0.0.1%3A1", "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.HTTPProxyURL != nil {
		t.Fatalf("HTTPProxyURL = %v, want nil", opts.HTTPProxyURL)
	}
	if opts.TransportFunc == nil {
		t.Fatal("TransportFunc is nil")
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment}
	if _, err := opts.TransportFunc(transport); err != nil {
		t.Fatal(err)
	}
	if transport.Proxy != nil {
		t.Fatal("transport still has a Proxy function")
	}
}
