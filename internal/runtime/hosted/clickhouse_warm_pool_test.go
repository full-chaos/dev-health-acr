package hosted

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clickhousedriver "github.com/ClickHouse/clickhouse-go/v2"
	runtimeclickhouse "github.com/full-chaos/dev-health-go/clickhouse"

	"github.com/full-chaos/dev-health-acr/internal/config"
)

const acrMaxConcurrentStatements = 4

func assertWarmPool(t *testing.T, name string, options runtimeclickhouse.Options) {
	t.Helper()
	if options.MaxOpenConns < acrMaxConcurrentStatements {
		t.Fatalf("%s MaxOpenConns = %d, want at least the %d statements acr runs at once", name, options.MaxOpenConns, acrMaxConcurrentStatements)
	}
	if options.MaxIdleConns < options.MaxOpenConns {
		t.Fatalf("%s MaxIdleConns = %d below MaxOpenConns = %d: the client closes every surplus connection on release, so a burst re-dials TCP+TLS on the next burst", name, options.MaxIdleConns, options.MaxOpenConns)
	}
	if options.ConnMaxLifetime < 10*time.Minute {
		t.Fatalf("%s ConnMaxLifetime = %s: a short lifetime closes the connection on release and re-dials on the next statement", name, options.ConnMaxLifetime)
	}
}

func TestClickHouseClientOptionsKeepAWarmPool(t *testing.T) {
	assertWarmPool(t, "API", clickHouseClientOptions(config.Config{}, nil))
}

func TestClickHousePoolOptionsKeepTheCallersOtherFields(t *testing.T) {
	ceiling := uint64(7)
	got := ClickHousePoolOptions(runtimeclickhouse.Options{DSN: "clickhouse://h:9440/db", MaxBytesToRead: &ceiling, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetime: time.Second})
	if got.DSN != "clickhouse://h:9440/db" || got.MaxBytesToRead == nil || *got.MaxBytesToRead != 7 {
		t.Fatalf("pool options dropped the caller's fields: %+v", got)
	}
	assertWarmPool(t, "shared", got)
}

func privateCAServer(t *testing.T) (address string, caPath string) {
	t.Helper()
	server := httptest.NewTLSServer(nil)
	t.Cleanup(server.Close)
	certificate := server.Certificate()
	pemBytes := pemCertificate(certificate)
	caPath = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(server.URL, "https://"), caPath
}

func pemCertificate(certificate *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
}

func handshake(t *testing.T, address string, configuration *tls.Config) error {
	t.Helper()
	connection, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", address, configuration)
	if err == nil {
		_ = connection.Close()
	}
	return err
}

// The DSN's secure=true gives the client system roots only: a server whose
// certificate comes from a private CA is refused until ClickHouseCACertPath
// names that CA, and the bundle never turns verification off.
func TestSecureClickHouseTrustsSystemRootsAndHonoursTheCABundle(t *testing.T) {
	address, caPath := privateCAServer(t)
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := clickhousedriver.ParseDSN("clickhouse://user:pw@" + address + "/db?secure=true")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TLS == nil || parsed.TLS.RootCAs != nil || parsed.TLS.InsecureSkipVerify {
		t.Fatalf("secure=true TLS = %+v, want verification on with the system roots (nil RootCAs)", parsed.TLS)
	}
	system := parsed.TLS.Clone()
	system.ServerName = host
	if err := handshake(t, address, system); err == nil {
		t.Fatal("a private-CA server certificate was accepted with no CA bundle")
	}
	bundle, err := runtimeclickhouse.TLSConfigFromCABundle(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.InsecureSkipVerify {
		t.Fatal("the CA bundle config skips verification")
	}
	bundle.ServerName = host
	if err := handshake(t, address, bundle); err != nil {
		t.Fatalf("the server certificate was refused with its CA in ClickHouseCACertPath: %v", err)
	}
	if none, err := runtimeclickhouse.TLSConfigFromCABundle(""); err != nil || none != nil {
		t.Fatalf("empty ClickHouseCACertPath gave %v, %v; want nil config (the DSN's system-root TLS stays)", none, err)
	}
}
