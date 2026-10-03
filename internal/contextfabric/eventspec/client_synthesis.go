package eventspec

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// The outcomes of one client-synthesis decision.
const (
	ClientSynthesisOutcomeServed        = "served"
	ClientSynthesisOutcomeUnavailable   = "unavailable"
	ClientSynthesisOutcomeInputTooLarge = "input_too_large"
)

// ClientSynthesisOutcomeVocabulary lists every outcome the line records.
func ClientSynthesisOutcomeVocabulary() []string {
	return []string{ClientSynthesisOutcomeServed, ClientSynthesisOutcomeUnavailable, ClientSynthesisOutcomeInputTooLarge}
}

// ClientSynthesisDecision is the one line a turn that asked to write its own
// answer produces when it reaches the synthesis seam: the turn was refused
// because this deployment cannot serve it, its input did not fit the bound, or
// the input was served. It carries no question text and no part of the input.
var ClientSynthesisDecision = Event{
	ID:                 "contextfabric.client_synthesis_decision",
	Msg:                contextfabric.ClientSynthesisDecisionLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per investigation that asked for client synthesis, written when the turn is refused at its start, when the input cannot be bounded, or when the input is served; a turn that ends before the synthesis step emits none, and a request that did not ask for client synthesis emits none",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: ClientSynthesisOutcomeVocabulary()},
		{Key: "status", Type: FieldString, Presence: PresenceRequired, Applicability: "the served result status when outcome=served; empty on every other line"},
		{Key: "bundle_bytes", Type: FieldInt, Presence: PresenceRequired, Applicability: "the size of the served synthesis input; 0 unless outcome=served"},
		{Key: "max_bytes", Type: FieldInt, Presence: PresenceRequired, Applicability: "the bound the input was fitted to; 0 when outcome=unavailable"},
		{Key: "bounded", Type: FieldBool, Presence: PresenceRequired, Applicability: "whether the facts were reduced to fit the bound"},
		{Key: "facts_read", Type: FieldInt, Presence: PresenceRequired, Applicability: "0 when outcome=unavailable"},
		{Key: "facts_given", Type: FieldInt, Presence: PresenceRequired, Applicability: "0 when outcome=unavailable"},
		{Key: "commits_retracted", Type: FieldInt, Presence: PresenceRequired, Applicability: "the commits the commit gate retracted from the served result; 0 unless outcome=served"},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}
