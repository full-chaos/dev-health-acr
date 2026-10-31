package hosted

import (
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestOAuthRuntimeComposition(t *testing.T) {
	store := memory.NewOAuthStore(time.Now)
	if got := oauthRuntime(config.Config{}, store, nil); got != nil {
		t.Fatalf("unconfigured OAuth composed %+v", got)
	}
	cfg := config.Config{OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp"}, OAuthClientMetadataDocuments: true}
	runtime := oauthRuntime(cfg, store, nil)
	if runtime == nil || runtime.Store != store || runtime.Issuer != cfg.OAuthIssuer || len(runtime.Resources) != 1 || runtime.Resources[0] != cfg.OAuthResources[0] || runtime.ClientMetadata == nil {
		t.Fatalf("composed OAuth runtime = %+v", runtime)
	}
	cfg.OAuthClientMetadataDocuments = false
	if runtime := oauthRuntime(cfg, store, nil); runtime == nil || runtime.ClientMetadata != nil {
		t.Fatalf("metadata documents off still composed a fetcher: %+v", runtime)
	}
}

// CHAOS-12123: the browser clients and the development static client reach
// the OAuth runtime as configured.
func TestOAuthRuntimeCompositionCarriesTheBrowserClients(t *testing.T) {
	store := memory.NewOAuthStore(time.Now)
	cfg := config.Config{
		OAuthIssuer: "https://acr.example.test", OAuthResources: []string{"https://mcp.example.test/mcp", "https://acr.example.test"},
		OAuthClientMetadataDocuments: true, OAuthBrowserClients: []string{"https://www.example.test/acr/client.json"},
		OAuthDevBrowserRedirectURIs: []string{"http://localhost:3000/acr/callback"},
	}
	runtime := oauthRuntime(cfg, store, nil)
	if runtime == nil || len(runtime.BrowserClients) != 1 || runtime.BrowserClients[0] != cfg.OAuthBrowserClients[0] ||
		len(runtime.DevBrowserRedirectURIs) != 1 || runtime.DevBrowserRedirectURIs[0] != cfg.OAuthDevBrowserRedirectURIs[0] {
		t.Fatalf("composed OAuth runtime = %+v", runtime)
	}
}
