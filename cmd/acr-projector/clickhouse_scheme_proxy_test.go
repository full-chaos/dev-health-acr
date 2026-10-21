package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

// An http(s) ClickHouse DSN reaches the HTTP driver only if every guard before it failed: the open refuses it itself,
// with backing stores required or not, and no credential reaches the environment proxy.
func TestAnHTTPClickHouseDSNNeverReachesTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		for _, required := range []bool{false, true} {
			cfg := config.ProjectorConfig{
				RequireBackingStores:     required,
				ClickHouseDSN:            "http://oracle:" + proxycapture.Canary + "@upstream.invalid:8123/default",
				ClickHouseMaxBytesToRead: 256 << 20,
			}
			client, err := openProjectorClickHouse(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
			if err == nil {
				_ = client.ping(ctx)
				_ = client.close()
				panic("openProjectorClickHouse accepted an http ClickHouse DSN")
			}
		}
	}).AssertNothingReached(t)
}
