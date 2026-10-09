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
	// StepConsentPreview is the web consent page reading a request before
	// the signed-in user decides it (StepConsent).
	StepConsentPreview = "consent_preview"
	// StepDeviceAuthorization is RFC 8628's device_authorization endpoint,
	// which starts a device-code grant. StepToken covers every /token poll
	// against it, the same as it covers authorization_code exchanges.
	StepDeviceAuthorization = "device_authorization"
	// The credential lifecycle of a signed-in caller's own credential:
	// acknowledging a device-poll credential it stored, rotating it,
	// revoking it (or rolling a rotation back).
	StepCredentialAck    = "credential_ack"
	StepCredentialRotate = "credential_rotate"
	StepCredentialRevoke = "credential_revoke"
)

// StepVocabulary lists every step.
func StepVocabulary() []string {
	return []string{StepRegister, StepAuthorize, StepConsentPreview, StepConsent, StepDeviceAuthorization, StepToken, StepCredentialAck, StepCredentialRotate, StepCredentialRevoke}
}

// OAuth step outcomes. "ok" means the step did what it exists to do: a client
// was registered, the browser was sent to the consent page, the consent page
// read the request, a code was issued, a credential was minted. Every other member names the one reason the step stopped.
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
	OutcomeInvalidScope            = "invalid_scope"
	OutcomeInvalidGrant            = "invalid_grant"
	OutcomePKCEMismatch            = "pkce_mismatch"
	OutcomeRedirectMismatch        = "redirect_mismatch"
	OutcomeClientMismatch          = "client_mismatch"
	OutcomeResourceMismatch        = "resource_mismatch"
	OutcomeRateLimited             = "rate_limited"
	// OutcomeUnauthenticated: a consent request without a valid web
	// assertion for a signed-in user.
	OutcomeUnauthenticated = "unauthenticated"
	// OutcomeAuthorizationPending: an RFC 8628 /token poll before the device
	// authorization has been decided.
	OutcomeAuthorizationPending = "authorization_pending"
	// OutcomeSlowDown: an RFC 8628 /token poll faster than the device
	// authorization's interval since the last poll.
	OutcomeSlowDown    = "slow_down"
	OutcomeUnavailable = "unavailable"
)

// OutcomeVocabulary lists every outcome, ok first.
func OutcomeVocabulary() []string {
	return []string{
		OutcomeOK, OutcomePending, OutcomeAccessDenied, OutcomeExpired, OutcomeAlreadyCompleted,
		OutcomeInvalidRequest, OutcomeInvalidClient, OutcomeInvalidClientMetadata, OutcomeInvalidRedirectURI,
		OutcomeUnsupportedResponseType, OutcomeUnsupportedGrantType, OutcomePKCERequired, OutcomeInvalidTarget, OutcomeInvalidScope,
		OutcomeInvalidGrant, OutcomePKCEMismatch, OutcomeRedirectMismatch, OutcomeClientMismatch,
		OutcomeResourceMismatch, OutcomeUnauthenticated, OutcomeAuthorizationPending, OutcomeSlowDown, OutcomeRateLimited, OutcomeUnavailable,
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

// Client refusals: why a step could not identify its client, or could not
// accept the redirect_uri a request presented for it. "none" on every line
// whose client was not refused.
const (
	ClientRefusalNone = "none"
	// ClientRefusalUnknownClient: not a registered client, and not a client
	// ID metadata document URL.
	ClientRefusalUnknownClient = "unknown_client"
	// ClientRefusalNotHTTPS: a client ID that is an absolute URL with a host
	// and a scheme other than https.
	ClientRefusalNotHTTPS = "not_https"
	// ClientRefusalUnsupportedClientID: an https client ID this server does
	// not accept as a metadata document URL, or metadata documents are off.
	ClientRefusalUnsupportedClientID = "unsupported_client_id"
	// ClientRefusalFetchFailed: the metadata document could not be fetched
	// (network error, timeout, a status other than 200, a redirect).
	ClientRefusalFetchFailed = "fetch_failed"
	// ClientRefusalPrivateAddress: the metadata document host resolves to an
	// address that is not publicly routable.
	ClientRefusalPrivateAddress = "private_address"
	// ClientRefusalTooLarge: the metadata document exceeds the size limit.
	ClientRefusalTooLarge = "too_large"
	// ClientRefusalInvalidDocument: the metadata document is not a JSON
	// object served as application/json, or its member names are ambiguous
	// (two equal ignoring case, or one equal to a known member only ignoring
	// case).
	ClientRefusalInvalidDocument = "invalid_document"
	// ClientRefusalBadClientID: the document's client_id is not the URL it
	// was fetched from.
	ClientRefusalBadClientID = "bad_client_id"
	// ClientRefusalInvalidRedirectURIs: the document lists no redirect URI,
	// or one this server does not accept.
	ClientRefusalInvalidRedirectURIs = "invalid_redirect_uris"
	// ClientRefusalAuthMethodUnsupported: the document neither names nor
	// lists the token endpoint authentication method "none".
	ClientRefusalAuthMethodUnsupported = "auth_method_unsupported"
	// ClientRefusalRedirectURIMismatch: the presented redirect_uri is not
	// one the client registered or its document lists.
	ClientRefusalRedirectURIMismatch = "redirect_uri_mismatch"
)

// ClientRefusalVocabulary lists every client refusal, none first.
func ClientRefusalVocabulary() []string {
	return []string{
		ClientRefusalNone, ClientRefusalUnknownClient, ClientRefusalNotHTTPS, ClientRefusalUnsupportedClientID,
		ClientRefusalFetchFailed, ClientRefusalPrivateAddress, ClientRefusalTooLarge, ClientRefusalInvalidDocument,
		ClientRefusalBadClientID, ClientRefusalInvalidRedirectURIs, ClientRefusalAuthMethodUnsupported, ClientRefusalRedirectURIMismatch,
	}
}

// Scopes an OAuth request may ask for and a credential may be granted, in
// canonical order. internal/auth pins this list against its own scope
// constants.
const (
	ScopeContextRead  = "context:read"
	ScopeEvidenceRead = "evidence:read"
	ScopeDataRead     = "data:read"
)

// ScopeVocabulary lists every OAuth scope.
func ScopeVocabulary() []string {
	return []string{ScopeContextRead, ScopeEvidenceRead, ScopeDataRead}
}
