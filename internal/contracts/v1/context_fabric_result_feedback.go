package v1

import (
	"fmt"
	"slices"
	"time"
)

// Ask Dev answer feedback (Ask Dev plan §5.3 E6, item I-32a): whether the
// caller found one of its own results correct and useful. Feedback is kept
// per result, for the result's owner only: the routes take the same serving
// decision as the read by id, so a caller who cannot read the result cannot
// read or set its feedback, and gets the same 404. Only a result that
// completed an Ask Dev submission (it has an owner) takes feedback; it is in
// the result's backing set and goes with the result's retention. See
// docs/design/ask-dev-conversation-contract.md §11.
const (
	// ContextFabricResultFeedbackSchema is the schema_version of the
	// feedback read: GET and POST
	// /api/v1/context-fabric/investigations/feedback/{result_id}.
	ContextFabricResultFeedbackSchema = "context_fabric_result_feedback.v1"
	// ContextFabricResultFeedbackRequestSchema is the schema_version of the
	// POST body that sets the feedback.
	ContextFabricResultFeedbackRequestSchema = "context_fabric_result_feedback_request.v1"
)

// ContextFabricResultFeedbackValue is the closed value of one feedback
// flag.
type ContextFabricResultFeedbackValue string

const (
	// ContextFabricResultFeedbackYes: the caller said yes.
	ContextFabricResultFeedbackYes ContextFabricResultFeedbackValue = "yes"
	// ContextFabricResultFeedbackNo: the caller said no.
	ContextFabricResultFeedbackNo ContextFabricResultFeedbackValue = "no"
	// ContextFabricResultFeedbackUnset: the caller said nothing, or cleared
	// what it said.
	ContextFabricResultFeedbackUnset ContextFabricResultFeedbackValue = "unset"
)

// ContextFabricResultFeedbackValueVocabulary is the closed set, in the
// published enum's order.
func ContextFabricResultFeedbackValueVocabulary() [3]ContextFabricResultFeedbackValue {
	return [3]ContextFabricResultFeedbackValue{ContextFabricResultFeedbackYes, ContextFabricResultFeedbackNo, ContextFabricResultFeedbackUnset}
}

// Valid reports whether v is a member of the closed set.
func (v ContextFabricResultFeedbackValue) Valid() bool {
	members := ContextFabricResultFeedbackValueVocabulary()
	return slices.Contains(members[:], v)
}

// ContextFabricResultFeedbackRequest is the body of POST
// /api/v1/context-fabric/investigations/feedback/{result_id}. It sets both
// flags: the whole feedback, so a repeat of one request is harmless. Both
// flags are required; "unset" clears one.
type ContextFabricResultFeedbackRequest struct {
	SchemaVersion string                           `json:"schema_version"`
	Correct       ContextFabricResultFeedbackValue `json:"correct"`
	Useful        ContextFabricResultFeedbackValue `json:"useful"`
}

// Validate holds the request to its schema.
func (r ContextFabricResultFeedbackRequest) Validate() error {
	switch {
	case r.SchemaVersion != ContextFabricResultFeedbackRequestSchema:
		return fmt.Errorf("feedback request schema_version must be %q", ContextFabricResultFeedbackRequestSchema)
	case !r.Correct.Valid():
		return fmt.Errorf("feedback request correct is not a member of the closed vocabulary")
	case !r.Useful.Valid():
		return fmt.Errorf("feedback request useful is not a member of the closed vocabulary")
	}
	return nil
}

// ContextFabricResultFeedback is the body of GET and POST
// /api/v1/context-fabric/investigations/feedback/{result_id}: the caller's
// feedback on one of its own results.
type ContextFabricResultFeedback struct {
	SchemaVersion string                           `json:"schema_version"`
	ResultID      string                           `json:"result_id"`
	Correct       ContextFabricResultFeedbackValue `json:"correct"`
	Useful        ContextFabricResultFeedbackValue `json:"useful"`
	// UpdatedAt is when the feedback was last set. It is absent when it was
	// never set; both flags are then "unset".
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// Validate holds the feedback to its schema.
func (f ContextFabricResultFeedback) Validate() error {
	switch {
	case f.SchemaVersion != ContextFabricResultFeedbackSchema:
		return fmt.Errorf("feedback schema_version must be %q", ContextFabricResultFeedbackSchema)
	case !stringLengthBetween(f.ResultID, 8, 256):
		return fmt.Errorf("feedback result_id violates v1 bounds")
	case !f.Correct.Valid():
		return fmt.Errorf("feedback correct is not a member of the closed vocabulary")
	case !f.Useful.Valid():
		return fmt.Errorf("feedback useful is not a member of the closed vocabulary")
	case f.UpdatedAt != nil && f.UpdatedAt.IsZero():
		return fmt.Errorf("feedback updated_at is present but zero")
	case f.UpdatedAt == nil && (f.Correct != ContextFabricResultFeedbackUnset || f.Useful != ContextFabricResultFeedbackUnset):
		return fmt.Errorf("feedback that was never set has both flags unset")
	}
	return nil
}

// ParseContextFabricResultFeedbackRequest is the server's decode of the POST
// body: the thread documents' presence rules (no null anywhere, no key twice,
// no unknown key, no trailing content), then Validate.
func ParseContextFabricResultFeedbackRequest(data []byte) (ContextFabricResultFeedbackRequest, error) {
	return parseContextFabricThreadDocument[ContextFabricResultFeedbackRequest](data)
}

// ParseContextFabricResultFeedback is the consumer decode of the feedback
// body, by the same rules.
func ParseContextFabricResultFeedback(data []byte) (ContextFabricResultFeedback, error) {
	return parseContextFabricThreadDocument[ContextFabricResultFeedback](data)
}
