//go:build darwin

package sidecar

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isACLAttributeMissing(err error) bool {
	return errors.Is(err, unix.ENOATTR)
}
