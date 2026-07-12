package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

func TestClientMapsRateLimitedErrorWithRetryAfter(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		writeJSONFixture(t, w, http.StatusTooManyRequests, contractsv1.ErrorEnvelope{
			SchemaVersion: contractsv1.ErrorSchema,
			RequestID:     "req_server",
			Error: contractsv1.ErrorDetail{
				Code: "rate_limited", Message: "Request rate limit exceeded",
				HTTPStatus: http.StatusTooManyRequests, Retryable: true,
			},
		})
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected an *APIError, got %v (%T)", err, err)
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatal("expected errors.Is to match ErrRateLimited")
	}
	if apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("unexpected retry-after: %s", apiErr.RetryAfter)
	}
	if strings.Contains(err.Error(), testBearerCanary) {
		t.Fatal("error string must never contain the bearer token")
	}
}

func TestClientMapsForbiddenAndUnauthorizedErrors(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusUnauthorized, "invalid_token", ErrInvalidToken},
		{http.StatusForbidden, "repo_forbidden", ErrRepositoryForbidden},
		{http.StatusForbidden, "feature_not_enabled", ErrFeatureNotEnabled},
		{http.StatusNotFound, "not_found", ErrNotFound},
	}
	for _, tc := range cases {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSONFixture(t, w, tc.status, contractsv1.ErrorEnvelope{
				SchemaVersion: contractsv1.ErrorSchema,
				RequestID:     "req_denied_case",
				Error:         contractsv1.ErrorDetail{Code: tc.code, Message: "denied", HTTPStatus: tc.status},
			})
		}))
		client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		_, err = client.Capabilities(context.Background())
		if !errors.Is(err, tc.want) {
			t.Errorf("code=%s: expected errors.Is to match %v, got %v", tc.code, tc.want, err)
		}
		server.Close()
	}
}

func TestClientRefusesToFollowRedirects(t *testing.T) {
	var evilCalls atomic.Int32
	evilServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		evilCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer evilServer.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evilServer.URL+"/steal-bearer", http.StatusFound)
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	if !errors.Is(err, ErrUnexpectedRedirect) {
		t.Fatalf("expected ErrUnexpectedRedirect, got %v", err)
	}
	if evilCalls.Load() != 0 {
		t.Fatal("the client followed a redirect to a different origin, which would have forwarded the bearer token")
	}
}

// isExpectedEarlyCloseError reports whether err is the benign, expected
// result of the deliberately oversized response in
// TestClientResponseTooLargeIsRejected: readLimited (api_client_transport.go)
// makes the client stop reading, and close the connection, as soon as it
// has seen more bytes than MaxResponseBytes -- deliberately before the
// handler below finishes flushing the rest of the oversized fixture. That
// races the handler's write against the client's close; on typical fast
// loopback the write usually wins, but there is no guarantee, and on a
// busier or slower loopback (as CI runners regularly are) the write can
// lose, surfacing here as a broken pipe or connection reset. That is the
// scenario under test behaving exactly as designed -- the client's own
// rejection decision (checked below) never depends on this write
// completing -- so it must not fail the test. Any other write error is
// still unexpected and must fail the test.
func isExpectedEarlyCloseError(err error) bool {
	return errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, net.ErrClosed)
}

func TestClientResponseTooLargeIsRejected(t *testing.T) {
	unexpectedWriteErr := make(chan error, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture := validCapabilitiesFixture()
		fixture.Service = strings.Repeat("x", int(minResponseBytes)*2)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(fixture); err != nil && !isExpectedEarlyCloseError(err) {
			select {
			case unexpectedWriteErr <- err:
			default:
			}
		}
	}))
	cfg := newFixtureConfig(t, server)
	cfg.MaxResponseBytes = minResponseBytes
	client, err := NewClient(cfg, fixedCredentialSource(testBearerCanary))
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	// Server.Close blocks until the handler goroutine above has finished
	// (or failed) its write of the deliberately oversized fixture, so
	// unexpectedWriteErr is safe to drain immediately after.
	server.Close()
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
	select {
	case werr := <-unexpectedWriteErr:
		t.Fatalf("server failed to write the oversized fixture for an unexpected reason: %v", werr)
	default:
	}
}

func TestClientRejectsUnknownFieldsInResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schema_version":"capabilities.v1","service":"dev-health-acr","unexpected_field":true}`))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("expected ErrMalformedResponse for an unknown field, got %v", err)
	}
}

func TestClientRejectsTrailingJSONInResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schema_version":"capabilities.v1","service":"dev-health-acr"}{"trailing":true}`))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("expected ErrMalformedResponse for trailing JSON, got %v", err)
	}
}

func TestClientFallsBackToTransportErrorForNonConformingErrorBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html><body>gateway exploded</body></html>"))
	}))
	defer server.Close()
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(testBearerCanary))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("expected ErrMalformedResponse, got %v", err)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatus != http.StatusBadGateway {
			t.Fatalf("unexpected status: %d", apiErr.HTTPStatus)
		}
		if !apiErr.Retryable {
			t.Fatal("a 502 transport failure should be reported retryable")
		}
		if strings.Contains(apiErr.Message, "gateway exploded") {
			t.Fatal("the raw upstream body must never be echoed into the error message")
		}
	}
}

func TestClientRejectsShapeInvalidCredentialBeforeSending(t *testing.T) {
	var handlerCalls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	// A CredentialSource can be any caller-supplied function, not just
	// LoadCredential; the shape guard in call() must catch a license-shaped
	// (or otherwise garbage) value regardless of where it came from.
	client, err := NewClient(newFixtureConfig(t, server), fixedCredentialSource(licenseShapedToken))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Capabilities(context.Background())
	if !errors.Is(err, ErrCredentialShapeInvalid) {
		t.Fatalf("expected ErrCredentialShapeInvalid, got %v", err)
	}
	if handlerCalls != 0 {
		t.Fatal("a shape-invalid credential was sent to the hosted API")
	}
	if strings.Contains(err.Error(), licenseShapedToken) {
		t.Fatal("the rejected credential value must never appear in the error string")
	}
}
