package hosted

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestOAuthRuntimeComposition(t *testing.T) {
	store := memory.NewOAuthStore(time.Now)
	if got := oauthRuntime(config.Config{}, store); got != nil {
		t.Fatalf("unconfigured OAuth composed %+v", got)
	}
	cfg := config.Config{OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"}}
	runtime := oauthRuntime(cfg, store)
	if runtime == nil || runtime.Store != store || runtime.Issuer != cfg.OAuthIssuer || len(runtime.Resources) != 1 || runtime.Resources[0] != cfg.OAuthResources[0] {
		t.Fatalf("composed OAuth runtime = %+v", runtime)
	}
}
