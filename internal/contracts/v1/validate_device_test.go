package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contractcheck"
)

func TestDeviceTokenRequestValidate_rejects_non_RFC_grant_type(t *testing.T) {
	request := DeviceTokenRequest{SchemaVersion: DeviceTokenRequestSchema, GrantType: "authorization_code", DeviceCode: "0123456789abcdefghijklmnopqrstuv"}
	if err := request.Validate(); err == nil {
		t.Fatal("device token validator accepted a non-device grant")
	}
}

func TestDeviceAuthorizationRequestValidate_rejectsPresentNullOrEmptyHints(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"device_authorization_request.v1","organization_id_hint":null}`,
		`{"schema_version":"device_authorization_request.v1","organization_id_hint":""}`,
		`{"schema_version":"device_authorization_request.v1","repository_hints":null}`,
		`{"schema_version":"device_authorization_request.v1","repository_hints":[]}`,
	} {
		var request DeviceAuthorizationRequest
		if err := json.Unmarshal([]byte(body), &request); err != nil {
			t.Fatal(err)
		}
		if err := request.Validate(); err == nil {
			t.Fatalf("validator accepted malformed present hint %s", body)
		}
	}
}

func TestDeviceAuthorizationRequestDecode_rejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"device_authorization_request.v1","unexpected":true}`,
		`{"schema_version":"device_authorization_request.v1"} {}`,
	} {
		var request DeviceAuthorizationRequest
		if err := request.UnmarshalJSON([]byte(body)); err == nil {
			t.Fatalf("custom decoder accepted malformed body %s", body)
		}
	}
}

