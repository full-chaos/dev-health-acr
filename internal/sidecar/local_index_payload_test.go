package sidecar

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeLocalEvidenceBundle_acceptsExactProviderPayloadCeiling(t *testing.T) {
	// Given
	bundle := localBundleWithPayloadBytes(t, maxLocalEvidenceBundlePayloadBytes)

	// When
	_, err := NormalizeLocalEvidenceBundle(bundle)

	// Then
	if err != nil {
		t.Fatalf("NormalizeLocalEvidenceBundle() error = %v", err)
	}
}

func TestNormalizeLocalEvidenceBundle_rejectsProviderPayloadOneOverWithoutLeak(t *testing.T) {
	// Given
	bundle := localBundleWithPayloadBytes(t, maxLocalEvidenceBundlePayloadBytes+1)

	// When
	_, err := NormalizeLocalEvidenceBundle(bundle)

	// Then
	if !errors.Is(err, ErrInvalidLocalEvidenceBundle) {
		t.Fatalf("NormalizeLocalEvidenceBundle() error = %v, want ErrInvalidLocalEvidenceBundle", err)
	}
	if strings.Contains(err.Error(), "/private/acr/provider-payload") {
		t.Fatalf("payload error leaked local content: %q", err)
	}
}

func localBundleWithPayloadBytes(t *testing.T, want int) LocalEvidenceBundle {
	t.Helper()
	bundle := LocalEvidenceBundle{
		ProviderID: "fixture", ProviderVersion: "1.0.0", QueryID: "task-context", QueryVersion: "v1",
		Evidence: []LocalExpandedEvidence{
			{ID: "evidence-01", Locator: "locator-01", Title: "Local symbol", QueryID: "query", Relation: "definition", RepositoryPath: "internal/example.go", StartLine: 1},
			{ID: "evidence-02", Locator: "locator-02", Title: "Local symbol", QueryID: "query", Relation: "definition", RepositoryPath: "internal/example.go", StartLine: 1},
			{ID: "evidence-03", Locator: "locator-03", Title: "Local symbol", QueryID: "query", Relation: "definition", RepositoryPath: "internal/example.go", StartLine: 1},
			{ID: "evidence-04", Locator: "locator-04", Title: "Local symbol", QueryID: "query", Relation: "definition", RepositoryPath: "internal/example.go", StartLine: 1},
		},
	}
	remaining := want - providerBundlePayloadBytes(bundle)
	for index := range bundle.Evidence {
		if remaining == 0 {
			break
		}
		count := min(remaining, maxLocalEvidenceExcerptBytes)
		bundle.Evidence[index].Excerpt = strings.Repeat("x", count)
		remaining -= count
	}
	if remaining != 0 {
		t.Fatalf("payload fixture cannot reach %d bytes", want)
	}
	bundle.Evidence[len(bundle.Evidence)-1].Excerpt = strings.TrimSuffix(bundle.Evidence[len(bundle.Evidence)-1].Excerpt, "x") + `"\\` + "\n" + "/private/acr/provider-payload日"
	for providerBundlePayloadBytes(bundle) < want {
		bundle.Evidence[0].Excerpt += "x"
	}
	for providerBundlePayloadBytes(bundle) > want {
		bundle.Evidence[0].Excerpt = strings.TrimSuffix(bundle.Evidence[0].Excerpt, "x")
	}
	if got := providerBundlePayloadBytes(bundle); got != want {
		t.Fatalf("payload fixture bytes = %d, want %d", got, want)
	}
	return bundle
}

func providerBundlePayloadBytes(bundle LocalEvidenceBundle) int {
	bytes := len(bundle.ProviderID) + len(bundle.ProviderVersion) + len(bundle.QueryID) + len(bundle.QueryVersion) + 1
	for _, evidence := range bundle.Evidence {
		bytes += len(evidence.ID) + len(evidence.Locator) + len(evidence.Title) + len(evidence.Excerpt) + len(evidence.QueryID) + len(evidence.Relation) + len(evidence.RepositoryPath) + decimalDigits(evidence.StartLine)
	}
	return bytes
}
