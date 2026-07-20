package sidecar

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateLocalIndexCapabilities_rejectsInvalidStatusFreshnessCombinations(t *testing.T) {
	valid := []LocalIndexCapabilities{
		{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessFresh},
		{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessUnknown},
		{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessStale},
		{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessUnknown},
	}
	for _, capabilities := range valid {
		require.NoError(t, ValidateLocalIndexCapabilities(capabilities))
	}
	invalid := []LocalIndexCapabilities{
		LocalIndexCapabilities{Available: false, Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessUnknown},
		LocalIndexCapabilities{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusUnavailable, Freshness: LocalIndexFreshnessUnknown},
		LocalIndexCapabilities{ProviderID: "fixture", ProviderVersion: "1", Available: true, MaxItems: 1, MaxOutputTokens: 1, Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessStale},
	}
	for _, capabilities := range invalid {
		require.ErrorIs(t, ValidateLocalIndexCapabilities(capabilities), ErrInvalidLocalIndexCapabilities)
	}
}

func TestValidateLocalEvidenceBundle_rejectsNonCanonicalFreshnessWarnings(t *testing.T) {
	base := LocalEvidenceBundle{ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessFresh, Warnings: []string{"indexed_commit_unknown"}}
	valid := []LocalEvidenceBundle{
		base,
		{ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessStale, Truncated: true, Warnings: []string{"local_worktree_mismatch", "local_index_stale", "local_workspace_dirty", "changed_files_truncated", "local_query_budget_exhausted", "indexed_commit_unknown"}},
	}
	for _, bundle := range valid {
		require.NoError(t, ValidateLocalEvidenceBundle(bundle))
	}
	invalid := []LocalEvidenceBundle{
		{ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessFresh, Warnings: []string{"local_index_malformed"}},
		{ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessStale, Warnings: []string{"local_workspace_dirty", "local_index_stale"}},
		{ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1", Status: LocalIndexStatusDegraded, Freshness: LocalIndexFreshnessStale, IndexedCommit: "0123456789abcdef0123456789abcdef01234567", Warnings: []string{"indexed_commit_unknown"}},
	}
	for _, bundle := range invalid {
		require.ErrorIs(t, ValidateLocalEvidenceBundle(bundle), ErrInvalidLocalEvidenceBundle)
	}
}

func TestTrimCodeGraphEvidence_insertsBudgetWarningBeforeUnknownCommit(t *testing.T) {
	bundle := LocalEvidenceBundle{
		ProviderID: "fixture", ProviderVersion: "1", QueryID: "query", QueryVersion: "v1",
		Status: LocalIndexStatusAvailable, Freshness: LocalIndexFreshnessFresh,
		Warnings: []string{"indexed_commit_unknown"},
		Evidence: []LocalExpandedEvidence{
			{ID: "one", Locator: "one", Title: "one", Excerpt: strings.Repeat("a", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1},
			{ID: "two", Locator: "two", Title: "two", Excerpt: strings.Repeat("b", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1},
			{ID: "three", Locator: "three", Title: "three", Excerpt: strings.Repeat("c", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1},
			{ID: "four", Locator: "four", Title: "four", Excerpt: strings.Repeat("d", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1},
			{ID: "five", Locator: "five", Title: "five", Excerpt: strings.Repeat("e", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1},
		},
	}
	trimmed, truncated, err := trimCodeGraphEvidence(bundle)
	require.NoError(t, err)
	require.True(t, truncated)
	require.True(t, trimmed.Truncated)
	require.Equal(t, []string{"local_query_budget_exhausted", "indexed_commit_unknown"}, trimmed.Warnings)
	require.NoError(t, ValidateLocalEvidenceBundle(trimmed))
}

func TestDisabledLocalIndexProvider_reportsTypedUnavailableState(t *testing.T) {
	provider := NewDisabledLocalIndexProvider()
	capabilities, capabilitiesErr := provider.Capabilities(t.Context())
	_, contextErr := provider.ContextForTask(t.Context(), LocalContextRequest{TaskID: "task", Goal: "goal", MaxItems: 1, MaxOutputTokens: 1})
	for _, err := range []error{capabilitiesErr, contextErr} {
		var localErr *LocalIndexError
		require.ErrorAs(t, err, &localErr)
		require.Equal(t, LocalIndexErrorDisabled, localErr.Code())
		require.Equal(t, LocalIndexStatusUnavailable, localErr.Status())
		require.Equal(t, LocalIndexFreshnessUnknown, localErr.Freshness())
		require.Equal(t, []string{"local_index_disabled"}, localErr.Warnings())
		require.ErrorIs(t, err, ErrLocalIndexUnavailable)
	}
	require.False(t, capabilities.Available)
	_, resolveErr := provider.ResolveEvidence(t.Context(), "missing")
	require.True(t, errors.Is(resolveErr, ErrLocalEvidenceNotFound))
}

func TestNewWorkspaceLocalIndexProvider_reportsInvalidConfigurationBeforeDisabled(t *testing.T) {
	provider := NewWorkspaceLocalIndexProvider(LocalIndexConfig{Provider: LocalIndexProviderDisabled, Err: errors.New("unsafe config detail")}, LocalWorkspaceSnapshot{})
	capabilities, err := provider.Capabilities(t.Context())
	var localErr *LocalIndexError
	require.ErrorAs(t, err, &localErr)
	require.Equal(t, LocalIndexErrorMalformed, localErr.Code())
	require.Equal(t, []string{"local_index_malformed"}, localErr.Warnings())
	require.ErrorIs(t, err, errLocalIndexConfigInvalid)
	require.NotContains(t, err.Error(), "unsafe config detail")
	require.False(t, capabilities.Available)
	require.Equal(t, LocalIndexStatusUnavailable, capabilities.Status)
	require.Equal(t, LocalIndexFreshnessUnknown, capabilities.Freshness)
}
