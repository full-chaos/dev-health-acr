package eventspec

import "github.com/full-chaos/dev-health-acr/internal/oauthvocab"

// The OAuth authorization-code login acr-api serves for hosted MCP clients:
// one Info line per step, closed vocabularies only. Codes, handles, verifiers,
// client IDs, redirect URIs, state and tokens never reach the line.

// OAuth vocabularies are declared once, in internal/oauthvocab; the names
// below are aliases so this declaration and the producers read one list.
const (
	OAuthStepRegister  = oauthvocab.StepRegister
	OAuthStepAuthorize = oauthvocab.StepAuthorize
	OAuthStepConsent   = oauthvocab.StepConsent
	OAuthStepToken     = oauthvocab.StepToken
	// OAuthStepConsentPreview is the web consent page reading a request.
	OAuthStepConsentPreview = oauthvocab.StepConsentPreview
	// OAuthStepDeviceAuthorization is RFC 8628's device_authorization
	// endpoint.
	OAuthStepDeviceAuthorization = oauthvocab.StepDeviceAuthorization
	// The self-credential lifecycle routes.
	OAuthStepCredentialAck    = oauthvocab.StepCredentialAck
	OAuthStepCredentialRotate = oauthvocab.StepCredentialRotate
	OAuthStepCredentialRevoke = oauthvocab.StepCredentialRevoke
)

// OAuthStepVocabulary lists every step.
func OAuthStepVocabulary() []string { return oauthvocab.StepVocabulary() }

// OAuthOutcomeVocabulary lists every outcome, ok first.
func OAuthOutcomeVocabulary() []string { return oauthvocab.OutcomeVocabulary() }

// OAuthScopeVocabulary lists every OAuth scope.
func OAuthScopeVocabulary() []string { return oauthvocab.ScopeVocabulary() }

// OAuthClientKindVocabulary lists every client kind.
func OAuthClientKindVocabulary() []string { return oauthvocab.ClientKindVocabulary() }

// OAuthClientRefusalVocabulary lists every client refusal, none first.
func OAuthClientRefusalVocabulary() []string { return oauthvocab.ClientRefusalVocabulary() }

// OAuthStepLogMessage is the message of the one OAuth line.
const OAuthStepLogMessage = "acr-api oauth step"

// OAuthStep is the one line each OAuth request to acr-api produces:
// registration, the authorize request (a redirect to the web consent page),
// the consent page's read of the request, its approve or deny decision, and
// each token request. From the lines of one login a reader can rebuild it:
// the client kind, whether the consent page read the request, whether the
// user approved (ok) or denied (access_denied) it or why the decision was
// refused, and why a token request was refused.
var OAuthStep = Event{
	ID:                 "api.oauth_step",
	Msg:                OAuthStepLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per request to /register, /authorize, /authorize/consent, /device_authorization, /token or the credential routes (/api/v1/auth/credentials/self/ack, /rotate, /revoke and the on-behalf /internal/v1/credentials/{credential_id}/revoke, which shares the credential_revoke step), including POSTs refused by the authenticator or limiter before their handler (a method the router rejects with 405 never reaches the route and writes none); metadata routes emit none. On a credential step the outcome reads: ok = done; invalid_request = malformed body; unauthenticated = no valid credential; invalid_grant = nothing to act on or a state conflict (no credential awaiting acknowledgement, stale credential, rotation conflict); expired = the acknowledgement window closed; rate_limited; unavailable = the credential service or runtime could not answer. The credential id and the token never reach the line",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "step", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthStepVocabulary()},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthOutcomeVocabulary()},
		{Key: "client_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthClientKindVocabulary()},
		{Key: "scopes", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: OAuthScopeVocabulary(), Applicability: "the requested scopes on an authorize ok line, the granted scopes on a token ok line, empty on every other line"},
		{Key: "status", Type: FieldInt, Presence: PresenceRequired},
		{Key: "client_refusal", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthClientRefusalVocabulary(), Applicability: "why the client could not be identified or its redirect_uri was not accepted (on register, authorize, consent_preview, consent and device_authorization lines); none on every other line"},
	},
}

// The decisions and refusal reasons of the on-behalf credential revoke.
const (
	CredentialRevokeOnBehalfDecisionRevoked  = oauthvocab.OnBehalfDecisionRevoked
	CredentialRevokeOnBehalfDecisionRefused  = oauthvocab.OnBehalfDecisionRefused
	CredentialRevokeOnBehalfDecisionConflict = oauthvocab.OnBehalfDecisionConflict
	CredentialRevokeOnBehalfReasonNone       = oauthvocab.OnBehalfReasonNone
)

// CredentialRevokeOnBehalfLogMessage is the message of the on-behalf revoke line.
const CredentialRevokeOnBehalfLogMessage = "acr-api credential revoke on behalf"

// CredentialRevokeOnBehalf is the decision line of POST
// /internal/v1/credentials/{credential_id}/revoke: the actor type the Auth
// Control Plane named, whether acr revoked the credential, and, when it did
// not, why. The answer of every refusal is the same 404, so this line is the
// only place the reason is recorded.
var CredentialRevokeOnBehalf = Event{
	ID:                 "api.credential_revoke_on_behalf",
	Msg:                CredentialRevokeOnBehalfLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per request to /internal/v1/credentials/{credential_id}/revoke: one for each request whose body names a valid actor and that reaches the credential service; none for a request refused before it (authenticator, class, scope, limiter, malformed body) or one the service could not answer. The credential id, the actor id and the token never reach the line",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "actor_type", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: oauthvocab.OnBehalfActorVocabulary()},
		{Key: "decision", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: oauthvocab.OnBehalfDecisionVocabulary()},
		{Key: "reason", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: oauthvocab.OnBehalfReasonVocabulary(), Applicability: "why a refused request was refused; none when the credential was revoked or was already revoked"},
	},
}
