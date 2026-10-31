package contextfabric

import (
	"context"
	"time"

	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Ask Dev answer feedback (CHAOS-11950, plan item I-32a): whether the owner of
// an Ask Dev result found it correct and useful. One feedback row per result,
// keyed by the result id within its organization; only the result's
// submission owner reaches it (SameSubmissionOwner over the result row's
// submission_owner_*), so a rotated credential of the same user on the same
// surface reaches the user's feedback and nobody else does. The row holds two
// closed flags and a timestamp, no free text. It is in the result's backing
// set (plan §2.6, D-22): it goes with the result (the retention job, I-30),
// and an organization purge removes it.

// ResultFeedback is the stored feedback on one result. UpdatedAt is zero when
// the feedback was never set; both flags are then unset.
type ResultFeedback struct {
	Correct   contractsv1.ContextFabricResultFeedbackValue
	Useful    contractsv1.ContextFabricResultFeedbackValue
	UpdatedAt time.Time
}

// UnsetResultFeedback is the feedback of a result whose owner never set one.
func UnsetResultFeedback() ResultFeedback {
	return ResultFeedback{Correct: contractsv1.ContextFabricResultFeedbackUnset, Useful: contractsv1.ContextFabricResultFeedbackUnset}
}

// ResultFeedbackStore reads and sets the feedback of the caller's own
// results. Both methods answer ErrInvestigationResultNotFound (wrapped) for
// every result the caller does not own feedback on: an unknown id, another
// organization's result, a result that completed no submission, another
// owner's result, and a result only closed threads' turns name (design §6
// T6). The route decides the read with the stored-result gate first; the
// store holds the owner and closed-thread rules again in its own statement,
// so a write never lands on a result the caller lost between the two.
type ResultFeedbackStore interface {
	// GetResultFeedback returns the caller's feedback on resultID, or
	// UnsetResultFeedback when none was set.
	GetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (ResultFeedback, error)
	// SetResultFeedback stores both flags with UpdatedAt = now (never
	// earlier than the stamp already stored) and returns what is stored. A
	// repeat with the same flags moves UpdatedAt only.
	SetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, correct, useful contractsv1.ContextFabricResultFeedbackValue, now time.Time) (ResultFeedback, error)
}
