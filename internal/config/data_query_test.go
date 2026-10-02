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

// ACR_DATA_GRAPHQL_URL (CHAOS-7075) is read at the one parse site with the
// ACR_DATA_QUERY_URL rules: empty = off; a bad value fails load without
// echoing it.
func TestDataGraphQLURL(t *testing.T) {
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataGraphQLURL() != "" {
		t.Fatalf("DataGraphQLURL = %q, want empty (feature off)", cfg.DataGraphQLURL())
	}
	cfg, err = load(dataQueryEnv(map[string]string{"ACR_DATA_GRAPHQL_URL": "http://dev-health-ops-query-api-mcp.dev-health.svc:8092"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataGraphQLURL() != "http://dev-health-ops-query-api-mcp.dev-health.svc:8092" {
		t.Fatalf("DataGraphQLURL = %q", cfg.DataGraphQLURL())
	}
	for _, bad := range []string{"ftp://h:8092", "http://user:secret@h:8092", "http://h:8092?x=1", "http://h:8092#f", "not a url"} {
		_, err := load(dataQueryEnv(map[string]string{"ACR_DATA_GRAPHQL_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "ACR_DATA_GRAPHQL_URL") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("%q: err %v", bad, err)
		}
	}
}

// ACR_DATA_QUERY_PATH: unset = "/query" (today's behaviour); a valid value is kept; a value that is SET but empty, whitespace or malformed is refused at
// startup, never silently defaulted, and the refusal does not echo the value.
func TestDataQueryPathDefaultsAndAcceptsAValidValue(t *testing.T) {
	cfg, err := load(dataQueryEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataQueryPath() != "/query" {
		t.Fatalf("DataQueryPath = %q, want the default /query", cfg.DataQueryPath())
	}
	cfg, err = load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_PATH": "/query/run-operation"}))
	if err != nil || cfg.DataQueryPath() != "/query/run-operation" {
		t.Fatalf("valid path: %v, %q", err, cfg.DataQueryPath())
	}
	cfg, err = load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_PATH": "  /query/run-operation  "}))
	if err != nil || cfg.DataQueryPath() != "/query/run-operation" {
		t.Fatalf("padded path is trimmed: %v, %q", err, cfg.DataQueryPath())
	}
}

func TestDataQueryPathRefusesASetButBadValueWithoutEchoingIt(t *testing.T) {
	const secret = "s3cr3t-token"
	for name, raw := range map[string]string{
		"empty":        "",
		"whitespace":   "   ",
		"relative":     "query/" + secret,
		"query string": "/query?k=" + secret,
		"fragment":     "/query#" + secret,
		"dot segment":  "/../" + secret,
		"empty seg":    "//" + secret,
		"trailing /":   "/query/" + secret + "/",
		"space":        "/query " + secret,
		"absolute url": "http://h/" + secret,
		"non-ascii":    "/qüry" + secret,
		"too long":     "/" + strings.Repeat("a", 201) + secret,
	} {
		_, err := load(dataQueryEnv(map[string]string{"ACR_DATA_QUERY_PATH": raw}))
		if err == nil {
			t.Errorf("%s: ACR_DATA_QUERY_PATH=%q was accepted", name, raw)
			continue
		}
		if !strings.Contains(err.Error(), "ACR_DATA_QUERY_PATH") || strings.Contains(err.Error(), secret) {
			t.Errorf("%s: error = %q, want it to name the variable and not echo the value", name, err)
		}
	}
}
