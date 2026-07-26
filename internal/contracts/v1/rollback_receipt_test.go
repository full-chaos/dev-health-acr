package v1

import (
	"testing"
	"time"
)

func TestCredentialRevokeRequest_acceptsValidRollbackReceipt(t *testing.T) {
	// Given
	request := CredentialRevokeRequest{
		SchemaVersion: CredentialRevokeRequestSchema,
		RollbackReceipt: &CredentialRotationReceipt{
			SourceCredentialID:      "credential-source",
			ReplacementCredentialID: "credential-successor",
			RollbackUntil:           time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		},
	}

	// When
	err := request.Validate()

	// Then
	if err != nil {
		t.Fatalf("validate rollback request: %v", err)
	}
}
