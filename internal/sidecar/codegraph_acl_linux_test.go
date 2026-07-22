//go:build linux

package sidecar

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestIsACLAttributeMissingAcceptsLinuxENODATA(t *testing.T) {
	// Given
	err := unix.ENODATA

	// When
	missing := isACLAttributeMissing(err)

	// Then
	require.True(t, missing)
}

func TestIsACLAttributeMissingRejectsLinuxPermissionError(t *testing.T) {
	// Given
	err := unix.EPERM

	// When
	missing := isACLAttributeMissing(err)

	// Then
	require.False(t, missing)
}

func TestCodeGraphRootHasOnlyBaseACL_acceptsLinuxRootWithoutPOSIXACL(t *testing.T) {
	// Given
	root := t.TempDir()

	// When
	trusted := codeGraphRootHasOnlyBaseACL(root)

	// Then
	require.True(t, trusted)
}
