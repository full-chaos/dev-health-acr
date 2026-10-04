package eventspec

import "github.com/full-chaos/dev-health-acr/internal/contextfabric"

// The outcomes of one supplied-synthesis decision.
const (
	SuppliedSynthesisOutcomeServed                 = "served"
	SuppliedSynthesisOutcomeContractMismatch       = "contract_mismatch"
	SuppliedSynthesisOutcomeInterpretationRequired = "interpretation_required"
	SuppliedSynthesisOutcomeInputChanged           = "input_changed"
	SuppliedSynthesisOutcomeRejected               = "rejected"
	SuppliedSynthesisOutcomeUnavailable            = "unavailable"
)

// SuppliedSynthesisOutcomeVocabulary lists every outcome the line records.
func SuppliedSynthesisOutcomeVocabulary() []string {
	return []string{
		SuppliedSynthesisOutcomeServed, SuppliedSynthesisOutcomeContractMismatch, SuppliedSynthesisOutcomeInterpretationRequired,
		SuppliedSynthesisOutcomeInputChanged, SuppliedSynthesisOutcomeRejected, SuppliedSynthesisOutcomeUnavailable,
	}
}

// SuppliedSynthesisContractFieldVocabulary lists the fields a supplied
// synthesis can mismatch on.
func SuppliedSynthesisContractFieldVocabulary() []string {
	return []string{"model_output_version", "prompt_version", "system_sha256", "input_sha256"}
}

// SuppliedSynthesisDecision is the one line a request that carries a
// synthesis the caller wrote produces: refused at the start of the turn,
// refused when the draft is checked against the rebuilt input, or served. It
// carries no question text and no part of the supplied output. client_model
// is the caller's declared model name, bounded to a fixed character class by
// the request contract.
var SuppliedSynthesisDecision = Event{
	ID:                 "contextfabric.supplied_synthesis_decision",
	Msg:                contextfabric.SuppliedSynthesisDecisionLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "at most one line per investigation that carries a supplied synthesis, written when the turn is refused at its start, when the draft is checked against the rebuilt input, or when the result is saved; a turn that ends before the synthesis step with an accepted contract emits none, and a request with no supplied synthesis emits none",
	Fields: []Field{
		{Key: "org_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: SuppliedSynthesisOutcomeVocabulary()},
		{Key: "contract_mismatch", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: SuppliedSynthesisContractFieldVocabulary(), Applicability: "the contract fields whose declared value is absent or differs from the service's own when outcome=contract_mismatch; empty on every other line"},
		{Key: "rejection_reason", Type: FieldString, Presence: PresenceRequired, Applicability: "the synthesis rejection reason when outcome=rejected; empty on every other line"},
		{Key: "client_model", Type: FieldString, Presence: PresenceRequired, Applicability: "the declared model name, or undeclared when the caller sent none"},
		{Key: "output_bytes", Type: FieldInt, Presence: PresenceRequired, Applicability: "the size of the supplied output in bytes"},
		{Key: "request_id", Type: FieldString, Presence: PresenceConditional, Applicability: "written when the request context carries a request ID"},
	},
}
