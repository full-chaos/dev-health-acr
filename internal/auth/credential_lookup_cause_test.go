package auth

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
)

func TestCredentialLookupFailureNamesCauseAndFailsClosed(t *testing.T) {
	connection := fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08006", Class: "connection_failure"})
	cases := []struct {
		name      string
		err       error
		wantLevel string
		wantCause string
		wantClass string
		wantDB    string
	}{
		{"canceled", fmt.Errorf("find credential: %w", context.Canceled), "INFO", "context_canceled", "caller_canceled", "none"},
		{"deadline", fmt.Errorf("find credential: %w", context.DeadlineExceeded), "ERROR", "deadline_exceeded", "credential_store", "none"},
		{"connection", connection, "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"connection exception", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08000", Class: "connection_exception"}), "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"connection does not exist", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08003", Class: "connection_does_not_exist"}), "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"connection failure to establish", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08001", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"connection rejected", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08004", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"transaction resolution unknown", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08007", Class: "unclassified"}), "ERROR", "connection_failure", "credential_store", "connection_exception"},
		{"protocol violation", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "08P01", Class: "unclassified"}), "ERROR", "other", "credential_store", "connection_exception"},
		{"non connection class", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "42501", Class: "insufficient_privilege"}), "ERROR", "other", "credential_store", "access_rule_violation"},
		{"resource exhausted", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "53300", Class: "unclassified"}), "ERROR", "resource_exhausted", "credential_store", "insufficient_resources"},
		{"out of memory", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "53200", Class: "unclassified"}), "ERROR", "resource_exhausted", "credential_store", "insufficient_resources"},
		{"malformed class 53", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "53x", Class: "unclassified"}), "ERROR", "other", "credential_store", "insufficient_resources"},
		{"operator shutdown", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "57P01", Class: "unclassified"}), "ERROR", "server_unavailable", "credential_store", "operator_intervention"},
		{"unmapped class", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "ZZ999", Class: "unclassified"}), "ERROR", "other", "credential_store", "other"},
		{"class without sqlstate", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "5", Class: "unclassified"}), "ERROR", "other", "credential_store", "none"},
		{"crash shutdown", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "57P02", Class: "unclassified"}), "ERROR", "server_unavailable", "credential_store", "operator_intervention"},
		{"cannot connect now", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "57P03", Class: "unclassified"}), "ERROR", "server_unavailable", "credential_store", "operator_intervention"},
		{"statement timeout stays other", fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: "57014", Class: "query_canceled"}), "ERROR", "other", "credential_store", "operator_intervention"},
		{"unavailable without class", fmt.Errorf("find credential: %w", storage.ErrUnavailable), "ERROR", "other", "credential_store", "none"},
		{"other", errors.New("postgres://operator:secret@example"), "ERROR", "other", "credential_store", "none"},
	}
	wantKinds := map[string]string{"canceled": "context_canceled", "deadline": "deadline_exceeded", "other": "unknown", "unavailable without class": "unavailable"}
	for _, tc := range cases {
		if _, ok := wantKinds[tc.name]; !ok {
			wantKinds[tc.name] = "sqlstate"
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &failingCredentialStore{CredentialStore: newMemoryCredentialStore(t), err: tc.err}
			var buf bytes.Buffer
			authenticator, err := NewAuthenticator(store, memory.NewAuditStore(), AuthenticatorOptions{
				Now: func() time.Time { return time.Date(2026, 7, 10, 15, 0, 0, 0, time.UTC) }, Limiter: NoopLimiter{}, Logger: slog.New(slog.NewJSONHandler(&buf, nil)),
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := make([]byte, tokenSecretBytes)
			for i := range raw {
				raw[i] = 42
			}
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.Header.Set("Authorization", "Bearer "+TokenPrefix+base64.RawURLEncoding.EncodeToString(raw))
			request.Header.Set("X-Request-ID", "req_cause")
			response := httptest.NewRecorder()
			authenticator.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Fatal("store fault admitted the request")
			})).ServeHTTP(response, request)
			assertContractError(t, response, http.StatusServiceUnavailable, "upstream_unavailable")
			line := buf.String()
			for _, want := range []string{`"level":"` + tc.wantLevel + `"`, `"cause":"` + tc.wantCause + `"`, `"failure_class":"` + tc.wantClass + `"`, `"db_class":"` + tc.wantDB + `"`, `"request_id":"req_cause"`} {
				if !strings.Contains(line, want) {
					t.Fatalf("log line missing %s: %s", want, line)
				}
			}
			// Exactly one record, at the wanted level: a caller cancel writes
			// the INFO record and no ERROR record beside it.
			var records []map[string]any
			for _, raw := range strings.Split(strings.TrimSpace(line), "\n") {
				var entry map[string]any
				if err := json.Unmarshal([]byte(raw), &entry); err != nil {
					t.Fatalf("log line is not a JSON record %q: %v", raw, err)
				}
				records = append(records, entry)
				if cause, _ := entry["cause"].(string); cause != tc.wantCause {
					t.Fatalf("record cause = %q, want %q: %s", cause, tc.wantCause, raw)
				}
			}
			if len(records) != 1 || records[0]["level"] != tc.wantLevel {
				t.Fatalf("records = %v, want exactly one at %s", records, tc.wantLevel)
			}
			if tc.wantLevel == "INFO" && strings.Contains(line, `"level":"ERROR"`) {
				t.Fatalf("a caller cancel wrote an ERROR record: %s", line)
			}
			if want := wantKinds[tc.name]; !strings.Contains(line, `"error_kind":"`+want+`"`) {
				t.Fatalf("log line missing error_kind %q: %s", want, line)
			}
			if want := credentialLookupSQLState(tc.err); want != "" && !strings.Contains(line, `"sqlstate":"`+want+`"`) {
				t.Fatalf("log line missing sqlstate %s: %s", want, line)
			} else if want == "" && strings.Contains(line, `"sqlstate":`) {
				t.Fatalf("log line carries a sqlstate for an error without one: %s", line)
			}
			if strings.Contains(line, "secret") {
				t.Fatalf("raw error text leaked: %s", line)
			}
		})
	}
}

