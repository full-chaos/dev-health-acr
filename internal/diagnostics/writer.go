package diagnostics

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrDestinationInvalid is returned by WriteBundle for every fail-closed
// destination check below: an empty path, an oversized payload, a missing
// or non-directory parent, a symlinked parent or destination, or a
// destination path that already exists as any type -- regular file,
// directory, or symlink. Every case fails closed -- none of them fall
// back to a "best effort" write, and none silently replace whatever was
// already at the destination.
var ErrDestinationInvalid = errors.New("diagnostics: destination path is invalid")

// WriteBundle atomically writes data (the output of Build) to the
// caller-supplied explicit path: a temporary file is created in the same
// directory with mode 0600, written, and synced, then finalized into
// place with a no-clobber hard link (see atomicWrite) so a reader never
// observes a partially-written bundle and a pre-existing destination --
// of any type, created at any point up to and including the finalization
// instant -- is never silently overwritten. It refuses to follow or
// replace a symlink at either the parent directory or the destination
// path itself, and bounds the payload size defensively even though Build
// already enforces MaxBundleBytes.
func WriteBundle(path string, data []byte) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: path must not be empty", ErrDestinationInvalid)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: bundle data must not be empty", ErrDestinationInvalid)
	}
	if len(data) > MaxBundleBytes {
		return fmt.Errorf("%w: bundle of %d bytes exceeds the %d byte bound", ErrDestinationInvalid, len(data), MaxBundleBytes)
	}

	dir := filepath.Dir(path)
	if err := validateDirectoryPath(dir); err != nil {
		return err
	}

	// This Lstat is a fast-path check only, for a clear, categorized error
	// message in the common non-racing case; it does not by itself
	// guarantee no-clobber (a check here followed by an unconditional
	// os.Rename would still race against a concurrent writer). The
	// authoritative, race-free guarantee is atomicWrite's no-clobber hard-
	// link finalization below, which fails closed regardless of what --
	// if anything -- shows up at path between this check and that call.
	if destInfo, err := os.Lstat(path); err == nil {
		switch {
		case destInfo.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("%w: refusing to overwrite a symlink at the destination path", ErrDestinationInvalid)
		case destInfo.IsDir():
			return fmt.Errorf("%w: destination path is a directory", ErrDestinationInvalid)
		default:
			return fmt.Errorf("%w: destination path already exists", ErrDestinationInvalid)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: destination path is not accessible: %v", ErrDestinationInvalid, err)
	}

	return atomicWrite(dir, path, data)
}

// atomicWrite performs the create-temp/write/sync/no-clobber-finalize
// sequence. The temporary file is always removed afterward -- on any
// write failure it is the only copy and must not leak into dir; on
// success, path is a second directory entry (a hard link) pointing at
// the same inode, so removing the temp name leaves exactly one entry,
// at path, with the written content.
func atomicWrite(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".acr-diagnostics-*.tmp")
	if err != nil {
		return fmt.Errorf("diagnostics: create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("diagnostics: set temporary file permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("diagnostics: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("diagnostics: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("diagnostics: close temporary file: %w", err)
	}
	if err := validateDirectoryPath(dir); err != nil {
		return err
	}

	// os.Link is the single syscall that decides "no clobber": it creates
	// path as a new directory entry aliasing tmpPath's already-written,
	// already-synced inode only if path does not already exist, and fails
	// with ErrExist otherwise -- atomically, with no separate check step
	// for a concurrent writer (or an attacker) to race between. Whatever
	// shows up at path between WriteBundle's earlier Lstat fast-path check
	// and this call -- another writer's finished bundle, a regular file, a
	// directory, or a symlink -- this fails closed rather than silently
	// overwriting or following it. Because path and tmpPath share an
	// inode, path also inherits tmp's already-applied 0600 permissions
	// with no separate chmod needed.
	if err := os.Link(tmpPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: destination path already exists", ErrDestinationInvalid)
		}
		return fmt.Errorf("diagnostics: finalize bundle: %w", err)
	}
	return nil
}

func validateDirectoryPath(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("%w: resolve destination directory: %v", ErrDestinationInvalid, err)
	}
	volume := filepath.VolumeName(abs)
	root := volume + string(filepath.Separator)
	remaining := strings.TrimPrefix(abs, root)
	current := root
	for _, component := range strings.Split(remaining, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return fmt.Errorf("%w: destination directory is not accessible: %v", ErrDestinationInvalid, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: destination directory contains a symlink", ErrDestinationInvalid)
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: destination directory component is not a directory", ErrDestinationInvalid)
		}
	}
	return nil
}
