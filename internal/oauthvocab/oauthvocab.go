// Package oauthvocab holds the closed vocabularies of acr-api's OAuth login
// telemetry. It imports nothing so that both the producer (internal/auth,
// internal/api) and the declaration (internal/contextfabric/eventspec) read
// the one list.
package oauthvocab

// OAuth steps.
const (
	StepRegister  = "register"
	StepAuthorize = "authorize"
	StepConsent   = "consent"
	StepToken     = "token"
)

// StepVocabulary lists every step.
func StepVocabulary() []string {
	return []string{StepRegister, StepAuthorize, StepConsent, StepToken}
}

// OAuth step outcomes. "ok" means the step did what it exists to do: a client
// was registered, a consent page was served, a code was issued, a credential
// was minted. Every other member names the one reason the step stopped.
const (
	OutcomeOK                      = "ok"
	OutcomePending                 = "pending"
	OutcomeAccessDenied            = "access_denied"
	OutcomeExpired                 = "expired"
	OutcomeAlreadyCompleted        = "already_completed"
	OutcomeInvalidRequest          = "invalid_request"
	OutcomeInvalidClient           = "invalid_client"
	OutcomeInvalidClientMetadata   = "invalid_client_metadata"
	OutcomeInvalidRedirectURI      = "invalid_redirect_uri"
	OutcomeUnsupportedResponseType = "unsupported_response_type"
	OutcomeUnsupportedGrantType    = "unsupported_grant_type"
	OutcomePKCERequired            = "pkce_required"
	OutcomeInvalidTarget           = "invalid_target"
	OutcomeInvalidGrant            = "invalid_grant"
	OutcomePKCEMismatch            = "pkce_mismatch"
	OutcomeRedirectMismatch        = "redirect_mismatch"
	OutcomeClientMismatch          = "client_mismatch"
	OutcomeResourceMismatch        = "resource_mismatch"
	OutcomeRateLimited             = "rate_limited"
	OutcomeUnavailable             = "unavailable"
)

// OutcomeVocabulary lists every outcome, ok first.
func OutcomeVocabulary() []string {
	return []string{
		OutcomeOK, OutcomePending, OutcomeAccessDenied, OutcomeExpired, OutcomeAlreadyCompleted,
		OutcomeInvalidRequest, OutcomeInvalidClient, OutcomeInvalidClientMetadata, OutcomeInvalidRedirectURI,
		OutcomeUnsupportedResponseType, OutcomeUnsupportedGrantType, OutcomePKCERequired, OutcomeInvalidTarget,
		OutcomeInvalidGrant, OutcomePKCEMismatch, OutcomeRedirectMismatch, OutcomeClientMismatch,
		OutcomeResourceMismatch, OutcomeRateLimited, OutcomeUnavailable,
	}
}

// OAuth client kinds.
const (
	ClientKindNone             = "none"
	ClientKindDynamic          = "dynamic"
	ClientKindMetadataDocument = "metadata_document"
)

// ClientKindVocabulary lists every client kind; none means the step
// stopped before a client was identified.
func ClientKindVocabulary() []string {
	return []string{ClientKindNone, ClientKindDynamic, ClientKindMetadataDocument}
}
