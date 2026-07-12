package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoctorReportsReachableCanonicalVersionMismatch(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"schema_version":"error.v1","request_id":"req_0123456789abcdef0123456789abcdef","error":{"code":"version_mismatch","message":"unsupported","http_status":426,"retryable":false,"details":{"minimum_client_version":"9.0.0"}}}`))
	}))
	t.Cleanup(server.Close)
	configureDoctorServer(t, server)

	// When
	report := runDoctorLive()

	// Then
	if report.LiveCheck == nil || !report.LiveCheck.Reachable || report.Status != "live_check_incompatible" || !strings.Contains(report.LiveCheck.Detail, "9.0.0") {
		t.Fatalf("doctor report = %#v", report)
	}
}

func TestDoctorTreatsMalformedHTTPErrorAsUnreachable(t *testing.T) {
	// Given
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"schema_version":"error.v1","error":{"code":"version_mismatch"}}`))
	}))
	t.Cleanup(server.Close)
	configureDoctorServer(t, server)

	// When
	report := runDoctorLive()

	// Then
	if report.LiveCheck == nil || report.LiveCheck.Reachable || report.Status != "live_check_unreachable" {
		t.Fatalf("doctor report = %#v", report)
	}
	if strings.Contains(report.LiveCheck.Detail, server.URL) || !strings.Contains(report.LiveCheck.Detail, "non-conforming 426 response") {
		t.Fatalf("live detail = %q", report.LiveCheck.Detail)
	}
}
