package auth

import (
	"context"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/storage/memory"
	"github.com/stretchr/testify/require"
)

func TestService_PrepareCreate_reusesCreateInvariants_withoutPersistingPlaintext(t *testing.T) {
	// Given
	ctx := context.Background()
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
	audit := memory.NewAuditStore()
	credentials := newMemoryCredentialStoreAt(t, now, audit)
	service := newTestService(t, credentials, audit, now)
	request := CreateCredentialRequest{
		OrgID:            "11111111-1111-1111-1111-111111111111",
		Name:             "device login",
		RepositoryScopes: []string{"Full-Chaos/Dev-Health-ACR"},
		CreatedBy:        "user_1",
		ExpiresAt:        pointerToTime(now.Add(30 * 24 * time.Hour)),
	}

	// When
	prepared, err := service.PrepareCreate(request)

	// Then
	require.NoError(t, err)
	storedBeforeCommit, err := credentials.List(ctx, request.OrgID)
	require.NoError(t, err)
	require.Empty(t, storedBeforeCommit)
	input := prepared.StorageInput()
	require.Equal(t, []string{"full-chaos/dev-health-acr"}, input.RepositoryScopes)
	require.Equal(t, []string{ScopeContextRead, ScopeEvidenceRead}, input.Scopes)
	require.Equal(t, HashToken(prepared.token), input.TokenHash)
	require.NotContains(t, input.TokenPrefix, prepared.token)

	stored, err := credentials.CreateCredential(ctx, input)
	require.NoError(t, err)
	issued := prepared.Issued(stored)
	require.Equal(t, prepared.token, issued.Token)
	require.True(t, IsTokenShapeValid(issued.Token))
}

func pointerToTime(value time.Time) *time.Time { return &value }
