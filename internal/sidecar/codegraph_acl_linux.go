//go:build linux

package sidecar

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isACLAttributeMissing(err error) bool {
	return errors.Is(err, unix.ENODATA)
}

func codeGraphRootHasOnlyBaseACL(path string) bool {
	_, err := unix.Getxattr(path, "system.posix_acl_access", nil)
	return isACLAttributeMissing(err)
}
