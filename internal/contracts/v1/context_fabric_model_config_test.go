package v1

import "testing"

func validOrgModelConfigWriteRequest() ContextFabricOrgModelConfigWriteRequest {
	return ContextFabricOrgModelConfigWriteRequest{
		SchemaVersion: ContextFabricOrgModelConfigWriteRequestSchema,
		Provider:      "acme-gateway",
		BaseURL:       "https://llm.acme-gateway.example/v1/",
		Model:         "acme-large",
		FallbackModel: "acme-large-fallback",
		Credential:    "sk-acme-live-a1b2c3d4e5f6wxyz",
	}
}

func TestContextFabricOrgModelConfigWriteRequest_Validate_acceptsAWellFormedRequest(t *testing.T) {
	if err := validOrgModelConfigWriteRequest().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want a well-formed request to pass", err)
	}
}

// TestContextFabricOrgModelConfigWriteRequest_Validate_rejectsCredentialBearingBaseURL
// is the Codex round-1 F1 probe, permanently locked: a base URL is a
// location, never a channel for a secret. All three shapes a caller could
// use to smuggle a credential into the (plaintext, echoed-back-on-GET)
// base_url field must be rejected, each with an error naming the specific
// part that is not allowed.
func TestContextFabricOrgModelConfigWriteRequest_Validate_rejectsCredentialBearingBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
	}{
		{"userinfo", "https://user:secret@host.example/v1/"},
		{"userinfo without password", "https://user@host.example/v1/"},
		{"query", "https://host.example/v1/?api_key=leaked"},
		{"fragment", "https://host.example/v1/#leaked-fragment"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			request := validOrgModelConfigWriteRequest()
			request.BaseURL = c.baseURL
			if err := request.Validate(); err == nil {
				t.Fatalf("Validate() accepted base_url %q, want it rejected", c.baseURL)
			}
		})
	}
}

func TestContextFabricOrgModelConfigWriteRequest_Validate_rejectsInsecureBaseURL(t *testing.T) {
	request := validOrgModelConfigWriteRequest()
	request.BaseURL = "http://llm.acme-gateway.example/v1/"
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() accepted a plaintext http base_url")
	}
}

func TestContextFabricOrgModelConfigWriteRequest_Validate_rejectsMissingCredential(t *testing.T) {
	request := validOrgModelConfigWriteRequest()
	request.Credential = ""
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() accepted a request with no credential")
	}
}

func TestContextFabricOrgModelConfigWriteRequest_Validate_rejectsFallbackModelEqualToModel(t *testing.T) {
	request := validOrgModelConfigWriteRequest()
	request.FallbackModel = request.Model
	if err := request.Validate(); err == nil {
		t.Fatal("Validate() accepted fallback_model equal to model")
	}
}

func TestMaskContextFabricOrgModelCredential_isAFixedMaskPlusAtMostFourTrailingCharacters(t *testing.T) {
	masked := MaskContextFabricOrgModelCredential("sk-acme-live-a1b2c3d4e5f6wxyz")
	if masked == "" {
		t.Fatal("mask is empty")
	}
	if masked == "sk-acme-live-a1b2c3d4e5f6wxyz" {
		t.Fatal("mask equals the plaintext credential")
	}
	if len(masked) < 4 {
		t.Fatalf("mask %q is shorter than the visible suffix", masked)
	}
	suffix := masked[len(masked)-4:]
	if suffix != "wxyz" {
		t.Fatalf("mask suffix = %q, want the last 4 characters of the credential", suffix)
	}
}