func TestDeviceApprovalPreviewRequestDecode_rejectsRepositoryScopes(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"device_approval_preview_request.v1","user_code":"ABCDEFGH","repository_scopes":null}`,
		`{"schema_version":"device_approval_preview_request.v1","user_code":"ABCDEFGH","repository_scopes":["example-org/widget-service"]}`,
	} {
		var request DeviceApprovalRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("decoder accepted preview-only request with repository scopes %s", body)
		}
	}

	var preview DeviceApprovalRequest
	if err := json.Unmarshal([]byte(`{"schema_version":"device_approval_preview_request.v1","user_code":"ABCDEFGH"}`), &preview); err != nil {
		t.Fatalf("decoder rejected valid preview request: %v", err)
	}
}

func TestDeviceAuthorizationRequestValidate_rejectsTypedNilRepositoryHints(t *testing.T) {
	var nilHints []string
	request := DeviceAuthorizationRequest{
		SchemaVersion: DeviceAuthorizationRequestSchema, RepositoryHints: &nilHints,
	}
	if err := request.Validate(); err == nil {
		t.Fatal("validator accepted typed nil repository hints")
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := contractcheck.ValidateSerialized("", "device_authorization_request.v1.schema.json", encoded); err == nil {
		t.Fatal("schema accepted typed nil repository hints")
	}
}

func TestDeviceAuthorizationRequestValidate_countsHintRunesLikeSchema(t *testing.T) {
	valid := strings.Repeat("界", 128)
	invalid := strings.Repeat("界", 129)
	for _, organizationIDHint := range []string{valid, invalid} {
		body, err := json.Marshal(map[string]any{
			"schema_version": "device_authorization_request.v1", "organization_id_hint": organizationIDHint,
		})
		if err != nil {
			t.Fatal(err)
		}
		var request DeviceAuthorizationRequest
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if got := request.Validate() == nil; got != (organizationIDHint == valid) {
			t.Fatalf("hint of %d runes accepted = %t", len([]rune(organizationIDHint)), got)
		}
	}
}

func TestDeviceTokenResponseValidate_accepts_fixed_30_day_credential(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(deviceCredentialLifetime)
	response := DeviceTokenResponse{
		SchemaVersion: DeviceTokenResponseSchema,
		AccessToken:   "[REDACTED]",
		TokenType:     "Bearer",
		ExpiresIn:     int(deviceCredentialLifetime.Seconds()),
		Credential: ClientCredential{
			SchemaVersion:    ClientCredentialSchema,
			CredentialID:     "credential-0001",
			Name:             "MCP sidecar",
			TokenPrefix:      "fcacr_abcd1234",
			OrgID:            "org_fullchaos",
			RepositoryScopes: []string{"full-chaos/dev-health-acr"},
			Scopes:           []string{"context:read", "evidence:read"},
			CreatedAt:        createdAt,
			ExpiresAt:        &expiresAt,
		},
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("device token validator rejected fixed device credential: %v", err)
	}
	assertSchemaParity(t, "device_token_response.v1.schema.json", response)
}

func TestDeviceApprovalRequestValidate_rejects_wildcard_scope(t *testing.T) {
	tests := []string{"*/repo", "*/dev-health", "org/re*po", "o*rg/repo", "full-chaos/*"}
	for _, scope := range tests {
		t.Run(scope, func(t *testing.T) {
			request := DeviceApprovalRequest{SchemaVersion: DeviceApprovalRequestSchema, UserCode: "ABCDEFGH", RepositoryScopes: []string{scope}}
			if err := request.Validate(); err == nil {
				t.Fatal("device approval validator accepted a wildcard repository grant")
			}
			encoded := []byte(`{"schema_version":"device_approval_request.v1","user_code":"ABCDEFGH","repository_scopes":["` + scope + `"]}`)
			if err := contractcheck.ValidateSerialized("", "device_approval_request.v1.schema.json", encoded); err == nil {
				t.Fatal("schema accepted a wildcard repository grant")
			}
		})
	}
}

func TestOAuthDeviceErrorResponseValidate_rejects_non_RFC_error(t *testing.T) {
	response := OAuthDeviceErrorResponse{SchemaVersion: OAuthDeviceErrorSchema, Error: "invalid_request"}
	if err := response.Validate(); err == nil {
		t.Fatal("OAuth device error validator accepted an unpinned error")
	}
}

func TestDeviceContractValidators_match_schema(t *testing.T) {
	createdAt := time.Date(2026, time.July, 25, 0, 0, 0, 0, time.UTC)
	expiresAt := createdAt.Add(deviceCredentialLifetime)
	revokedAt := createdAt.Add(time.Hour)
	credential := ClientCredential{
		SchemaVersion:    ClientCredentialSchema,
		CredentialID:     "credential-0001",
		Name:             "MCP sidecar",
		TokenPrefix:      "fcacr_abcd1234",
		OrgID:            "org_fullchaos",
		RepositoryScopes: []string{"full-chaos/dev-health-acr"},
		Scopes:           []string{"context:read", "evidence:read"},
		CreatedAt:        createdAt,
		ExpiresAt:        &expiresAt,
	}
	tests := []struct {
		schema string
		value  interface{ Validate() error }
	}{
		{schema: "device_authorization_request.v1.schema.json", value: deviceAuthorizationRequestWithHints("org_fullchaos", []string{"full-chaos/dev-health-acr"})},
		{schema: "device_authorization_response.v1.schema.json", value: DeviceAuthorizationResponse{SchemaVersion: DeviceAuthorizationResponseSchema, DeviceCode: "0123456789abcdefghijklmnopqrstuv", UserCode: "ABCDEFGH", VerificationURI: "https://web.fullchaos.dev/acr/device", ExpiresIn: 600, Interval: 5}},
		{schema: "device_token_request.v1.schema.json", value: DeviceTokenRequest{SchemaVersion: DeviceTokenRequestSchema, GrantType: DeviceCodeGrantType, DeviceCode: "0123456789abcdefghijklmnopqrstuv"}},
		{schema: "device_approval_request.v1.schema.json", value: DeviceApprovalRequest{SchemaVersion: DeviceApprovalRequestSchema, UserCode: "ABCDEFGH", RepositoryScopes: []string{"full-chaos/dev-health-acr"}}},
		{schema: "device_approval_response.v1.schema.json", value: DeviceApprovalResponse{SchemaVersion: DeviceApprovalResponseSchema, Status: "approved"}},
		{schema: "device_approval_preview_request.v1.schema.json", value: DeviceApprovalPreviewRequest{SchemaVersion: DeviceApprovalPreviewRequestSchema, UserCode: "ABCDEFGH"}},
		{schema: "device_approval_preview_response.v1.schema.json", value: DeviceApprovalPreviewResponse{SchemaVersion: DeviceApprovalPreviewResponseSchema, OrganizationIDHint: "org_fullchaos", RepositoryHints: []string{"full-chaos/dev-health-acr"}}},
		{schema: "credential_rotate_request.v1.schema.json", value: CredentialRotateRequest{SchemaVersion: CredentialRotateRequestSchema}},
		{schema: "credential_rotate_response.v1.schema.json", value: CredentialRotateResponse{SchemaVersion: CredentialRotateResponseSchema, AccessToken: "[REDACTED]", Credential: credential, Receipt: CredentialRotationReceipt{SourceCredentialID: "credential-0001", ReplacementCredentialID: "credential-0002", RollbackUntil: createdAt.Add(15 * time.Minute)}}},
		{schema: "credential_revoke_request.v1.schema.json", value: CredentialRevokeRequest{SchemaVersion: CredentialRevokeRequestSchema}},
		{schema: "oauth_device_error.v1.schema.json", value: OAuthDeviceErrorResponse{SchemaVersion: OAuthDeviceErrorSchema, Error: OAuthDeviceErrorAuthorizationPending}},
	}
	for _, test := range tests {
		t.Run(test.schema, func(t *testing.T) {
			if err := test.value.Validate(); err != nil {
				t.Fatalf("validator rejected schema-valid value: %v", err)
			}
			assertSchemaParity(t, test.schema, test.value)
		})
	}
	credential.RevokedAt = &revokedAt
	revocation := CredentialRevokeResponse{SchemaVersion: CredentialRevokeResponseSchema, Credential: credential}
	if err := revocation.Validate(); err != nil {
		t.Fatalf("revocation validator rejected schema-valid value: %v", err)
	}
	assertSchemaParity(t, "credential_revoke_response.v1.schema.json", revocation)
}

func deviceAuthorizationRequestWithHints(organizationIDHint string, repositoryHints []string) DeviceAuthorizationRequest {
	return DeviceAuthorizationRequest{
		SchemaVersion: DeviceAuthorizationRequestSchema, OrganizationIDHint: &organizationIDHint, RepositoryHints: &repositoryHints,
	}
}

func TestDeviceContractFixtures_validate(t *testing.T) {
	tests := []struct {
		name  string
		value interface{ Validate() error }
	}{
		{name: "device authorization request", value: loadFixture[DeviceAuthorizationRequest](t, "device_authorization_request.v1.json")},
		{name: "device authorization response", value: loadFixture[DeviceAuthorizationResponse](t, "device_authorization_response.v1.json")},
		{name: "device token request", value: loadFixture[DeviceTokenRequest](t, "device_token_request.v1.json")},
		{name: "device token response", value: loadFixture[DeviceTokenResponse](t, "device_token_response.v1.json")},
		{name: "device approval request", value: loadFixture[DeviceApprovalRequest](t, "device_approval_request.v1.json")},
		{name: "device approval response", value: loadFixture[DeviceApprovalResponse](t, "device_approval_response.v1.json")},
		{name: "device approval preview request", value: loadFixture[DeviceApprovalPreviewRequest](t, "device_approval_preview_request.v1.json")},
		{name: "device approval preview response", value: loadFixture[DeviceApprovalPreviewResponse](t, "device_approval_preview_response.v1.json")},
		{name: "credential rotate request", value: loadFixture[CredentialRotateRequest](t, "credential_rotate_request.v1.json")},
		{name: "credential rotate response", value: loadFixture[CredentialRotateResponse](t, "credential_rotate_response.v1.json")},
		{name: "credential revoke request", value: loadFixture[CredentialRevokeRequest](t, "credential_revoke_request.v1.json")},
		{name: "credential revoke response", value: loadFixture[CredentialRevokeResponse](t, "credential_revoke_response.v1.json")},
		{name: "OAuth device error", value: loadFixture[OAuthDeviceErrorResponse](t, "oauth_device_error.v1.json")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.value.Validate(); err != nil {
				t.Fatalf("fixture violates semantic contract: %v", err)
			}
		})
	}
}
