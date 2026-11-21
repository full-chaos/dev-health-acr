package genkitruntime

import (
	"context"
	"errors"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// Error classes a rejected draw logs when no rule judged it. They are not
// rejection reasons (the closed vocabulary names the rules that refuse a
// draft) and are spelled with an error_ prefix so a reader never confuses the
// two. Fixed identifiers only.
const (
	drawErrorCanceled         = "error_canceled"
	drawErrorDeadlineExceeded = "error_deadline_exceeded"
	drawErrorModelOutput      = "error_model_output"
	drawErrorOther            = "error_other"
)

// synthesisDrawReason names, for the draw log, why one draw was refused: the
// output-schema check when Genkit's local check refused the draw, even when a
// rule of this package refused the same draft too (the schema check strips
// keys, so that rule refusal is a result of it, not the cause); otherwise the
// rule when a rule judged the draft, and otherwise the class of the error. A
// draw with no error returns the empty word, which the log renders as "none".
// Only the per-draw log reads this: the rejection the call returns keeps the
// rule.
func synthesisDrawReason(err error, local *decodeSynthesisRejection) string {
	if err == nil {
		return ""
	}
	if local != nil && local.schemaStage != "" {
		return string(contextfabric.RejectionReasonOutputSchemaMismatch)
	}
	var rejection *contextfabric.SynthesisRejection
	if errors.As(err, &rejection) {
		return string(contextfabric.SynthesisRejectionReasonOf(err))
	}
	switch {
	case errors.Is(err, contextfabric.ErrModelCancelled), errors.Is(err, context.Canceled):
		return drawErrorCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return drawErrorDeadlineExceeded
	case errors.Is(err, contextfabric.ErrModelOutput):
		return drawErrorModelOutput
	default:
		return drawErrorOther
	}
}
