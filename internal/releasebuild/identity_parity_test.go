package releasebuild

import (
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/version"
)

func TestIdentity_Validate_matches_canonical_version_validator(t *testing.T) {
	versions := []string{
		"1.2.3",
		"1.2.3-alpha.1+build.7",
		"1.2.3-01",
		"v1.2.3",
		"1.2.3-",
		"01.2.3",
	}
	for _, value := range versions {
		t.Run(value, func(t *testing.T) {
			// When
			err := Identity{Version: value, Commit: testIdentity().Commit, Date: testIdentity().Date}.Validate()

			// Then
			if got, want := err == nil, version.IsCanonical(value); got != want {
				t.Errorf("Validate() success = %t, canonical success = %t", got, want)
			}
		})
	}
}
