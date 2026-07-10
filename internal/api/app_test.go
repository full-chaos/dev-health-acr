package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

type evidenceRows struct{}

func (evidenceRows) ResolveEvidenceScope(context.Context, contextpacket.ReadPlan) (contractsv1.ResolvedScope, error) {
	return contractsv1.ResolvedScope{}, nil
}

func (evidenceRows) EvidenceRows(context.Context, contextpacket.ReadPlan) ([]contractsv1.EvidenceRef, []contractsv1.SourceWatermark, []contractsv1.UnavailableSource, error) {
	return nil, nil, nil, nil
}

func testLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func testApp(t *testing.T, checks ...ReadinessCheck) *App {
	t.Helper()
	fixedNow := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	app, err := NewApp(AppConfig{
		ServiceName:    "dev-health-acr",
		ServiceVersion: "test",
		RequestTimeout: time.Second,
	}, Dependencies{
		Capabilities: StaticCapabilitiesProvider{
			Now: func() time.Time { return fixedNow },
			Value: contractsv1.Capabilities{
				SchemaVersion:         contractsv1.CapabilitiesSchema,
				Service:               "dev-health-acr",
				ServiceVersion:        "test",
				MinimumSidecarVersion: "0.1.0",
			},
		},
		ReadinessChecks: checks,
		Now:             func() time.Time { return fixedNow },
		RequestID:       func() string { return "req_generated" },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestHealth(t *testing.T) {
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") != "req_generated" {
		t.Fatalf("missing generated request id: %q", response.Header().Get("X-Request-ID"))
	}
}

func TestNewEvidenceStore_uses_injected_factory(t *testing.T) {
	codec, err := contextpacket.NewEvidenceIDCodec(contextpacket.EvidenceIDKeyring{ActiveKID: "test", Keys: map[string][]byte{"test": []byte("01234567890123456789012345678901")}})
	if err != nil {
		t.Fatal(err)
	}
	app := testApp(t)
	app.evidenceStoreFactory = contextpacket.NewEvidenceStoreFactory(codec)
	store, err := app.NewEvidenceStore(evidenceRows{})
	if err != nil || store == nil {
		t.Fatalf("evidence store = %#v, error = %v", store, err)
	}
}

func TestRequestIDIsPropagated(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("X-Request-ID", "caller-request")
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	if got := response.Header().Get("X-Request-ID"); got != "caller-request" {
		t.Fatalf("unexpected request ID: %q", got)
	}
}

func TestReadinessFailureIsSafe(t *testing.T) {
	app := testApp(t, CheckFunc{CheckName: "clickhouse", Fn: func(context.Context) error {
		return errors.New("clickhouse://user:super-secret@example")
	}})
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	if strings.Contains(response.Body.String(), "super-secret") {
		t.Fatal("readiness response leaked dependency details")
	}
	var body readinessResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "not_ready" || len(body.Checks) != 1 || body.Checks[0].Name != "clickhouse" {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestCapabilitiesShape(t *testing.T) {
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var capabilities contractsv1.Capabilities
	if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.SchemaVersion != contractsv1.CapabilitiesSchema {
		t.Fatalf("unexpected schema: %s", capabilities.SchemaVersion)
	}
}

func TestCapabilitiesFailureUsesContractError(t *testing.T) {
	provider := failingCapabilitiesProvider{}
	app, err := NewApp(AppConfig{ServiceName: "acr", ServiceVersion: "test", RequestTimeout: time.Second}, Dependencies{
		Capabilities: provider,
		RequestID:    func() string { return "req_failure" },
	}, testLogger(&bytes.Buffer{}))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/agent-context/capabilities", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status: %d", response.Code)
	}
	var envelope contractsv1.ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RequestID != "req_failure" || envelope.Error.Code != "upstream_unavailable" {
		t.Fatalf("unexpected envelope: %#v", envelope)
	}
}

type failingCapabilitiesProvider struct{}

func (failingCapabilitiesProvider) Capabilities(context.Context, *http.Request) (contractsv1.Capabilities, error) {
	return contractsv1.Capabilities{}, errors.New("unavailable")
}

func TestRequestIDIsAvailableToDownstreamMiddleware(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()
	testApp(t).Handler().ServeHTTP(response, request)
	if request.Header.Get("X-Request-ID") != "req_generated" {
		t.Fatalf("downstream request header did not receive request ID: %q", request.Header.Get("X-Request-ID"))
	}
	if response.Header().Get("X-Request-ID") != "req_generated" {
		t.Fatalf("response header did not receive request ID: %q", response.Header().Get("X-Request-ID"))
	}
}
