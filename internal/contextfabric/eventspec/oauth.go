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
)

// OAuthStepVocabulary lists every step.
func OAuthStepVocabulary() []string { return oauthvocab.StepVocabulary() }

// OAuthOutcomeVocabulary lists every outcome, ok first.
func OAuthOutcomeVocabulary() []string { return oauthvocab.OutcomeVocabulary() }

// OAuthScopeVocabulary lists every OAuth scope.
func OAuthScopeVocabulary() []string { return oauthvocab.ScopeVocabulary() }

// OAuthClientKindVocabulary lists every client kind.
func OAuthClientKindVocabulary() []string { return oauthvocab.ClientKindVocabulary() }

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
	BoundedAggregation: "exactly one line per request to /register, /authorize, /authorize/consent, /device_authorization or /token; metadata routes emit none",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "step", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthStepVocabulary()},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthOutcomeVocabulary()},
		{Key: "client_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthClientKindVocabulary()},
		{Key: "scopes", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: OAuthScopeVocabulary(), Applicability: "the requested scopes on an authorize ok line, the granted scopes on a token ok line, empty on every other line"},
		{Key: "status", Type: FieldInt, Presence: PresenceRequired},
	},
}
