package contractcheck

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenAPI_includes_canonical_device_and_credential_operations(t *testing.T) {
	// Given
	root, err := findRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	value, err := decodeJSONFile(filepath.Join(root, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	document, ok := value.(map[string]any)
	if !ok {
		t.Fatal("OpenAPI document is not an object")
	}
	paths, ok := document["paths"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI paths are not an object")
	}
	want := map[string]string{
		"/api/v1/oauth/device_authorization":   "createDeviceAuthorization",
		"/api/v1/oauth/token":                  "exchangeDeviceToken",
		"/api/v1/oauth/device_approval":        "approveDeviceAuthorization",
		"/api/v1/auth/credentials/self/rotate": "rotateOwnCredential",
		"/api/v1/auth/credentials/self/revoke": "revokeOwnCredential",
	}

	// When / Then
	for path, operationID := range want {
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("missing OpenAPI path %s", path)
		}
		post, ok := item["post"].(map[string]any)
		if !ok {
			t.Fatalf("%s lacks a POST operation", path)
		}
		if got, _ := post["operationId"].(string); got != operationID {
			t.Fatalf("%s operationId = %q, want %q", path, got, operationID)
		}
	}
	raw, err := os.ReadFile(filepath.Join(root, "contracts", "openapi", "acr-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("verification_uri_complete")) {
		t.Fatal("OpenAPI must not expose verification_uri_complete")
	}
}

func TestDeviceContractSchemas_reject_malformed_payloads(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		body   []byte
	}{
		{name: "wrong grant", schema: "device_token_request.v1.schema.json", body: []byte(`{"schema_version":"device_token_request.v1","grant_type":"authorization_code","device_code":"0123456789abcdefghijklmnopqrstuv"}`)},
		{name: "wildcard approval", schema: "device_approval_request.v1.schema.json", body: []byte(`{"schema_version":"device_approval_request.v1","user_code":"ABCDEFGH","repository_scopes":["full-chaos/*"]}`)},
		{name: "client supplied organization", schema: "device_approval_request.v1.schema.json", body: []byte(`{"schema_version":"device_approval_request.v1","user_code":"ABCDEFGH","org_id":"org_fullchaos","repository_scopes":["full-chaos/dev-health-acr"]}`)},
		{name: "unrecognized error", schema: "oauth_device_error.v1.schema.json", body: []byte(`{"schema_version":"oauth_device_error.v1","error":"invalid_request"}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateSerialized("", test.schema, test.body); err == nil {
				t.Fatalf("%s accepted malformed payload", test.schema)
			}
		})
	}
}
