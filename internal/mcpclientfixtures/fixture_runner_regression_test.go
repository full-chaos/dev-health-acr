package mcpclientfixtures

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientFixtureRunner_rejects_empty_and_trailing_fixture_metadata(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"empty root", func(_ *testing.T, _ string) {}},
		{"trailing metadata", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "fixture.v1.json"), []byte(`{"expected_classification":"package.args"}{}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Given
			root := t.TempDir()
			tc.setup(t, root)

			// When
			err := ValidateClientFixtures(root)

			// Then
			if err == nil {
				t.Fatal("invalid fixture root was accepted")
			}
		})
	}
}
