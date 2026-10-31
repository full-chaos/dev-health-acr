//go:build unix

package main

import (
	"io/fs"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReleaseSourceRoot_copies_canonical_modes_under_any_umask(t *testing.T) {
	for _, mask := range []int{0o002, 0o022, 0o077} {
		previous := syscall.Umask(mask)
		root := releaseSourceRoot(t)
		syscall.Umask(previous)

		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return walkErr
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if mode := info.Mode().Perm(); mode != 0o644 && mode != 0o755 {
				t.Errorf("umask %04o: %s has mode %04o, want 0644 or 0755", mask, path, mode)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("umask %04o: walk copied root: %v", mask, err)
		}
	}
}
