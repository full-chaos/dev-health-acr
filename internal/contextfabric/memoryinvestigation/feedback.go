package memoryinvestigation

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// The in-memory twin of pginvestigation's feedback store (CHAOS-11950). Every
// rule is decided under mu in the order the PostgreSQL statement decides it:
// the result row in the caller's organization, its submission owner, the
// closed-thread rule, then the feedback row.

var _ contextfabric.ResultFeedbackStore = (*Store)(nil)

// feedbackResultLocked reports whether principal owns feedback on resultID:
// the result is in principal's organization, completed a submission of
// principal (SameSubmissionOwner), and is not hidden by a closed thread.
func (s *Store) feedbackResultLocked(principal storage.Principal, resultID string, now time.Time) bool {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" || resultID == "" {
		return false
	}
	stored, found := s.results[resultID]
	if !found || stored.orgID != orgID || stored.owner == nil || !stored.owner.Is(principal) {
		return false
	}
	return !s.hiddenByClosedThreadLocked(orgID, resultID, now)
}

// GetResultFeedback implements contextfabric.ResultFeedbackStore.
func (s *Store) GetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (contextfabric.ResultFeedback, error) {
	if s == nil {
		return contextfabric.ResultFeedback{}, errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.feedbackResultLocked(principal, resultID, now) {
		return contextfabric.ResultFeedback{}, ErrNotFound
	}
	if feedback, set := s.feedback[resultID]; set {
		return feedback, nil
	}
	return contextfabric.UnsetResultFeedback(), nil
}

// SetResultFeedback implements contextfabric.ResultFeedbackStore.
func (s *Store) SetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, correct, useful contractsv1.ContextFabricResultFeedbackValue, now time.Time) (contextfabric.ResultFeedback, error) {
	if s == nil {
		return contextfabric.ResultFeedback{}, errors.New("memoryinvestigation: store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	if !correct.Valid() || !useful.Valid() || now.IsZero() {
		return contextfabric.ResultFeedback{}, errors.New("memoryinvestigation: feedback flags must be members of the closed vocabulary and the time set")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.feedbackResultLocked(principal, resultID, now) {
		return contextfabric.ResultFeedback{}, ErrNotFound
	}
	// updated_at never moves back (GREATEST in the PostgreSQL statement):
	// a replica whose clock is behind keeps the later stamp.
	at := now.UTC().Truncate(time.Microsecond)
	if previous, set := s.feedback[resultID]; set && previous.UpdatedAt.After(at) {
		at = previous.UpdatedAt
	}
	feedback := contextfabric.ResultFeedback{Correct: correct, Useful: useful, UpdatedAt: at}
	s.feedback[resultID] = feedback
	return feedback, nil
}
