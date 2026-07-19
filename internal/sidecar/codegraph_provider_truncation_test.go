package sidecar

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeGraphProvider_ContextForTask_marksCanonicalCandidateDropTruncated(t *testing.T) {
	// Given
	provider, workspace, _ := newFixtureCodeGraphProvider(t)
	request := LocalContextRequest{TaskID: "CHAOS-3007", Goal: "consume the local index safely", MaxItems: 1, MaxOutputTokens: 1000, Workspace: &workspace}

	// When
	bundle, err := provider.ContextForTask(t.Context(), request)

	// Then
	require.NoError(t, err)
	require.Len(t, bundle.Evidence, 1)
	require.True(t, bundle.Truncated)
}
