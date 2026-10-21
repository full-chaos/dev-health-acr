package factoracle

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/testsupport/proxycapture"
)

func TestTheDefaultOracleClientsNeverDialTheEnvironmentProxy(t *testing.T) {
	proxycapture.Check(t, func(ctx context.Context) {
		dir, err := os.MkdirTemp("", "venue-token-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		tokenFile := filepath.Join(dir, "token")
		if err := os.WriteFile(tokenFile, []byte(proxycapture.Canary), 0o600); err != nil {
			panic(err)
		}
		_, _, _, _ = (&VenuePlanes{URL: proxycapture.Target, TokenFile: tokenFile}).post(ctx, "read_facts", map[string]any{})
		_, _ = SeedTarget{BaseURL: proxycapture.Target, User: "oracle", Password: proxycapture.Canary}.query(ctx, "", "SELECT 1", nil)
		_, _, _, _ = (&VenuePlanes{URL: proxycapture.Target, TokenFile: tokenFile, Client: &http.Client{}}).post(ctx, "read_facts", map[string]any{})
		_, _ = SeedTarget{BaseURL: proxycapture.Target, User: "oracle", Password: proxycapture.Canary, Client: &http.Client{}}.query(ctx, "", "SELECT 1", nil)
	}).AssertNothingReached(t)
}
