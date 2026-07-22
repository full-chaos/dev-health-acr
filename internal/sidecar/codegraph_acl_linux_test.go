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

func TestACLAttributesAbsentAcceptsBothMissingLinuxACLs(t *testing.T) {
	// Given
	missingErr := unix.ENODATA

	// When
	absent := aclAttributesAbsent(missingErr, missingErr)

	// Then
	require.True(t, absent)
}

func TestACLAttributesAbsentRejectsPresentLinuxACL(t *testing.T) {
	// Given
	missingErr := unix.ENODATA

	// When
	absent := aclAttributesAbsent(missingErr, nil)

	// Then
	require.False(t, absent)
}
