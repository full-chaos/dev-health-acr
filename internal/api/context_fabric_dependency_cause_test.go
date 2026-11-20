package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/falkorgraph"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// realRefusedGraphError is a real flattened graph error: a graph read against
// a closed port. The raw driver error names the address.
func realRefusedGraphError(t *testing.T) (error, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	adapter, err := falkorgraph.New(falkorgraph.Config{Addr: addr, GraphPrefix: "acr-cf-test", RequestTimeout: time.Second, MaxAttempts: 1, MaxResults: 25, PoolSize: 1, AllowInsecure: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.ProjectionWatermark(context.Background(), "org_1", "devhealth")
	if err == nil {
		t.Fatal("a read against a closed port returned no error")
	}
	return err, addr
}

func servedFailureBody(t *testing.T, investigatorErr error) (map[string]any, string, int) {
	t.Helper()
	app, token, logs := newContextFabricTestAppWithLogs(t, investigatorFunc(func(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		return contextfabric.InvestigationResult{}, investigatorErr
	}))
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationRequest(t, token))
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", response.Body.String(), err)
	}
	delete(body, "request_id")
	return body, logs.String(), response.Code
}

// TestTheRouteFailureLineNamesTheDependencyCause: a real refused graph read
// logs failure_dependency_cause, no log field carries the address, and the
// served body equals the body of a plain ErrUnavailable.
func TestTheRouteFailureLineNamesTheDependencyCause(t *testing.T) {
	graphErr, addr := realRefusedGraphError(t)
	body, logs, status := servedFailureBody(t, fmt.Errorf("resolve subjects: %w", graphErr))
	controlBody, controlLogs, controlStatus := servedFailureBody(t, fmt.Errorf("resolve subjects: %w", contextfabric.ErrUnavailable))

	if status != http.StatusServiceUnavailable || status != controlStatus || !reflect.DeepEqual(body, controlBody) {
		t.Fatalf("served %d %v, want the plain unavailable answer %d %v", status, body, controlStatus, controlBody)
	}
	entry := decodeFailureLog(t, logs)
	if got := entry["failure_dependency_cause"]; got != falkorgraph.ReadFailureConnectionRefused {
		t.Fatalf("failure_dependency_cause = %v, want %q: %s", got, falkorgraph.ReadFailureConnectionRefused, logs)
	}
	_, port, _ := net.SplitHostPort(addr)
	for _, planted := range []string{addr, "127.0.0.1", ":" + port} {
		if strings.Contains(logs, planted) {
			t.Fatalf("a log field carries %q: %s", planted, logs)
		}
	}
	if _, ok := decodeFailureLog(t, controlLogs)["failure_dependency_cause"]; ok {
		t.Fatalf("an error with no driver cause logs failure_dependency_cause: %s", controlLogs)
	}
}
