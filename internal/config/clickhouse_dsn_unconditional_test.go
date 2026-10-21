package config

import (
	"strings"
	"testing"
)

// The scheme refusal does not depend on any other flag: a DSN that is set is checked on every load and every
// Validate, with backing stores required or not, projection on or off.
func TestClickHouseDSNSchemeIsRefusedWhateverElseIsConfigured(t *testing.T) {
	const secret = "pw-unconditional"
	for _, scheme := range []string{"http", "https"} {
		dsn := scheme + "://u:" + secret + "@ch.unconditional.example:8123/db"
		for _, required := range []string{"true", "false"} {
			t.Run("api/"+scheme+"/required="+required, func(t *testing.T) {
				values := completeRuntimeEnvironment()
				values["ACR_REQUIRE_BACKING_STORES"] = required
				values["ACR_CLICKHOUSE_DSN"] = dsn
				_, err := load(mapLookup(values))
				refusedForScheme(t, err, secret)
			})
			for _, projection := range []string{"true", "false"} {
				t.Run("projector/"+scheme+"/required="+required+"/projection="+projection, func(t *testing.T) {
					_, err := loadProjector(mapLookup(map[string]string{
						"ACR_ENVIRONMENT":                       "development",
						"ACR_REQUIRE_BACKING_STORES":            required,
						"ACR_CONTEXT_FABRIC_PROJECTION_ENABLED": projection,
						"ACR_CONTEXT_FABRIC_ORG_IDS":            "org-unconditional",
						"ACR_POSTGRES_DSN":                      "postgres://configured",
						"ACR_POSTGRES_CONNECTION_KIND":          "direct",
						"ACR_CLICKHOUSE_DSN":                    dsn,
					}), requiredStoresAll)
					refusedForScheme(t, err, secret)
				})
			}
		}
		t.Run("validate/"+scheme, func(t *testing.T) {
			values := completeRuntimeEnvironment()
			values["ACR_REQUIRE_BACKING_STORES"] = "false"
			cfg, err := load(mapLookup(values))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ClickHouseDSN = dsn
			refusedForScheme(t, cfg.Validate(), secret)
			projector, err := loadProjector(mapLookup(map[string]string{
				"ACR_ENVIRONMENT":              "development",
				"ACR_POSTGRES_DSN":             "postgres://configured",
				"ACR_POSTGRES_CONNECTION_KIND": "direct",
				"ACR_CLICKHOUSE_DSN":           "clickhouse://configured",
			}), requiredStoresAll)
			if err != nil {
				t.Fatal(err)
			}
			projector.ClickHouseDSN, projector.RequireBackingStores = dsn, false
			refusedForScheme(t, projector.validate(requiredStoresAll), secret)
		})
	}
}

func refusedForScheme(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("accepted an http(s) ClickHouse DSN, want refusal")
	}
	if !strings.Contains(err.Error(), "must use the native protocol") || strings.Contains(err.Error(), secret) {
		t.Fatalf("error %q must be the scheme refusal and not echo the DSN", err)
	}
}
