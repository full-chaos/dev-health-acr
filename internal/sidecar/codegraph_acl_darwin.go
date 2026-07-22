//go:build darwin

package sidecar

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isACLAttributeMissing(err error) bool {
	return errors.Is(err, unix.ENOATTR)
}

func codeGraphRootHasOnlyBaseACL(path string) bool {
	_, posixACLErr := unix.Getxattr(path, "system.posix_acl_access", nil)
	_, maclErr := unix.Getxattr(path, "com.apple.macl", nil)
	return aclAttributesAbsent(posixACLErr, maclErr)
}

func aclAttributesAbsent(posixACLErr, maclErr error) bool {
	return isACLAttributeMissing(posixACLErr) && isACLAttributeMissing(maclErr)
}
