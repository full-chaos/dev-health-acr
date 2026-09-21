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
)

// OAuthStepVocabulary lists every step.
func OAuthStepVocabulary() []string { return oauthvocab.StepVocabulary() }

// OAuthOutcomeVocabulary lists every outcome, ok first.
func OAuthOutcomeVocabulary() []string { return oauthvocab.OutcomeVocabulary() }

// OAuthClientKindVocabulary lists every client kind.
func OAuthClientKindVocabulary() []string { return oauthvocab.ClientKindVocabulary() }

// OAuthStepLogMessage is the message of the one OAuth line.
const OAuthStepLogMessage = "acr-api oauth step"

// OAuthStep is the one line each OAuth request to acr-api produces:
// registration, the authorize request, each consent check the browser page
// makes, and each token request. From the lines of one login a reader can
// rebuild it: the client kind, whether consent was pending, approved or
// denied, and why a token request was refused.
var OAuthStep = Event{
	ID:                 "api.oauth_step",
	Msg:                OAuthStepLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityExactlyOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per request to /register, /authorize or /oauth/token; metadata routes emit none",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "step", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthStepVocabulary()},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthOutcomeVocabulary()},
		{Key: "client_kind", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: OAuthClientKindVocabulary()},
		{Key: "status", Type: FieldInt, Presence: PresenceRequired},
	},
}
