package hosted

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestAnHTTPClickHouseDSNNeverReachesTheEnvironmentProxyFromTheAPIRuntime(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		for _, required := range []bool{false, true} {
			components, err := openClickHouse(ctx, clickHouseOpenRequest{
				config: config.Config{
					RequireBackingStores:     required,
					ClickHouseDSN:            "http://oracle:" + proxycapture.Canary + "@upstream.invalid:8123/default",
					ClickHouseMaxBytesToRead: 256 << 20,
					EvidenceIDActiveKID:      "current",
					EvidenceIDKeys:           map[string][]byte{"current": []byte(strings.Repeat("k", 32))},
				},
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			})
			if err == nil {
				_ = components.check(ctx)
				_ = components.close()
				panic("openClickHouse accepted an http ClickHouse DSN")
			}
		}
	}).AssertNothingReached(t)
}
