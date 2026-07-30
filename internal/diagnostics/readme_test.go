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

func TestReadmeTextExplainsCredentialUnavailableStatus(t *testing.T) {
	input := Input{
		Identity: Identity{Service: "acr-mcp", Version: "1.2.3", Commit: "commit", BuildDate: "date", GOOS: "linux", GOARCH: "amd64"},
		Static:   StaticReport{Status: "credential_unavailable"},
	}

	readme := readmeText(input)

	if !strings.Contains(readme, "`credential_unavailable`") || !strings.Contains(readme, "could not be checked safely") {
		t.Fatalf("diagnostic README did not explain credential_unavailable: %q", readme)
	}
}
