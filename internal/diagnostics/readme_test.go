package diagnostics

import (
	"strings"
	"testing"
)

func TestReadmeText_limits_sharing_to_private_support_channels(t *testing.T) {
	// Given
	input := Input{Identity: Identity{Service: "acr-mcp", Version: "1.2.3", Commit: "commit", BuildDate: "date", GOOS: "linux", GOARCH: "amd64"}}

	// When
	readme := readmeText(input)

	// Then
	for _, want := range []string{"approved private support channels", "Do not attach it to public issues", "Metadata in this bundle", "schema version", "credential source category", "entitlement and scope booleans"} {
		if !strings.Contains(readme, want) {
			t.Errorf("README does not contain %q", want)
		}
	}
}
