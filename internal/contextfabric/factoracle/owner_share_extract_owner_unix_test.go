//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly || solaris

package factoracle

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// osFakeInfo replaces the owner record of a file.
type osFakeInfo struct {
	os.FileInfo
	sys any
}

func (f osFakeInfo) Sys() any { return f.sys }

// A test cannot make a 0600 file of another user it can still open, so the
// owner record is replaced: the same check LoadOwnershipExtract runs on the
// open file.
func TestOwnerShareExtractFileOfAnotherUserIsRefused(t *testing.T) {
	info, err := os.Stat(osWriteExtractFile(t, "x\n", 0o600, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwnershipExtractFile(info); err != nil {
		t.Fatalf("the running user's own 0600 file is refused: %v", err)
	}
	other := *info.Sys().(*syscall.Stat_t)
	other.Uid++
	if err := checkOwnershipExtractFile(osFakeInfo{FileInfo: info, sys: &other}); err == nil || !strings.Contains(err.Error(), "not owned by the user running the pair") {
		t.Fatalf("error %v, want a file of another user refused", err)
	}
	if err := checkOwnershipExtractFile(osFakeInfo{FileInfo: info, sys: nil}); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("error %v, want a file with no owner record refused", err)
	}
}
