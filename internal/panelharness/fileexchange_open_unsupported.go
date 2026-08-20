//go:build !darwin && !linux

package panelharness

import (
	"errors"
	"fmt"
	"os"
	"runtime"
)

// ErrExchangeReadsUnsupported mirrors internal/sidecar's own
// ErrBoundedFileReadsUnsupported precisely: a structural, platform-wide
// refusal, not an ambiguous per-file condition.
var ErrExchangeReadsUnsupported = errors.New("panelharness: bounded file-exchange reads are not supported on this platform")

// openNoFollowNonBlocking fails closed on every platform other than macOS
// and Linux -- mirrors internal/sidecar/boundedfile_unsupported.go's own
// platform gate and its reasoning exactly: this package's O_NOFOLLOW/
// O_NONBLOCK guarantees are only verified on darwin/linux, so a build for
// any other GOOS refuses every file-exchange response read outright
// rather than falling back to a TOCTOU-vulnerable pattern.
func openNoFollowNonBlocking(_ string) (*os.File, error) {
	return nil, fmt.Errorf("%w (%s)", ErrExchangeReadsUnsupported, runtime.GOOS)
}
