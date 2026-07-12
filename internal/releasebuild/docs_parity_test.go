package releasebuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePolicy_usesLinuxArtifactName_whenShowingConsumerVerification(t *testing.T) {
	// Given
	expected := ArtifactName(Target{Product: "acr-api", GOOS: "linux", GOARCH: "amd64"}, "1.2.3")
	policyPath := filepath.Join("..", "..", "docs", "release-policy.md")

	// When
	policy, err := os.ReadFile(policyPath)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), expected) {
		t.Fatalf("release policy must use %q", expected)
	}
}
