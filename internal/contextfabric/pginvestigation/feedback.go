package pginvestigation

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// Ask Dev answer feedback (CHAOS-11950). A feedback row is reached only
// through its result row, in one statement: the caller's organization, the
// result's submission owner (ownerMatchSQL over submission_owner_*; a result
// that completed no submission has none and matches no caller), and the
// closed-thread rule (hiddenByClosedThreadSQL, design §6 T6). So a write never
// lands on a result the caller lost after the route's serving decision.

var _ contextfabric.ResultFeedbackStore = (*Store)(nil)

// feedbackResultSQL is the result row the caller owns feedback on, over $1
// result id, $2 org id, $3 now and the caller's owner $4-$7.
func feedbackResultSQL() string {
	return `r.result_id = $1 AND r.org_id = $2
   AND ` + ownerMatchSQL("r.submission_owner_auth_method", "r.submission_owner_subject", "r.submission_owner_user_id", "r.submission_owner_surface", "$4", "$5", "$6", "$7") + `
   AND NOT ` + hiddenByClosedThreadSQL("$3")
}

func feedbackArgs(principal storage.Principal, resultID string, now time.Time) ([]any, error) {
	orgID := strings.TrimSpace(principal.OrgID)
	if orgID == "" || strings.TrimSpace(resultID) == "" || now.IsZero() {
		return nil, ErrNotFound
	}
	owner, err := contextfabric.SubmissionOwnerOf(principal)
	if err != nil {
		return nil, ErrNotFound
	}
	return []any{resultID, orgID, now.UTC(), owner.AuthenticationMethod, owner.Subject, owner.UserID, string(owner.Surface)}, nil
}

// GetResultFeedback implements contextfabric.ResultFeedbackStore.
func (s *Store) GetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, now time.Time) (contextfabric.ResultFeedback, error) {
	if err := s.configured(); err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	args, err := feedbackArgs(principal, resultID, now)
	if err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	var correct, useful sql.NullString
	var updatedAt sql.NullTime
	err = s.db.QueryRowContext(ctx, `
SELECT f.correct, f.useful, f.updated_at
  FROM acr.context_fabric_investigation_results r
  LEFT JOIN acr.context_fabric_result_feedback f ON f.result_id = r.result_id AND f.org_id = r.org_id
 WHERE `+feedbackResultSQL(), args...).Scan(&correct, &useful, &updatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ResultFeedback{}, ErrNotFound
	case err != nil:
		return contextfabric.ResultFeedback{}, fmt.Errorf("get result feedback: %w", sanitizeError(err))
	}
	if !updatedAt.Valid {
		return contextfabric.UnsetResultFeedback(), nil
	}
	return readResultFeedback(correct.String, useful.String, updatedAt.Time)
}

// SetResultFeedback implements contextfabric.ResultFeedbackStore.
func (s *Store) SetResultFeedback(ctx context.Context, principal storage.Principal, resultID string, correct, useful contractsv1.ContextFabricResultFeedbackValue, now time.Time) (contextfabric.ResultFeedback, error) {
	if err := s.configured(); err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	if !correct.Valid() || !useful.Valid() {
		return contextfabric.ResultFeedback{}, errors.New("pginvestigation: feedback flags must be members of the closed vocabulary")
	}
	args, err := feedbackArgs(principal, resultID, now)
	if err != nil {
		return contextfabric.ResultFeedback{}, err
	}
	args = append(args, string(correct), string(useful))
	var storedCorrect, storedUseful string
	var updatedAt time.Time
	err = s.db.QueryRowContext(ctx, `
INSERT INTO acr.context_fabric_result_feedback (result_id, org_id, correct, useful, created_at, updated_at)
SELECT r.result_id, r.org_id, $8, $9, $3, $3
  FROM acr.context_fabric_investigation_results r
 WHERE `+feedbackResultSQL()+`
ON CONFLICT (result_id) DO UPDATE
   SET correct = EXCLUDED.correct, useful = EXCLUDED.useful,
       updated_at = GREATEST(acr.context_fabric_result_feedback.updated_at, EXCLUDED.updated_at)
RETURNING correct, useful, updated_at`, args...).Scan(&storedCorrect, &storedUseful, &updatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return contextfabric.ResultFeedback{}, ErrNotFound
	case err != nil:
		return contextfabric.ResultFeedback{}, fmt.Errorf("set result feedback: %w", sanitizeError(err))
	}
	return readResultFeedback(storedCorrect, storedUseful, updatedAt)
}

// readResultFeedback holds a stored row to the closed vocabulary: a value
// the CHECK constraints admit but the contract does not is an error, never a
// served value.
func readResultFeedback(correct, useful string, updatedAt time.Time) (contextfabric.ResultFeedback, error) {
	feedback := contextfabric.ResultFeedback{
		Correct: contractsv1.ContextFabricResultFeedbackValue(correct), Useful: contractsv1.ContextFabricResultFeedbackValue(useful),
		UpdatedAt: updatedAt.UTC(),
	}
	if !feedback.Correct.Valid() || !feedback.Useful.Valid() || feedback.UpdatedAt.IsZero() {
		return contextfabric.ResultFeedback{}, errors.New("pginvestigation: stored result feedback is invalid")
	}
	return feedback, nil
}
