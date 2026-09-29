package config

import (
	"strings"
	"testing"
	"time"
)

func dataQueryEnv(extra map[string]string) lookupEnv {
	env := map[string]string{"ACR_LOCAL_COMPOSITION_READY": "true"}
	for k, v := range extra {
		env[k] = v
	}
	return mapLookup(env)
}

func TestDataQueryDefaultsOff(t *testing.T) {
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataQueryURL() != "" {
		t.Fatalf("DataQueryURL = %q, want empty (feature off)", cfg.DataQueryURL())
	}
	if cfg.DataQueryTimeout() != 30*time.Second {
		t.Fatalf("DataQueryTimeout = %s, want 30s", cfg.DataQueryTimeout())
	}
}

func TestDataQueryAcceptsValid(t *testing.T) {
	cfg, err := load(dataQueryEnv(map[string]string{
		"ACR_DATA_QUERY_URL":     "http://ops-query.dev-health.svc:8095",
		"ACR_DATA_QUERY_TIMEOUT": "55s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataQueryURL() != "http://ops-query.dev-health.svc:8095" || cfg.DataQueryTimeout() != 55*time.Second {
		t.Fatalf("got %q %s", cfg.DataQueryURL(), cfg.DataQueryTimeout())
	}
	if _, err := load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_TIMEOUT": "1s", "ACR_DATA_QUERY_URL": "https://q.example/base"})); err != nil {
		t.Fatalf("1s https: %v", err)
	}
}

func TestDataQueryRejectsBadURL(t *testing.T) {
	const secret = "s3cr3t-token"
	for name, raw := range map[string]string{
		"relative":      "/query",
		"no scheme":     "ops-query:8095",
		"ftp":           "ftp://ops-query:8095",
		"userinfo":      "http://user:" + secret + "@ops-query:8095",
		"query":         "http://ops-query:8095?k=" + secret,
		"empty query":   "http://ops-query:8095?",
		"fragment":      "http://ops-query:8095#" + secret,
		"empty frag":    "http://ops-query:8095#",
		"no host":       "http:///x",
		"unparseable":   "http://ops query:8095/" + secret,
		"scheme no //":  "http:ops-query",
		"control chars": "http://ops-query:8095/\x7f" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_URL": raw}))
			if err == nil || !strings.Contains(err.Error(), "ACR_DATA_QUERY_URL") {
				t.Fatalf("load(%q) error = %v, want ACR_DATA_QUERY_URL rejection", name, err)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) {
				t.Fatalf("error leaks the URL value: %v", err)
			}
		})
	}
}

func TestDataQueryRejectsOutOfRangeTimeout(t *testing.T) {
	for _, v := range []string{"999ms", "0s", "-5s", "55.001s", "56s", "2m", "abc"} {
		_, err := load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_TIMEOUT": v}))
		if err == nil || !strings.Contains(err.Error(), "ACR_DATA_QUERY_TIMEOUT") {
			t.Fatalf("timeout %q: error = %v, want ACR_DATA_QUERY_TIMEOUT rejection", v, err)
		}
	}
}
