//go:build !darwin && !linux

package sidecar

import (
	"os"
)

func openCodeGraphDatabase(_ string) (*os.File, error) {
	return nil, errCodeGraphMissing
}
