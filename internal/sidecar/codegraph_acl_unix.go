//go:build darwin || linux

package sidecar

import (
	"errors"

	"golang.org/x/sys/unix"
)

var codeGraphACLCheck = codeGraphRootHasOnlyBaseACL

func codeGraphRootHasOnlyBaseACL(path string) bool {
	name := "system.posix_acl_access"
	if err := aclAbsent(path, name); err == nil {
		return true
	}
	if err := aclAbsent(path, "com.apple.macl"); err == nil {
		return true
	}
	return false
}

func aclAbsent(path, name string) error {
	_, err := unix.Getxattr(path, name, nil)
	if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOATTR) {
		return nil
	}
	return err
}
