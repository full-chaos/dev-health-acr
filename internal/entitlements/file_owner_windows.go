//go:build windows

package entitlements

import "os"

func isPrivateSecret(os.FileInfo) bool { return false }
