package hosted

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/config"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
)

// The path rule lives twice (config.ValidateDataQueryPath at startup, directread's plainQueryPath where the client is built) so that directread does
// not import config: they must agree on every path.
func TestTheQueryPathRuleOfConfigAndTheClientAgree(t *testing.T) {
	for _, path := range []string{
		"/query", "/query/run-operation", "/a/b-c/d_e/f.g/h~i", "/A1", "/", "", "query", "/query?x", "/query#f", "/..", "/../x", "/a/./b", "//x", "/a//b", "/a/",
		"/q uery", "/query\n", "/\x00", "/ünï", "http://h/q", "/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	} {
		configOK := config.ValidateDataQueryPath(path) == nil
		_, err := directread.NewHTTPQueryClientWithPath("http://h", time.Second, path)
		// The client treats "" as the default, the config rule refuses an empty value that is SET: compare on non-empty paths only.
		if path == "" {
			continue
		}
		if clientOK := err == nil; clientOK != configOK {
			t.Errorf("path %q: config accepts=%t, client accepts=%t", path, configOK, clientOK)
		}
	}
}

// buildDataReads refuses a malformed path at startup (it never falls back to /query) and composes with a valid one.
func TestBuildDataReadsRefusesAMalformedQueryPath(t *testing.T) {
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: "127.0.0.1:1", GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	engine := chaos7071Investigator{graph: adapter, facts: chaos7071Facts{}}
	gate, _ := buildDirectReads(engine, logger)
	if _, err := buildDataReads("http://query-api.internal:8091", "/query?x=1", "", 30*time.Second, engine, gate, logger); err == nil {
		t.Fatal("a malformed query path was accepted")
	}
	if _, err := buildDataReads("http://query-api.internal:8091", "/query/run-operation", "", 30*time.Second, engine, gate, logger); err != nil {
		t.Fatalf("the dedicated route path was refused: %v", err)
	}
}

// open.go hands the configured path to buildDataReads: a composition that dropped it would keep posting to /query whatever the deployment sets.
func TestOpenPassesTheConfiguredQueryPathToTheDataReads(t *testing.T) {
	raw, err := os.ReadFile("open.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "buildDataReads(request.config.DataQueryURL(), request.config.DataQueryPath(), request.config.DataGraphQLURL(),") {
		t.Fatal("open.go does not pass config.DataQueryPath() to buildDataReads")
	}
}
