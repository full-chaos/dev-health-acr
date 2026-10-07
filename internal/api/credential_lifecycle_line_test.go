package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/auth"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/eventspec/certify"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/oauthvocab"
)

// Each self-credential request writes exactly one certified oauth step line,
// with the step of its route and the outcome its answer means.
func TestSelfCredentialRoutesWriteOneCertifiedOAuthStepLine(t *testing.T) {
	rotate := func(t *testing.T, token string) *http.Request {
		request := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/rotate", contractsv1.CredentialRotateRequest{SchemaVersion: contractsv1.CredentialRotateRequestSchema})
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request
	}
	revoke := func(t *testing.T, token string) *http.Request {
		request := deviceRequest(t, http.MethodPost, "/api/v1/auth/credentials/self/revoke", contractsv1.CredentialRevokeRequest{SchemaVersion: contractsv1.CredentialRevokeRequestSchema})
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		return request
	}
	malformed := func(path string) func(*testing.T, string) *http.Request {
		return func(t *testing.T, token string) *http.Request {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{not json"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token)
			return request
		}
	}
	cells := []struct {
		name    string
		build   func(*testing.T, string) *http.Request
		bearer  bool
		step    string
		outcome string
		status  int
	}{
		{"rotate_ok", rotate, true, oauthvocab.StepCredentialRotate, oauthvocab.OutcomeOK, http.StatusOK},
		{"revoke_ok", revoke, true, oauthvocab.StepCredentialRevoke, oauthvocab.OutcomeOK, http.StatusOK},
		{"ack_nothing_awaiting", func(t *testing.T, token string) *http.Request { return ackRequest(t, token, "") }, true, oauthvocab.StepCredentialAck, oauthvocab.OutcomeInvalidGrant, http.StatusNotFound},
		{"rotate_no_bearer", rotate, false, oauthvocab.StepCredentialRotate, oauthvocab.OutcomeUnauthenticated, http.StatusUnauthorized},
		{"revoke_no_bearer", revoke, false, oauthvocab.StepCredentialRevoke, oauthvocab.OutcomeUnauthenticated, http.StatusUnauthorized},
		{"ack_no_bearer", func(t *testing.T, token string) *http.Request { return ackRequest(t, token, "") }, false, oauthvocab.StepCredentialAck, oauthvocab.OutcomeUnauthenticated, http.StatusUnauthorized},
		{"rotate_malformed", malformed("/api/v1/auth/credentials/self/rotate"), true, oauthvocab.StepCredentialRotate, oauthvocab.OutcomeInvalidRequest, http.StatusBadRequest},
	}
	for _, cell := range cells {
		t.Run(cell.name, func(t *testing.T) {
			app, token := newHostedTestApp(t, nil, nil, []string{auth.ScopeContextRead, auth.ScopeEvidenceRead}, nil, nil)
			logs := &bytes.Buffer{}
			app.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
			bearer := ""
			if cell.bearer {
				bearer = token
			}
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, cell.build(t, bearer))
			if response.Code != cell.status {
				t.Fatalf("status %d, want %d; body=%s", response.Code, cell.status, response.Body.String())
			}
			parsed, err := certify.Parse(logs.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			lines := parsed.LinesWithMsg(eventspec.OAuthStepLogMessage)
			if len(lines) != 1 {
				t.Fatalf("%d oauth step lines, want exactly 1; logs=%s", len(lines), logs.String())
			}
			if _, err := certify.Certify(parsed, certify.Assertion{Event: eventspec.OAuthStep, Want: map[string]any{
				"request_id": lines[0]["request_id"], "step": cell.step, "outcome": cell.outcome, "client_kind": oauthvocab.ClientKindNone, "status": cell.status,
			}}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The acknowledgement window closing is named by the handler: its 409 alone
// reads as a state conflict.
func TestCredentialLifecycleLineTakesTheOutcomeAHandlerNames(t *testing.T) {
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	logs := &bytes.Buffer{}
	app.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := app.credentialLifecycleLine(oauthvocab.StepCredentialAck, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setCredentialLifecycleOutcome(r, oauthvocab.OutcomeExpired)
		w.WriteHeader(http.StatusConflict)
	}))
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/credentials/self/ack", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if !strings.Contains(logs.String(), `"outcome":"expired"`) || !strings.Contains(logs.String(), `"step":"credential_ack"`) {
		t.Fatalf("line %s, want step credential_ack outcome expired", logs.String())
	}
}

// A handler that panics still writes its one line, as the 500 the recovery
// middleware answers with, and the panic continues to that middleware.
func TestCredentialLifecycleLineSurvivesAPanic(t *testing.T) {
	app, _ := newHostedTestApp(t, nil, nil, nil, nil, nil)
	logs := &bytes.Buffer{}
	app.logger = slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handler := app.credentialLifecycleLine(oauthvocab.StepCredentialRotate, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	defer func() {
		if recover() == nil {
			t.Fatal("the panic did not continue to the recovery middleware")
		}
		if strings.Count(logs.String(), `"msg":"acr-api oauth step"`) != 1 || !strings.Contains(logs.String(), `"outcome":"unavailable"`) || !strings.Contains(logs.String(), `"status":500`) {
			t.Fatalf("lines %s, want one step line with outcome unavailable status 500", logs.String())
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/auth/credentials/self/rotate", nil))
}
