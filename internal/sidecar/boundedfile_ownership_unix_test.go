//go:build darwin || linux

package sidecar

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// fakeFileInfo is a minimal os.FileInfo whose Sys() returns a caller-chosen
// *syscall.Stat_t, so verifyTrustedCABundleOwnership can be unit tested
// against ownership shapes (a foreign uid, in particular) that cannot be
// produced deterministically and portably by chown-ing a real temp file
// without root privileges.
type fakeFileInfo struct {
	mode os.FileMode
	stat syscall.Stat_t
}

func (f fakeFileInfo) Name() string       { return "ca.pem" }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return &f.stat }

func TestVerifyTrustedCABundleOwnershipAcceptsCurrentUser(t *testing.T) {
	info := fakeFileInfo{mode: 0o600, stat: syscall.Stat_t{Uid: uint32(os.Geteuid())}}
	if err := verifyTrustedCABundleOwnership(info); err != nil {
		t.Fatalf("current-user-owned bundle was rejected: %v", err)
	}
}

func TestVerifyTrustedCABundleOwnershipAcceptsRoot(t *testing.T) {
	info := fakeFileInfo{mode: 0o600, stat: syscall.Stat_t{Uid: 0}}
	if err := verifyTrustedCABundleOwnership(info); err != nil {
		t.Fatalf("root-owned bundle was rejected: %v", err)
	}
}

// TestVerifyTrustedCABundleOwnershipRejectsForeignOwner is the canary for
// "require trusted ownership": a bundle owned by neither the current
// effective user nor root must never be trusted, even when its
// permissions grant no group/world access at all.
func TestVerifyTrustedCABundleOwnershipRejectsForeignOwner(t *testing.T) {
	foreignUID := uint32(os.Geteuid()) + 54321
	info := fakeFileInfo{mode: 0o600, stat: syscall.Stat_t{Uid: foreignUID}}
	if err := verifyTrustedCABundleOwnership(info); err == nil {
		t.Fatal("a foreign-owned CA bundle was accepted")
	}
}

// TestVerifyTrustedCABundleOwnershipRejectsGroupWritable is the canary for
// "reject group/world-writable anchors": a group-writable mode must be
// rejected even when the owner is fully trusted.
func TestVerifyTrustedCABundleOwnershipRejectsGroupWritable(t *testing.T) {
	info := fakeFileInfo{mode: 0o620, stat: syscall.Stat_t{Uid: uint32(os.Geteuid())}}
	if err := verifyTrustedCABundleOwnership(info); err == nil {
		t.Fatal("a group-writable CA bundle was accepted")
	}
}

func TestVerifyTrustedCABundleOwnershipRejectsWorldWritable(t *testing.T) {
	info := fakeFileInfo{mode: 0o602, stat: syscall.Stat_t{Uid: uint32(os.Geteuid())}}
	if err := verifyTrustedCABundleOwnership(info); err == nil {
		t.Fatal("a world-writable CA bundle was accepted")
	}
}

func TestVerifyTrustedCABundleOwnershipAcceptsGroupOrWorldReadable(t *testing.T) {
	// Group/world *read* access is not the invariant this check enforces
	// (only write access lets another party change trusted certificates);
	// a bundle that is merely world-readable, as many real CA bundles are,
	// must not be rejected.
	info := fakeFileInfo{mode: 0o644, stat: syscall.Stat_t{Uid: uint32(os.Geteuid())}}
	if err := verifyTrustedCABundleOwnership(info); err != nil {
		t.Fatalf("a world-readable-only CA bundle was rejected: %v", err)
	}
}
