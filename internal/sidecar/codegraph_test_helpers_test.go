package sidecar

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func managedCodeGraphFixtureRoot(t *testing.T, home string) string {
	t.Helper()
	managedRoot := filepath.Join(home, ".omo", "codegraph", "projects")
	if _, err := os.Stat(managedRoot); errors.Is(err, os.ErrNotExist) {
		require.NoError(t, os.MkdirAll(managedRoot, 0o700))
	} else {
		require.NoError(t, err)
	}
	return managedRoot
}
