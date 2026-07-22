//go:build !darwin && !linux

package releasebuild

import (
	"fmt"
	"os"
)

func openNoFollow(string) (*os.File, error) {
	return nil, fmt.Errorf("secure no-follow opening is unavailable on this host")
}
