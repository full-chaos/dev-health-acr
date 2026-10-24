package embedprovider

import "testing"

func startupAttrMap(t *testing.T, env map[string]string) map[string]any {
	t.Helper()
	attrs := StartupAttributes(func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	})
	if len(attrs)%2 != 0 {
		t.Fatalf("attributes are not key/value pairs: %v", attrs)
	}
	out := map[string]any{}
	for i := 0; i < len(attrs); i += 2 {
		out[attrs[i].(string)] = attrs[i+1]
	}
	return out
}

func configuredEnv(extra map[string]string) map[string]string {
	env := map[string]string{
		EnvBaseURL:   "https://embed.example/v1",
		EnvProvider:  "openai",
		EnvModel:     "text-embedding-3-large",
		EnvDimension: "3072",
		EnvAPIKey:    "keysecret",
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}

func TestStartupAttributes_DisclosesQueryTimeoutAndRetries(t *testing.T) {
	t.Run("explicit values", func(t *testing.T) {
		got := startupAttrMap(t, configuredEnv(map[string]string{EnvTimeout: "900ms", EnvMaxTransportRetries: "1", EnvBatchTimeout: "7s"}))
		if got["embed_query_timeout_ms"] != int64(900) || got["embed_max_transport_retries"] != 1 || got["embed_batch_timeout_ms"] != int64(7000) {
			t.Fatalf("attributes = %v", got)
		}
	})
	t.Run("go defaults", func(t *testing.T) {
		got := startupAttrMap(t, configuredEnv(nil))
		if got["embed_query_timeout_ms"] != DefaultTimeout.Milliseconds() || got["embed_max_transport_retries"] != DefaultMaxTransportRetries || got["embed_batch_timeout_ms"] != DefaultBatchTimeout.Milliseconds() {
			t.Fatalf("attributes = %v", got)
		}
	})
	t.Run("unconfigured and invalid are zero, never a default that is not in force", func(t *testing.T) {
		for name, env := range map[string]map[string]string{
			"unconfigured": nil,
			"invalid":      configuredEnv(map[string]string{EnvTimeout: "1h"}),
		} {
			got := startupAttrMap(t, env)
			if got["embed_query_timeout_ms"] != int64(0) || got["embed_max_transport_retries"] != 0 || got["embed_batch_timeout_ms"] != int64(0) {
				t.Fatalf("%s: attributes = %v", name, got)
			}
		}
	})
}
