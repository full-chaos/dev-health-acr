package auth

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestMachineScopes_credentialIssueRemainsAssertionOnly(t *testing.T) {
	// Given
	credentialInput := storage.CredentialCreateInput{
		CredentialID: "cred_machine", OrgID: "org_1", Name: "machine",
		TokenPrefix: "fcacr_abcdef", TokenHash: strings.Repeat("a", 64),
		RepositoryScopes: []string{"owner/repo"}, Scopes: []string{WebAssertionPermissionCredentialIssue},
		ActorID: "actor_1",
	}

	// When
	_, authErr := normalizeScopes([]string{WebAssertionPermissionCredentialIssue})
	storageErr := storage.ValidateCredentialCreateInput(credentialInput)

	// Then
	require.ErrorIs(t, authErr, ErrInvalidCredential)
	require.ErrorIs(t, storageErr, storage.ErrInvalidCredentialInput)
}

func TestDeviceAndSelfLifecycle_logsAndErrorsRedactSecrets(t *testing.T) {
	// Given
	fixture := newDeviceFlowFixture(t, deviceFlowRandom(41))
	started := fixture.start(t)
	principal := deviceApprovalPrincipal("*")
	_, err := fixture.flow.Approve(context.Background(), DeviceApprovalRequest{
		Principal: principal, UserCode: started.UserCode, RepositoryScopes: principal.RepositoryScopes,
	})
	require.NoError(t, err)
	issued, err := fixture.flow.Poll(context.Background(), started.DeviceCode)
	require.NoError(t, err)
	rotation, err := fixture.flow.credentials.RotateSelf(context.Background(), selfPrincipal(issued.Credential))
	require.NoError(t, err)
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))

	// When
	logger.Info("lifecycle values", slog.Any("device", started), slog.Any("rotation", rotation))
	wrapped := fmt.Errorf("lifecycle failed: %v / %v", started, rotation)

	// Then
	for _, secret := range []string{started.DeviceCode, started.UserCode, issued.Token, rotation.Issued.Token} {
		require.NotContains(t, output.String(), secret)
		require.NotContains(t, wrapped.Error(), secret)
	}
	require.Contains(t, output.String(), deviceAuthorizationStartRedacted)
	require.Contains(t, output.String(), selfRotationRedacted)
	require.NotContains(t, output.String(), TokenPrefix)
	require.NotContains(t, wrapped.Error(), TokenPrefix)
}
