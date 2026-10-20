package entitlements_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/entitlements"
)

func TestClientHasEntitlement_names_the_rejection_reason(t *testing.T) {
	const valid = `"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true`
	for _, tc := range []struct {
		name   string
		status int
		body   string
		reason string
	}{
		{"extra field", http.StatusOK, `{` + valid + `,"future_grant":true}`, "unknown_field"},
		{"wrong schema version", http.StatusOK, `{"schema_version":"acr_entitlement.v2","org_id":"org-1","agent_context_runtime":true}`, "schema"},
		{"other org id", http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-2","agent_context_runtime":true}`, "schema"},
		{"malformed body", http.StatusOK, `{` + valid, "decode"},
		{"missing field", http.StatusOK, `{"schema_version":"acr_entitlement.v1","org_id":"org-1"}`, "decode"},
		{"server error", http.StatusInternalServerError, `{}`, "status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			client := newClient(t, newTLSServer(t, tc.status, tc.body), entitlements.Config{})

			// When
			_, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

			// Then
			if err == nil {
				t.Fatal("HasEntitlement() error = nil, want a rejection")
			}
			if got := entitlements.RejectionReason(err); got != tc.reason {
				t.Fatalf("RejectionReason() = %q, want %q (err %v)", got, tc.reason, err)
			}
		})
	}
}

func TestClientHasEntitlement_names_a_transport_failure(t *testing.T) {
	// Given
	server := newTLSServer(t, http.StatusOK, `{}`)
	client := newClient(t, server, entitlements.Config{})
	server.Close()

	// When
	_, err := client.HasEntitlement(context.Background(), "org-1", "agent_context_runtime")

	// Then
	if err == nil {
		t.Fatal("HasEntitlement() error = nil, want a rejection")
	}
	if got := entitlements.RejectionReason(err); got != "transport" {
		t.Fatalf("RejectionReason() = %q, want transport", got)
	}
}

func TestRejectionReason_is_empty_for_an_unrelated_error(t *testing.T) {
	if got := entitlements.RejectionReason(errors.New("boom")); got != "" {
		t.Fatalf("RejectionReason() = %q, want empty", got)
	}
}
