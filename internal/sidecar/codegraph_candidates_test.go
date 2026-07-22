package sidecar

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestBoundedCandidateText_preservesUTF8WithinByteBudget(t *testing.T) {
	// Given
	value := strings.Repeat("a", maxLocalEvidenceTitleBytes-1) + "界"

	// When
	result := boundedCandidateText(value)

	// Then
	require.True(t, utf8.ValidString(result))
	require.LessOrEqual(t, len(result), maxLocalEvidenceTitleBytes)
	require.Equal(t, strings.Repeat("a", maxLocalEvidenceTitleBytes-1), result)
}

func TestBuildCodeGraphEvidence_marksDroppedCandidatesAndExactFit(t *testing.T) {
	// Given
	candidates := []codeGraphCandidate{
		{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:one", Title: "definition: one"},
		{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:two", Title: "definition: two"},
	}

	// When
	droppedEvidence, dropped, droppedErr := buildCodeGraphEvidence(candidates, 1, 1000)
	exactEvidence, exact, exactErr := buildCodeGraphEvidence(candidates[:1], 1, 1000)

	// Then
	require.NoError(t, droppedErr)
	require.Len(t, droppedEvidence, 1)
	require.True(t, dropped)
	require.NoError(t, exactErr)
	require.Len(t, exactEvidence, 1)
	require.False(t, exact)
}

func TestBuildCodeGraphEvidence_marksTokenBudgetDrops(t *testing.T) {
	// Given
	candidates := []codeGraphCandidate{
		{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:one", Title: "definition: one"},
		{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:two", Title: "definition: two"},
	}

	// When
	evidence, truncated, err := buildCodeGraphEvidence(candidates, 2, 4)

	// Then
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.True(t, truncated)
}

func TestBuildCodeGraphEvidence_marksRetainedShortenedTitleTruncated(t *testing.T) {
	// Given
	candidates := []codeGraphCandidate{{Command: codeGraphCommandQuery, Type: "definition", Locator: "node:one", Title: "definition: one", Truncated: true}}

	// When
	evidence, truncated, err := buildCodeGraphEvidence(candidates, 1, 1000)

	// Then
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.True(t, truncated)
}

func TestTrimCodeGraphEvidence_marksLogicalPayloadDrops(t *testing.T) {
	// Given
	bundle := LocalEvidenceBundle{ProviderID: codeGraphProviderID, ProviderVersion: "1.2.0", QueryID: "query", QueryVersion: codeGraphJSONQueryVersion, Warnings: []string{"indexed_commit_unknown"}, Evidence: []LocalExpandedEvidence{
		{ID: "cg:one", Locator: "node:one", Title: "one", Excerpt: strings.Repeat("a", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1, QueryID: "query", Relation: "definition"},
		{ID: "cg:two", Locator: "node:two", Title: "two", Excerpt: strings.Repeat("b", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1, QueryID: "query", Relation: "definition"},
		{ID: "cg:three", Locator: "node:three", Title: "three", Excerpt: strings.Repeat("c", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1, QueryID: "query", Relation: "definition"},
		{ID: "cg:four", Locator: "node:four", Title: "four", Excerpt: strings.Repeat("d", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1, QueryID: "query", Relation: "definition"},
		{ID: "cg:five", Locator: "node:five", Title: "five", Excerpt: strings.Repeat("e", maxLocalEvidenceExcerptBytes), EstimatedTokens: 1, QueryID: "query", Relation: "definition"},
	}}

	// When
	trimmed, truncated, err := trimCodeGraphEvidence(bundle)

	// Then
	require.NoError(t, err)
	require.True(t, truncated)
	require.Less(t, len(trimmed.Evidence), len(bundle.Evidence))
	require.NoError(t, ValidateLocalEvidenceBundle(trimmed))
}

func TestCodeGraphCandidateMappings_preserveAllowedQueryIDAndRepositoryPath(t *testing.T) {
	// Given
	candidates := append(affectedCandidates(codeGraphAffected{ChangedFiles: []string{"internal/affected.go"}, AffectedTests: []string{"internal/affected_test.go"}}), fileCandidates([]codeGraphFile{{Path: "internal/file.go", Language: "go"}})...)

	// When
	evidence, truncated, err := buildCodeGraphEvidence(candidates, len(candidates), 1000)

	// Then
	require.NoError(t, err)
	require.False(t, truncated)
	require.Equal(t, []LocalExpandedEvidence{
		{QueryID: "affected", RepositoryPath: "internal/affected.go"},
		{QueryID: "affected", RepositoryPath: "internal/affected_test.go"},
		{QueryID: "files", RepositoryPath: "internal/file.go"},
	}, []LocalExpandedEvidence{
		{QueryID: evidence[0].QueryID, RepositoryPath: evidence[0].RepositoryPath},
		{QueryID: evidence[1].QueryID, RepositoryPath: evidence[1].RepositoryPath},
		{QueryID: evidence[2].QueryID, RepositoryPath: evidence[2].RepositoryPath},
	})
}
