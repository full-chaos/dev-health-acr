//go:build darwin || linux

package releasebuild

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openNoFollow(name string) (*os.File, error) {
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open no-follow: %w", err)
	}
	return os.NewFile(uintptr(fd), name), nil
}
