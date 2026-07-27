package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestCredentialStore_RollbackSerializesStateAndAuditForConcurrentRequests(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := NewAuditStore()
	_, lifecycle, err := newCredentialStore(audit, func() time.Time { return now })
	require.NoError(t, err)
	source, err := lifecycle.CreateCredential(context.Background(), validCredentialCreateInput("rollback-source"))
	require.NoError(t, err)
	successor, err := lifecycle.RotateCredential(context.Background(), validCredentialRotationInput(source, "rollback-successor", strings.Repeat("b", 64), false))
	require.NoError(t, err)
	input := storage.CredentialRotationRollbackInput{
		OrgID: source.OrgID, SourceCredentialID: source.CredentialID, SuccessorCredentialID: successor.CredentialID,
		ActorID: "actor_1", RollbackUntil: now.Add(time.Minute),
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Go(func() {
			_, rollbackErr := lifecycle.RollbackCredentialRotation(context.Background(), input)
			results <- rollbackErr
		})
	}
	group.Wait()
	close(results)

	// When
	var successes, conflicts int
	for rollbackErr := range results {
		if rollbackErr == nil {
			successes++
		} else if errors.Is(rollbackErr, storage.ErrConflict) {
			conflicts++
		} else {
			t.Fatalf("RollbackCredentialRotation() error = %v, want conflict", rollbackErr)
		}
	}

	// Then
	require.Equal(t, 1, successes)
	require.Equal(t, 1, conflicts)
	storedSource, err := lifecycle.GetByID(context.Background(), source.OrgID, source.CredentialID)
	require.NoError(t, err)
	require.Nil(t, storedSource.RevokedAt)
	storedSuccessor, err := lifecycle.GetByID(context.Background(), successor.OrgID, successor.CredentialID)
	require.NoError(t, err)
	require.NotNil(t, storedSuccessor.RevokedAt)
	var rollbackAudits int
	for _, event := range audit.Events() {
		if event.Action == storage.AuditActionCredentialRevoked && event.ResourceID == successor.CredentialID {
			rollbackAudits++
		}
	}
	require.Equal(t, 1, rollbackAudits)
}

func TestCredentialStore_RollbackRejectsReceiptAtExactDeadlineWithoutStateOrAuditMutation(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 25, 14, 0, 0, 0, time.UTC)
	audit := NewAuditStore()
	_, lifecycle, err := newCredentialStore(audit, func() time.Time { return now })
	require.NoError(t, err)
	source, err := lifecycle.CreateCredential(context.Background(), validCredentialCreateInput("deadline-source"))
	require.NoError(t, err)
	successor, err := lifecycle.RotateCredential(context.Background(), validCredentialRotationInput(source, "deadline-successor", strings.Repeat("c", 64), false))
	require.NoError(t, err)

	// When
	_, err = lifecycle.RollbackCredentialRotation(context.Background(), storage.CredentialRotationRollbackInput{
		OrgID: source.OrgID, SourceCredentialID: source.CredentialID, SuccessorCredentialID: successor.CredentialID,
		ActorID: "actor_1", RollbackUntil: now,
	})

	// Then
	require.ErrorIs(t, err, storage.ErrConflict)
	storedSuccessor, getErr := lifecycle.GetByID(context.Background(), successor.OrgID, successor.CredentialID)
	require.NoError(t, getErr)
	require.Nil(t, storedSuccessor.RevokedAt)
	for _, event := range audit.Events() {
		require.NotEqual(t, successor.CredentialID, event.ResourceID, "expired rollback wrote an audit")
	}
}
