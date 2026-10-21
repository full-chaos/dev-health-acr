package factoracle

import (
	"context"
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
			return
		}
		defer conn.Close()
		_ = conn.Ping(ctx)
	}).AssertNothingReached(t)
}
