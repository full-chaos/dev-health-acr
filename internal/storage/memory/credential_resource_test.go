package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCredentialStore_RotationInheritsSourceResource proves a rotated
// successor carries the SOURCE credential's Resource binding forward, never
// re-derived from the rotation request (which has no Resource field of its
// own -- see credentialFromRotation's doc comment).
func TestCredentialStore_RotationInheritsSourceResource(t *testing.T) {
	// Given
	ctx := context.Background()
	store := mustCredentialStore(t)
	sourceInput := validCredentialCreateInput("resource-source")
	sourceInput.Resource = "https://example.com/resource"
	source, err := store.CreateCredential(ctx, sourceInput)
	require.NoError(t, err)
	require.Equal(t, "https://example.com/resource", source.Resource)

	// When
	replacement, err := store.RotateCredential(ctx, validCredentialRotationInput(source, "resource-replacement", strings.Repeat("b", 64), true))

	// Then
	require.NoError(t, err)
	require.Equal(t, source.Resource, replacement.Resource)
}
