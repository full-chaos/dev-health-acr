package main

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/dbacl"
	"github.com/stretchr/testify/require"
)

// runtimeFeatures follows the runtime's own rules: CHAOS-12140's browser_client
// is on when either browser client setting holds something other than
// whitespace (config.OAuthBrowserClassConfigured after config.Load's split),
// and the derived credential_purge row applies with it or with workload token
// exchange.
func TestRuntimeFeatures_browserClientFollowsTheRuntimeRule(t *testing.T) {
	for name, tc := range map[string]struct {
		env              map[string]string
		browser, purge   bool
		workloadExchange bool
	}{
		"nothing":                 {env: map[string]string{}},
		"blank browser settings":  {env: map[string]string{"ACR_OAUTH_BROWSER_CLIENTS": " \t", "ACR_OAUTH_DEV_BROWSER_REDIRECT_URIS": " "}},
		"browser client":          {env: map[string]string{"ACR_OAUTH_BROWSER_CLIENTS": " https://www.example.test/acr/c.json "}, browser: true, purge: true},
		"development client":      {env: map[string]string{"ACR_OAUTH_DEV_BROWSER_REDIRECT_URIS": "http://localhost:3000/acr/callback"}, browser: true, purge: true},
		"workload exchange alone": {env: map[string]string{"ACR_WORKLOAD_TOKEN_EXCHANGE_AUDIENCE": "https://acr.test", "ACR_WORKLOAD_TRUST_DOMAIN": "cluster.test"}, purge: true, workloadExchange: true},
	} {
		lookup := func(key string) (string, bool) { value, ok := tc.env[key]; return value, ok }
		features := runtimeFeatures(lookup)
		require.Equal(t, tc.browser, features[dbacl.BrowserClient], "%s: browser_client", name)
		require.Equal(t, tc.workloadExchange, features[dbacl.WorkloadTokenExchange], "%s: workload_token_exchange", name)
		require.Equal(t, tc.purge, features.Enabled(dbacl.CredentialPurge), "%s: credential_purge", name)
		var credentialDelete, familyDelete bool
		for _, statement := range dbacl.GrantStatements(`"role"`, features) {
			for _, privilege := range statement.Privileges {
				credentialDelete = credentialDelete || (statement.Table == "client_credentials" && privilege == "DELETE")
				familyDelete = familyDelete || (statement.Table == "browser_token_families" && privilege == "DELETE")
			}
		}
		require.Equal(t, tc.purge, credentialDelete, "%s: DELETE on client_credentials", name)
		require.Equal(t, tc.browser, familyDelete, "%s: DELETE on browser_token_families", name)
	}
}

func TestRuntimeFeatures_authProjectionFollowsTheRuntimeRule(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want bool
	}{
		"unset":    {env: map[string]string{}},
		"blank":    {env: map[string]string{"ACR_AUTH_PROJECTION_URL": " \t"}},
		"a URL":    {env: map[string]string{"ACR_AUTH_PROJECTION_URL": " https://auth-service.example.test "}, want: true},
		"workload": {env: map[string]string{"ACR_AUTH_WORKLOAD_EXCHANGE_URL": "https://auth-service.example.test"}},
	} {
		lookup := func(key string) (string, bool) { value, ok := tc.env[key]; return value, ok }
		features := runtimeFeatures(lookup)
		require.Equal(t, tc.want, features[dbacl.AuthProjection], "%s: auth_projection", name)
		var column bool
		for _, statement := range dbacl.GrantStatements(`"role"`, features) {
			column = column || (statement.Table == "credential_lifecycle_outbox" && len(statement.UpdateColumns) > 0)
		}
		require.Equal(t, tc.want, column, "%s: UPDATE (published_at)", name)
		var deletes bool
		for _, statement := range dbacl.GrantStatements(`"role"`, features) {
			for _, privilege := range statement.Privileges {
				deletes = deletes || (statement.Table == "credential_lifecycle_outbox" && privilege == "DELETE")
			}
		}
		require.Equal(t, tc.want, deletes, "%s: DELETE on the outbox", name)
	}
}
