//go:build !darwin && !linux

package sidecar

import "os"

func trustedCodeGraphGroupWritableRoot(os.FileInfo) bool {
	return false
}