func TestCredentialLookupErrorKindAndSQLStateAreClosedTokens(t *testing.T) {
	withClass := func(state string) error {
		return fmt.Errorf("find credential: %w: %w", storage.ErrUnavailable, &storage.DependencyErrorClass{SQLState: state, Class: "unclassified"})
	}
	cases := []struct {
		name      string
		err       error
		wantKind  string
		wantState string
	}{
		{"canceled", fmt.Errorf("x: %w", context.Canceled), "context_canceled", ""},
		{"deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), "deadline_exceeded", ""},
		{"sqlstate", withClass("53300"), "sqlstate", "53300"},
		{"sqlstate with letters", withClass("57P01"), "sqlstate", "57P01"},
		{"sqlstate too short", withClass("5"), "sqlstate", ""},
		{"sqlstate four characters", withClass("57P0"), "sqlstate", ""},
		{"sqlstate six characters", withClass("57P011"), "sqlstate", ""},
		{"sqlstate lower case", withClass("57p01"), "sqlstate", ""},
		{"sqlstate with free text", withClass("57P01 secret"), "sqlstate", ""},
		{"network", fmt.Errorf("x: %w: %w", storage.ErrUnavailable, &net.OpError{Op: "read", Err: errors.New("reset")}), "network", ""},
		{"plain eof", fmt.Errorf("x: %w: %w", storage.ErrUnavailable, io.EOF), "eof", ""},
		{"eof", fmt.Errorf("x: %w: %w", storage.ErrUnavailable, io.ErrUnexpectedEOF), "eof", ""},
		{"tls", fmt.Errorf("x: %w: %w", storage.ErrUnavailable, tls.RecordHeaderError{Msg: "bad"}), "tls", ""},
		{"bad conn", fmt.Errorf("x: %w", driver.ErrBadConn), "bad_conn", ""},
		{"conn done", fmt.Errorf("x: %w", sql.ErrConnDone), "conn_done", ""},
		{"unavailable without detail", fmt.Errorf("x: %w", storage.ErrUnavailable), "unavailable", ""},
		{"unknown", errors.New("postgres://operator:secret@example"), "unknown", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := credentialLookupErrorKind(tc.err); got != tc.wantKind || got == "" {
				t.Fatalf("error kind = %q, want %q", got, tc.wantKind)
			}
			if got := credentialLookupSQLState(tc.err); got != tc.wantState {
				t.Fatalf("sqlstate = %q, want %q", got, tc.wantState)
			}
		})
	}
}
