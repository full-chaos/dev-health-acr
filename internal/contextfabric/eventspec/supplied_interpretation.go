package eventspec

// SuppliedInterpretationDecisionLogMessage is the msg of the decision line
// for an interpretation the caller ran on its own model.
const SuppliedInterpretationDecisionLogMessage = "context fabric supplied interpretation decision"

// The outcomes of one supplied-interpretation decision.
const (
	SuppliedInterpretationOutcomeSuccess          = "success"
	SuppliedInterpretationOutcomeInvalidOutput    = "invalid_output"
	SuppliedInterpretationOutcomeContractMismatch = "contract_mismatch"
	SuppliedInterpretationOutcomeRequestInvalid   = "request_invalid"
)

// SuppliedInterpretationOutcomeVocabulary lists every outcome the line records.
func SuppliedInterpretationOutcomeVocabulary() []string {
	return []string{
		SuppliedInterpretationOutcomeSuccess, SuppliedInterpretationOutcomeInvalidOutput,
		SuppliedInterpretationOutcomeContractMismatch, SuppliedInterpretationOutcomeRequestInvalid,
	}
}

// SuppliedInterpretationContractFieldVocabulary lists the contract fields a
// supplied interpretation can mismatch on.
func SuppliedInterpretationContractFieldVocabulary() []string {
	return []string{"model_output_version", "prompt_version", "system_sha256"}
}

// SuppliedInterpretationDecision is the one line a request that carries its
// own interpretation produces at the interpret step, on every path: accepted,
// refused for its contract, refused for its content, or refused before
// either. It carries no question text and no part of the supplied output.
// client_model is the caller's declared model name, bounded to a fixed
// character class by the request contract.
var SuppliedInterpretationDecision = Event{
	ID:                 "contextfabric.supplied_interpretation_decision",
	Msg:                SuppliedInterpretationDecisionLogMessage,
	Level:              LevelInfo,
	Multiplicity:       MultiplicityZeroOrOnePerRequest,
	Attribution:        []string{"request_id"},
	BoundedAggregation: "exactly one line per investigation that carries a supplied interpretation, written when the interpret step accepts or refuses it; a request with no supplied interpretation emits none",
	Fields: []Field{
		{Key: "request_id", Type: FieldString, Presence: PresenceRequired},
		{Key: "org_id_hash", Type: FieldString, Presence: PresenceRequired},
		{Key: "interpretation_source", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: []string{"client"}},
		{Key: "outcome", Type: FieldString, Presence: PresenceRequired, ClosedVocabulary: SuppliedInterpretationOutcomeVocabulary()},
		{Key: "client_model", Type: FieldString, Presence: PresenceRequired, Applicability: "the declared model name, or undeclared when the caller sent none; empty when outcome=request_invalid"},
		{Key: "prompt_version", Type: FieldString, Presence: PresenceRequired, Applicability: "the service's own interpretation prompt version"},
		{Key: "model_output_version", Type: FieldString, Presence: PresenceRequired, Applicability: "the service's own model output version"},
		{Key: "contract_mismatch", Type: FieldStringSlice, Presence: PresenceRequired, ClosedVocabulary: SuppliedInterpretationContractFieldVocabulary(), Applicability: "the contract fields whose declared value is absent or differs from the service's own when outcome=contract_mismatch; empty on every other line"},
		{Key: "schema_error_type", Type: FieldString, Presence: PresenceRequired, Applicability: "the schema validator's name for the first failed rule when the output failed the model output schema or the strict decode; empty on every other line"},
		{Key: "rejection_reason", Type: FieldString, Presence: PresenceRequired, Applicability: "the interpretation rejection reason when outcome=invalid_output; empty on every other line"},
	},
}
