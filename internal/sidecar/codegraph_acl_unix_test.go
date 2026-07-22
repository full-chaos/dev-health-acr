//go:build darwin || linux

package sidecar

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestACLAttributesAbsentRejectsSuccessfulLookup(t *testing.T) {
	// Given
	lookupErr := errors.New("missing attribute")

	// When
	absent := aclAttributesAbsent(nil, lookupErr)

	// Then
	require.False(t, absent)
}

func TestACLAttributesAbsentRejectsPermissionErrors(t *testing.T) {
	// Given
	permissionErr := errors.New("permission denied")

	// When
	absent := aclAttributesAbsent(permissionErr, permissionErr)

	// Then
	require.False(t, absent)
}

func TestACLAttributesAbsentRejectsUnrelatedErrors(t *testing.T) {
	// Given
	lookupErr := errors.New("filesystem failure")

	// When
	absent := aclAttributesAbsent(lookupErr, lookupErr)

	// Then
	require.False(t, absent)
}
