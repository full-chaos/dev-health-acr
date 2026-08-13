package v1

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

// Schema versions for the per-organization BYO LLM configuration surface
// (CHAOS-3775). The write request carries the credential; the read/response
// shape never does -- ContextFabricOrgModelConfig.CredentialMasked is a
// server-computed display value, never the stored secret (AC-3775-4).
const (
	ContextFabricOrgModelConfigSchema             = "context_fabric_org_model_config.v1"
	ContextFabricOrgModelConfigWriteRequestSchema = "context_fabric_org_model_config_write_request.v1"
)

// contextFabricOrgModelConfigMaxFieldLength mirrors
// modelprovider.maximumProviderOrModelLength: the per-org surface must not
// admit a longer provider/model/base_url/credential than the deployment
// default surface does, since both ultimately flow into the same
// modelprovider.Config validation and genkit construction path.
const contextFabricOrgModelConfigMaxFieldLength = 256

// contextFabricOrgModelConfigMaxCredentialLength bounds the stored
// credential. Provider bearer tokens are short in practice; this is a
// generous ceiling to bound encrypted-column size, not a format assumption.
const contextFabricOrgModelConfigMaxCredentialLength = 4096

// ContextFabricOrgModelConfig is the read shape for an organization's BYO
// LLM configuration (AC-3775-1..5). OrgID is populated by the server from
// the authenticated principal; it is never accepted on a request body (TRD
// §19.3.6 "organization scope is structural").
type ContextFabricOrgModelConfig struct {
	SchemaVersion    string    `json:"schema_version"`
	OrgID            string    `json:"org_id"`
	Provider         string    `json:"provider"`
	BaseURL          string    `json:"base_url,omitempty"`
	Model            string    `json:"model"`
	FallbackModel    string    `json:"fallback_model,omitempty"`
	CredentialMasked string    `json:"credential_masked"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// ContextFabricOrgModelConfigWriteRequest is the write (upsert) request
// body. Credential is write-only: it is required on every write (this is a
// full-replace PUT, not a partial patch) and is never echoed back anywhere
// in the response, a log, a receipt, or telemetry (AC-3775-4, AC-3770-5).
type ContextFabricOrgModelConfigWriteRequest struct {
	SchemaVersion string `json:"schema_version"`
	Provider      string `json:"provider"`
	BaseURL       string `json:"base_url,omitempty"`
	Model         string `json:"model"`
	FallbackModel string `json:"fallback_model,omitempty"`
	Credential    string `json:"credential"`
}

// Validate enforces the same vendor-neutral bounds
// modelprovider.Config.validate() enforces on the deployment-default
// surface (TRD §19.3.6: "no field is consulted for an OpenAI-specific
// behavior"), so a per-organization configuration can never admit
// something the deployment-default composition path would reject at
// startup.
func (r ContextFabricOrgModelConfigWriteRequest) Validate() error {
	if r.SchemaVersion != ContextFabricOrgModelConfigWriteRequestSchema {
		return errors.New("context fabric org model config: schema_version is invalid")
	}
	if err := validateContextFabricModelID("provider", r.Provider); err != nil {
		return err
	}
	if strings.ContainsRune(r.Provider, '/') {
		return errors.New("context fabric org model config: provider must not contain a path separator")
	}
	if err := validateContextFabricModelID("model", r.Model); err != nil {
		return err
	}
	if r.FallbackModel != "" {
		if err := validateContextFabricModelID("fallback_model", r.FallbackModel); err != nil {
			return err
		}
		if r.FallbackModel == r.Model {
			return errors.New("context fabric org model config: fallback_model must name a different model than model")
		}
	}
	if err := validateContextFabricOrgModelBaseURL(r.BaseURL); err != nil {
		return err
	}
	credential := strings.TrimSpace(r.Credential)
	if credential == "" {
		return errors.New("context fabric org model config: credential is required")
	}
	if len(credential) > contextFabricOrgModelConfigMaxCredentialLength {
		return errors.New("context fabric org model config: credential is too long")
	}
	return nil
}

func validateContextFabricModelID(field, value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed != value || len(trimmed) > contextFabricOrgModelConfigMaxFieldLength {
		return errors.New("context fabric org model config: " + field + " is invalid")
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasSuffix(trimmed, "/") || strings.Contains(trimmed, "//") {
		return errors.New("context fabric org model config: " + field + " must not contain an empty path segment")
	}
	return nil
}

// validateContextFabricOrgModelBaseURL requires https, and requires the
// value to be nothing more than scheme + host + optional path: no userinfo,
// no query, no fragment. Unlike the deployment-default surface
// (modelprovider.Config.AllowInsecureBaseURL, which exists for a
// co-located BYO server reached over loopback), a customer-entered
// per-organization base URL always leaves ACR's own trust boundary over
// the network, so plaintext http is never permitted here.
//
// Codex round-1 finding F1: url.Parse alone does not reject
// "https://user:secret@host/", "https://host/?api_key=...", or
// "https://host/#secret" -- all three round-trip a caller-supplied
// credential through this field in plaintext (it is stored unencrypted:
// ContextFabricOrgModelConfig.BaseURL is a plain read-back column, not the
// encrypted credential column), and GET echoes it back verbatim. A base URL
// is a location, never a place to smuggle a secret, so userinfo, query, and
// fragment are each rejected outright with their own named error.
func validateContextFabricOrgModelBaseURL(value string) error {
	if value == "" {
		return nil
	}
	if len(value) > contextFabricOrgModelConfigMaxFieldLength {
		return errors.New("context fabric org model config: base_url is too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Scheme != "https" {
		return errors.New("context fabric org model config: base_url must be an absolute https URL")
	}
	if parsed.User != nil {
		return errors.New("context fabric org model config: base_url must not contain userinfo (a username or password)")
	}
	if parsed.RawQuery != "" {
		return errors.New("context fabric org model config: base_url must not contain a query string")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return errors.New("context fabric org model config: base_url must not contain a fragment")
	}
	return nil
}

// MaskContextFabricOrgModelCredential renders a display-only masked form of
// a stored credential (AC-3775-4: "reading it back through the product
// returns a masked value"). It never returns enough of the credential to
// reconstruct it -- at most the last 4 characters -- and is the ONLY form a
// credential may take outside the encrypted storage column.
func MaskContextFabricOrgModelCredential(credential string) string {
	trimmed := strings.TrimSpace(credential)
	if trimmed == "" {
		return ""
	}
	const visible = 4
	if len(trimmed) <= visible {
		return strings.Repeat("*", len(trimmed))
	}
	return strings.Repeat("*", 8) + trimmed[len(trimmed)-visible:]
}
