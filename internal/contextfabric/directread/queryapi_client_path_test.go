package directread_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric/directread"
)

// pathRecorder is a fake ops query service that records the method and path of every request.
type pathRecorder struct {
	mu    sync.Mutex
	seen  []string
	serve *httptest.Server
}

func newPathRecorder(t *testing.T) *pathRecorder {
	t.Helper()
	recorder := &pathRecorder{}
	recorder.serve = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		recorder.mu.Lock()
		recorder.seen = append(recorder.seen, r.Method+" "+r.URL.Path)
		recorder.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"x":1}}`)
	}))
	t.Cleanup(recorder.serve.Close)
	return recorder
}

func (r *pathRecorder) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

func execute(t *testing.T, client *directread.HTTPQueryClient) {
	t.Helper()
	if _, err := client.Execute(context.Background(), directread.QueryCall{OrgID: opOrgA, Document: "query X { x }"}); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

// The configured path is the path run_operation posts to, under the base URL (with or without a base path).
func TestQueryClientPostsToTheConfiguredPath(t *testing.T) {
	for _, test := range []struct {
		name, basePath, path, want string
	}{
		{"dedicated route", "", "/query/run-operation", "POST /query/run-operation"},
		{"dedicated route under a base path", "/base/", "/query/run-operation", "POST /base/query/run-operation"},
		{"empty path is the default", "", "", "POST /query"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := newPathRecorder(t)
			client, err := directread.NewHTTPQueryClientWithPath(upstream.serve.URL+test.basePath, 5*time.Second, test.path)
			if err != nil {
				t.Fatal(err)
			}
			execute(t, client)
			if got := upstream.requests(); len(got) != 1 || got[0] != test.want {
				t.Fatalf("requests = %v, want [%s]", got, test.want)
			}
		})
	}
}

// The default constructor keeps today's behaviour: POST /query. The graphql_query (MCP listener) client is untouched: it posts to /query on its own base URL.
func TestQueryClientDefaultsAndTheListenerClientStayOnSlashQuery(t *testing.T) {
	upstream := newPathRecorder(t)
	defaultClient, err := directread.NewHTTPQueryClient(upstream.serve.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, defaultClient)
	listenerClient, err := directread.NewHTTPGraphQLClient(upstream.serve.URL, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	execute(t, listenerClient)
	if got := upstream.requests(); len(got) != 2 || got[0] != "POST /query" || got[1] != "POST /query" {
		t.Fatalf("requests = %v, want two POST /query", got)
	}
	if directread.GraphQLListenerPath != "/query" {
		t.Fatalf("GraphQLListenerPath = %q", directread.GraphQLListenerPath)
	}
}

// A path that is not an absolute plain path is refused when the client is built, never sent.
func TestQueryClientRefusesAMalformedPath(t *testing.T) {
	for _, bad := range []string{"query", "/query?x=1", "/query#f", "/../query", "//query", "/q uery", "/query/", "/a/./b", "/a//b", "/query\n", "/q\x00", "/ünï", "http://h/query"} {
		if _, err := directread.NewHTTPQueryClientWithPath("http://h", time.Second, bad); !errors.Is(err, directread.ErrQueryClientConfig) {
			t.Errorf("NewHTTPQueryClientWithPath(%q) = %v, want ErrQueryClientConfig", bad, err)
		}
	}
}

// The "context fabric operation read" Info record names the configured ops query path (path only, never the URL), so a wrong route that still answers success is visible.
func TestOperationReadInfoRecordCarriesTheConfiguredQueryPath(t *testing.T) {
	for _, test := range []struct{ name, path, want string }{
		{"dedicated route", "/query/run-operation", "/query/run-operation"},
		{"default", "", "/query"},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := newPathRecorder(t)
			client, err := directread.NewHTTPQueryClientWithPath(upstream.serve.URL+"/base", 5*time.Second, test.path)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			cat, _ := directread.DefaultCatalogue()
			runner, err := directread.NewOperationRunner(directread.OperationRunnerConfig{
				Catalogue: cat,
				Gate:      directread.NewSubjectGate(newOpGraph(), nil),
				Client:    client,
				Logger:    logger,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.Run(context.Background(), opUnrestricted(opOrgA), directread.OperationRequest{Operation: "securityAlerts"}); err != nil {
				t.Fatalf("Run: %v", err)
			}
			var found bool
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var rec map[string]any
				if err := json.Unmarshal([]byte(line), &rec); err != nil || rec["msg"] != directread.OperationReadLogMessage {
					continue
				}
				found = true
				if rec["level"] != "INFO" {
					t.Errorf("level = %v, want INFO", rec["level"])
				}
				if rec["query_path"] != test.want {
					t.Errorf("query_path = %v, want %q (record: %s)", rec["query_path"], test.want, line)
				}
				if strings.Contains(line, upstream.serve.URL) || strings.Contains(line, "127.0.0.1") {
					t.Errorf("record carries the URL: %s", line)
				}
			}
			if !found {
				t.Fatalf("no operation read record in %q", logs.String())
			}
		})
	}
}
