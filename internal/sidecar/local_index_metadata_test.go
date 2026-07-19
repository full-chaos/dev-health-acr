package sidecar

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeLocalEvidenceBundle_validatesRetainedMetadata(t *testing.T) {
	// Given
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	valid := LocalEvidenceBundle{
		ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "query", QueryVersion: "v1",
		IndexedAt: &now, IndexedRef: "main", IndexedCommit: strings.Repeat("a", 40), Warnings: []string{"indexed_commit_unknown"}, Truncated: true,
		Evidence: []LocalExpandedEvidence{{ID: "evidence", Locator: "locator", Title: "title", QueryID: "query", Relation: "definition", RepositoryPath: "internal/sidecar/local_index.go", StartLine: 12}},
	}
	cases := []struct {
		name   string
		mutate func(*LocalEvidenceBundle)
	}{
		{name: "invalid indexed ref", mutate: func(bundle *LocalEvidenceBundle) { bundle.IndexedRef = "bad\nref" }},
		{name: "invalid indexed commit", mutate: func(bundle *LocalEvidenceBundle) { bundle.IndexedCommit = strings.Repeat("A", 40) }},
		{name: "too many warnings", mutate: func(bundle *LocalEvidenceBundle) { bundle.Warnings = make([]string, 101) }},
		{name: "duplicate warning", mutate: func(bundle *LocalEvidenceBundle) { bundle.Warnings = []string{"duplicate", "duplicate"} }},
		{name: "item query too long", mutate: func(bundle *LocalEvidenceBundle) { bundle.Evidence[0].QueryID = strings.Repeat("q", 65) }},
		{name: "item relation too long", mutate: func(bundle *LocalEvidenceBundle) { bundle.Evidence[0].Relation = strings.Repeat("r", 65) }},
		{name: "item path traversal", mutate: func(bundle *LocalEvidenceBundle) { bundle.Evidence[0].RepositoryPath = "../secret" }},
		{name: "line without path", mutate: func(bundle *LocalEvidenceBundle) {
			bundle.Evidence[0].RepositoryPath, bundle.Evidence[0].StartLine = "", 1
		}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// Given
			bundle := valid
			bundle.Warnings = append([]string(nil), valid.Warnings...)
			bundle.Evidence = append([]LocalExpandedEvidence(nil), valid.Evidence...)
			testCase.mutate(&bundle)

			// When
			_, err := NormalizeLocalEvidenceBundle(bundle)

			// Then
			require.ErrorIs(t, err, ErrInvalidLocalEvidenceBundle)
		})
	}
}

func TestNormalizeLocalEvidenceBundle_countsRetainedMetadataAtPayloadBoundary(t *testing.T) {
	// Given
	accepted := localBundleWithPayloadBytes(t, maxLocalEvidenceBundlePayloadBytes)
	rejected := localBundleWithPayloadBytes(t, maxLocalEvidenceBundlePayloadBytes+1)

	// When
	_, acceptedErr := NormalizeLocalEvidenceBundle(accepted)
	_, rejectedErr := NormalizeLocalEvidenceBundle(rejected)

	// Then
	require.NoError(t, acceptedErr)
	require.ErrorIs(t, rejectedErr, ErrInvalidLocalEvidenceBundle)
}
