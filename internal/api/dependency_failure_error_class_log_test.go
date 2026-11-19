package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// classifiedDependencyError is the shape internal/storage/postgres's
// sanitizeDatabaseError returns for a real Postgres failure: an operation
// prefix over storage.ErrUnavailable plus the safe *storage.DependencyErrorClass.
func classifiedDependencyError(operation string, class *storage.DependencyErrorClass) error {
	return fmt.Errorf("%s: %w: %w", operation, storage.ErrUnavailable, class)
}

func insufficientPrivilegeClass() *storage.DependencyErrorClass {
	return &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege", Table: "agent_episodes"}
}

func checkViolationClass() *storage.DependencyErrorClass {
	return &storage.DependencyErrorClass{SQLState: "23514", Class: "check_violation", Constraint: "context_packets_status_check", Table: "context_packets"}
}

// decodeLogLine returns the one JSON log line whose msg is message.
func decodeSingleLogLine(t *testing.T, logs, message string) map[string]any {
	t.Helper()
	var found map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		entry := map[string]any{}
		if json.Unmarshal([]byte(line), &entry) != nil || entry["msg"] != message {
			continue
		}
		if found != nil {
			t.Fatalf("more than one %q log line in:\n%s", message, logs)
		}
		found = entry
	}
	if found == nil {
		t.Fatalf("no %q log line in:\n%s", message, logs)
	}
	return found
}

func assertDependencyClassFields(t *testing.T, entry map[string]any, class *storage.DependencyErrorClass) {
	t.Helper()
	want := map[string]string{"db_sqlstate": class.SQLState, "db_error_class": class.Class, "db_table": class.Table}
	if class.Constraint != "" {
		want["db_constraint"] = class.Constraint
	}
	for key, value := range want {
		if entry[key] != value {
			t.Fatalf("log field %s = %v, want %q; entry = %v", key, entry[key], value, entry)
		}
	}
	if _, ok := entry["db_constraint"]; ok && class.Constraint == "" {
		t.Fatalf("log invented db_constraint for a class without one: %v", entry)
	}
}

func assertNoDependencyClassFields(t *testing.T, entry map[string]any) {
	t.Helper()
	for _, key := range []string{"db_sqlstate", "db_error_class", "db_constraint", "db_table"} {
		if _, ok := entry[key]; ok {
			t.Fatalf("log invented %s for an error without a class: %v", key, entry)
		}
	}
}

func TestEpisodeDependencyFailureLogCarriesErrorClass(t *testing.T) {
	cases := map[string]struct {
		err   error
		class *storage.DependencyErrorClass
	}{
		"classified": {err: fmt.Errorf("persist episode: %w", classifiedDependencyError("insert episode", insufficientPrivilegeClass())), class: insufficientPrivilegeClass()},
		"plain":      {err: fmt.Errorf("persist episode: %w", storage.ErrUnavailable)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			app, token := hostedEpisodeTestApp(t, &fakeEpisodeCreator{err: tc.err}, []string{auth.ScopeEpisodeWrite}, nil)
			logs := &bytes.Buffer{}
			app.logger = testLogger(logs)

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, authenticatedEpisodeRequest(t, token, episodeCreate(), "idempotency_01"))

			// Then
			assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			entry := decodeSingleLogLine(t, logs.String(), "episode recording dependency failed")
			if entry["failure_class"] != "episode_creator" {
				t.Fatalf("failure_class = %v", entry["failure_class"])
			}
			if tc.class == nil {
				assertNoDependencyClassFields(t, entry)
				return
			}
			assertDependencyClassFields(t, entry, tc.class)
			if strings.Contains(response.Body.String(), tc.class.Class) || strings.Contains(response.Body.String(), tc.class.SQLState) {
				t.Fatalf("response carries the class: %s", response.Body.String())
			}
		})
	}
}

func TestHostedReadDependencyFailureLogCarriesErrorClass(t *testing.T) {
	cases := map[string]struct {
		err   error
		class *storage.DependencyErrorClass
	}{
		"classified": {err: classifiedDependencyError("save context packet", checkViolationClass()), class: checkViolationClass()},
		"plain":      {err: storage.ErrUnavailable},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead}, nil, nil)
			app.runtime.Assembler = failingAssembler{err: tc.err}
			logs := &bytes.Buffer{}
			app.logger = testLogger(logs)

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, contextPacketRequest(t, app, token, hostedContextRequest()))

			// Then
			assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			entry := decodeSingleLogLine(t, logs.String(), "hosted read dependency failed")
			if entry["failure_class"] != "context_packet_assembly" {
				t.Fatalf("failure_class = %v", entry["failure_class"])
			}
			if tc.class == nil {
				assertNoDependencyClassFields(t, entry)
				return
			}
			assertDependencyClassFields(t, entry, tc.class)
		})
	}
}

type classifiedCapabilitiesProvider struct{ err error }

func (p classifiedCapabilitiesProvider) Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error) {
	return contractsv1.Capabilities{}, p.err
}

func TestCapabilitiesFailureLogsCarryErrorClass(t *testing.T) {
	class := &storage.DependencyErrorClass{SQLState: "08006", Class: "connection_failure"}
	cases := map[string]func(*App, string) *http.Request{
		"capabilities route": func(_ *App, token string) *http.Request {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			return request
		},
		"client version gate": func(app *App, token string) *http.Request {
			return contextPacketRequest(t, app, token, hostedContextRequest())
		},
	}
	for name, request := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			provider := classifiedCapabilitiesProvider{err: classifiedDependencyError("read capabilities", class)}
			app, token := newHostedTestApp(t, provider, nil, []string{auth.ScopeContextRead}, nil, nil)
			logs := &bytes.Buffer{}
			app.logger = testLogger(logs)

			// When
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request(app, token))

			// Then
			assertErrorResponse(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			entry := decodeSingleLogLine(t, logs.String(), "capabilities resolution failed")
			if entry["failure_class"] != "capabilities_provider" {
				t.Fatalf("failure_class = %v", entry["failure_class"])
			}
			if _, ok := entry["db_table"]; ok {
				t.Fatalf("log invented db_table for a class without one: %v", entry)
			}
			if entry["db_sqlstate"] != class.SQLState || entry["db_error_class"] != class.Class {
				t.Fatalf("log entry = %v, want db_sqlstate=%s db_error_class=%s", entry, class.SQLState, class.Class)
			}
		})
	}
}

type failingResultReadStore struct {
	contextfabric.InvestigationResultStore
	err error
}

func (s failingResultReadStore) Get(context.Context, storage.Principal, string) (contextfabric.StoredInvestigationResult, error) {
	return contextfabric.StoredInvestigationResult{}, s.err
}

func TestInvestigationResultReadFailureLogCarriesErrorClass(t *testing.T) {
	// Given
	class := checkViolationClass()
	store := failingResultReadStore{err: fmt.Errorf("get investigation result: %w", class)}
	app, token, logs := newContextFabricTestAppWithResultsAndLogs(t, nil, store)

	// When
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, investigationResultRequest(t, token, "result_route_test01"))

	// Then
	assertErrorResponse(t, response, http.StatusInternalServerError, "internal_error")
	entry := decodeSingleLogLine(t, logs.String(), "context fabric investigation result read failed")
	if entry["failure_class"] != "context_fabric_investigation_result" {
		t.Fatalf("failure_class = %v", entry["failure_class"])
	}
	assertDependencyClassFields(t, entry, class)
}
