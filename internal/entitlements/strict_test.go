package entitlements

import (
	"strings"
	"testing"
)

func TestDecodeResponse_rejects_missing_and_duplicate_contract_fields(t *testing.T) {
	for _, payload := range []string{
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1"}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":true,"agent_context_runtime":false}`,
	} {
		// Given
		body := strings.NewReader(payload)

		// When
		_, err := decodeResponse(body, 1024)

		// Then
		if err == nil {
			t.Fatalf("decodeResponse(%q) error = nil; want strict contract rejection", payload)
		}
	}
}

func TestDecodeResponse_rejects_null_contract_fields(t *testing.T) {
	for _, payload := range []string{
		`{"schema_version":null,"org_id":"org-1","agent_context_runtime":true}`,
		`{"schema_version":"acr_entitlement.v1","org_id":null,"agent_context_runtime":true}`,
		`{"schema_version":"acr_entitlement.v1","org_id":"org-1","agent_context_runtime":null}`,
	} {
		t.Run(payload, func(t *testing.T) {
			// Given
			body := strings.NewReader(payload)

			// When
			_, err := decodeResponse(body, 1024)

			// Then
			if err == nil {
				t.Fatalf("decodeResponse(%q) error = nil; want null rejection", payload)
			}
		})
	}
}

func TestDecodeResponse_rejects_well_formed_body_over_limit(t *testing.T) {
	// Given
	payload := `{"schema_version":"acr_entitlement.v1","org_id":"` + strings.Repeat("x", 1024) + `","agent_context_runtime":true}`

	// When
	_, err := decodeResponse(strings.NewReader(payload), 128)

	// Then
	if err == nil {
		t.Fatal("decodeResponse() error = nil; want bounded body rejection")
	}
}
