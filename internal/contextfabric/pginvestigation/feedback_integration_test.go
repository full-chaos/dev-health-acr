package pginvestigation_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/pginvestigation/paritytest"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

// CHAOS-11950: a feedback row goes with its result (design §10, D-22). The
// retention job (I-30) removes result rows; the foreign key removes their
// feedback in the same statement, so no feedback outlives its result. The
// stored value is held to the closed vocabulary on read.
func TestResultFeedbackGoesWithItsResult(t *testing.T) {
	ctx := context.Background()
	db := newInvestigationTestDatabase(t, ctx)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	store, err := pginvestigation.NewStore(db, pginvestigation.WithClock(func() time.Time { return now }))
	require.NoError(t, err)
	owner := storage.Principal{AuthenticationMethod: storage.AuthenticationMethodWebAssertion, Subject: "user-feedback-fk", OrgID: "org-feedback-fk"}
	key, err := contextfabric.SubmissionKeyFor(owner, "submission-feedback-fk")
	require.NoError(t, err)
	resultID := "result_feedback_fk_0001"
	_, admitted, err := store.AdmitSubmission(ctx, contextfabric.SubmissionAdmission{
		Key: key, RequestDigest: "sha256:" + fmt.Sprintf("%064x", 11950), ReservedResultID: resultID, ExpiresAt: now.Add(time.Minute),
	}, now)
	require.NoError(t, err)
	require.True(t, admitted)
	require.NoError(t, store.Save(ctx, owner, paritytest.ValidResult(resultID, "question"), nil, nil,
		contextfabric.TimeAxisKeyFor(contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}),
		contextfabric.ReuseRetrievalIdentity{}, contextfabric.ReusePromptVersions{}, contextfabric.ReuseVersionAuthorities{}, 0, "",
		contextfabric.SemanticStateAbsent(contextfabric.SemanticStateAbsenceTurnEndedBeforeInterpretation)))
	_, err = store.SetResultFeedback(ctx, owner, resultID, contractsv1.ContextFabricResultFeedbackYes, contractsv1.ContextFabricResultFeedbackNo, now)
	require.NoError(t, err)

	// A value the CHECK constraints refuse never reaches a read.
	_, err = db.ExecContext(ctx, `UPDATE acr.context_fabric_result_feedback SET correct = 'maybe' WHERE result_id = $1`, resultID)
	require.Error(t, err, "the CHECK constraint refuses a value outside the closed vocabulary")

	var rows int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_result_feedback WHERE result_id = $1`, resultID).Scan(&rows))
	require.Equal(t, 1, rows)
	// Remove the result row as the retention job will.
	_, err = db.ExecContext(ctx, `DELETE FROM acr.context_fabric_investigation_results WHERE result_id = $1`, resultID)
	require.NoError(t, err)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM acr.context_fabric_result_feedback WHERE result_id = $1`, resultID).Scan(&rows))
	require.Equal(t, 0, rows, "the feedback row outlived its result")
	_, err = store.GetResultFeedback(ctx, owner, resultID, now)
	require.ErrorIs(t, err, contextfabric.ErrInvestigationResultNotFound)
}
