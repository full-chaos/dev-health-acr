//go:build darwin

package sidecar

import (
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestIsACLAttributeMissingAcceptsDarwinENOATTR(t *testing.T) {
	// Given
	err := unix.ENOATTR

	// When
	missing := isACLAttributeMissing(err)

	// Then
	require.True(t, missing)
}

func TestIsACLAttributeMissingRejectsDarwinPermissionError(t *testing.T) {
	// Given
	err := unix.EPERM

	// When
	missing := isACLAttributeMissing(err)

	// Then
	require.False(t, missing)
}

func TestACLAttributesAbsentAcceptsBothMissingDarwinACLs(t *testing.T) {
	// Given
	missingErr := unix.ENOATTR

	// When
	absent := aclAttributesAbsent(missingErr, missingErr)

	// Then
	require.True(t, absent)
}

func TestACLAttributesAbsentRejectsPresentDarwinACL(t *testing.T) {
	// Given
	missingErr := unix.ENOATTR

	// When
	absent := aclAttributesAbsent(missingErr, nil)

	// Then
	require.False(t, absent)
}
