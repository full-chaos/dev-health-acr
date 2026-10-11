package v1

// ContextFabricStatusCorrectionDirection names which model-authored status
// the server's outcome-derived completeness authority replaced, and with
// what. The vocabulary is exactly the pairs the authority is ever allowed to
// serve: a model-authored complete may be lowered, and the lateral
// partial/degraded pair may swap. A status is never corrected UP to complete,
// so no member names that direction.
type ContextFabricStatusCorrectionDirection string

const (
	ContextFabricStatusCorrectionCompleteToPartial  ContextFabricStatusCorrectionDirection = "complete_to_partial"
	ContextFabricStatusCorrectionCompleteToDegraded ContextFabricStatusCorrectionDirection = "complete_to_degraded"
	ContextFabricStatusCorrectionPartialToDegraded  ContextFabricStatusCorrectionDirection = "partial_to_degraded"
	ContextFabricStatusCorrectionDegradedToPartial  ContextFabricStatusCorrectionDirection = "degraded_to_partial"
)

// ContextFabricStatusCorrection discloses that the served status is not the
// label the model authored: the server derived completeness from the answer's
// own requirement outcome rows and replaced the label.
//
// IT IS THE SOURCE ON THE RESULT and Completeness.StatusCorrection is its
// mirror, the RefusalBasis arrangement and for the same reason: the status
// itself is overwritten, so without a stored record the model's label would
// exist only on a log line, and a block that lived only on Completeness would
// be dropped by the first recomputation.
//
// ABSENT means the served status is the model's own label (no flag corrected
// it, or the rows agreed). It never means "unknown".
type ContextFabricStatusCorrection struct {
	// ModelStatus is the status the model authored and the server replaced.
	ModelStatus ContextFabricInvestigationStatus `json:"model_status"`
	// Direction pairs ModelStatus with the served status.
	Direction ContextFabricStatusCorrectionDirection `json:"direction"`
	// DecidingRequirement is the identity of the outcome row the derivation
	// relied on, as it appears in Completeness.Outcomes. Empty when no single
	// row decided it.
	DecidingRequirement string `json:"deciding_requirement,omitempty"`
	// DecidingOutcome is that row's outcome.
	DecidingOutcome ContextFabricPlanRequirementOutcome `json:"deciding_outcome,omitempty"`
}

var contextFabricStatusCorrectionDirections = [...]ContextFabricStatusCorrectionDirection{
	ContextFabricStatusCorrectionCompleteToPartial,
	ContextFabricStatusCorrectionCompleteToDegraded,
	ContextFabricStatusCorrectionPartialToDegraded,
	ContextFabricStatusCorrectionDegradedToPartial,
}

// ContextFabricStatusCorrectionDirectionVocabulary returns the closed set.
func ContextFabricStatusCorrectionDirectionVocabulary() []ContextFabricStatusCorrectionDirection {
	return append([]ContextFabricStatusCorrectionDirection(nil), contextFabricStatusCorrectionDirections[:]...)
}

// ContextFabricStatusCorrectionDirectionFor returns the direction for a
// model status and the served status, and false for a pair the authority
// never serves.
func ContextFabricStatusCorrectionDirectionFor(model, served ContextFabricInvestigationStatus) (ContextFabricStatusCorrectionDirection, bool) {
	direction := ContextFabricStatusCorrectionDirection(string(model) + "_to_" + string(served))
	for _, member := range contextFabricStatusCorrectionDirections {
		if member == direction {
			return direction, true
		}
	}
	return "", false
}

// ContextFabricStatusCorrectionModelStatus is the status a direction starts
// from.
func ContextFabricStatusCorrectionModelStatus(direction ContextFabricStatusCorrectionDirection) (ContextFabricInvestigationStatus, bool) {
	switch direction {
	case ContextFabricStatusCorrectionCompleteToPartial, ContextFabricStatusCorrectionCompleteToDegraded:
		return ContextFabricInvestigationComplete, true
	case ContextFabricStatusCorrectionPartialToDegraded:
		return ContextFabricInvestigationPartial, true
	case ContextFabricStatusCorrectionDegradedToPartial:
		return ContextFabricInvestigationDegraded, true
	}
	return "", false
}
