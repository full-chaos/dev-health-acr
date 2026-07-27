package sidecar

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// ErrBoundedFileReadsUnsupported is returned by openNoFollowNonBlocking on any
// platform other than darwin and linux (see boundedfile_unsupported.go). It is
// a structural, platform-wide refusal -- every call on such a platform returns
// it, for every path, whether or not anything is actually there -- not an
// ambiguous per-file condition like a permission error or a locked keyring.
// Callers for whom that distinction matters (credential.go's loadCredentialFile
// in particular: a platform that can never write or delete a credential file
// either has nothing there this sidecar could have put) check for it
// specifically rather than treating it as an unexplained read failure.
var ErrBoundedFileReadsUnsupported = errors.New("bounded local file reads are not supported on this platform")

// readBoundedRegularFile opens path and returns at most maxBytes+1 bytes
// from it, having verified that it names a regular file: never a
// directory, FIFO, device, socket, or symlink -- even one that would
// otherwise resolve to a legitimate regular file. It is the one shared
// implementation behind every security-sensitive local file read in this
// package (the CA bundle in api_client.go/config.go and the token file in
// credential.go), so there is exactly one bounded-read implementation to
// audit instead of several that could silently diverge.
//
// The path is opened by the platform-specific openNoFollowNonBlocking
// (boundedfile_unix.go / boundedfile_unsupported.go), which sets
// O_NOFOLLOW and O_NONBLOCK as part of the single open(2) syscall that
// obtains the descriptor -- there is no separate pre-open check (an
// lstat, for instance) and therefore no window between "check" and "open"
// for an attacker to swap the path's target: the kernel itself refuses to
// open a path whose last component is a symlink, and refuses to block
// inside open(2) waiting for a FIFO writer that may never arrive. The
// fstat(2) below then runs against that already-open descriptor, so
// nothing that happens to the path afterward (a symlink retarget, a file
// replace) can change what this function fstats or reads: the type check
// closes the same TOCTOU gap a separate lstat-then-open pattern cannot.
//
// The size bound is enforced twice: once against the fstat(2) size before
// reading (a fast-path rejection for an already-oversized file) and once
// against the actual bytes read via io.LimitReader(f, maxBytes+1), so a
// file grown after the fstat still cannot exceed maxBytes.
//
// The returned os.FileInfo is the fstat(2) result on the open descriptor,
// so a caller with an additional type-specific requirement (such as
// credential.go's group/world-permission enforcement) can apply it
// without a third stat(2) call of its own.
func readBoundedRegularFile(path string, maxBytes int64) ([]byte, os.FileInfo, error) {
	f, err := openNoFollowNonBlocking(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("must be a regular file, not %s", info.Mode().Type())
	}
	if info.Size() > maxBytes {
		return nil, nil, fmt.Errorf("exceeds the maximum size of %d bytes", maxBytes)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, nil, fmt.Errorf("exceeds the maximum size of %d bytes", maxBytes)
	}
	return data, info, nil
}

// describeFileError converts a readBoundedRegularFile error into a
// *ConfigError safe to surface verbatim in an operator-facing
// diagnostic (notably `acr-mcp doctor`'s JSON output): it names the
// failure category but never echoes the filesystem path itself, which
// this package treats as potentially sensitive (home-directory
// usernames, deployment layout).
//
// readBoundedRegularFile's own type/size errors are already static and
// path-free (e.g. "must be a regular file, not p---------", "exceeds the
// maximum size of N bytes"); those are not *fs.PathError and pass through
// unchanged, just namespaced under field. Only a genuine *fs.PathError
// from the underlying openNoFollowNonBlocking/f.Stat calls -- which
// embeds the path in its Error() text -- is collapsed to a fixed,
// path-free description here.
func describeFileError(field string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return &ConfigError{Field: field, Detail: "no such file"}
		case errors.Is(err, fs.ErrPermission):
			return &ConfigError{Field: field, Detail: "permission denied"}
		default:
			return &ConfigError{Field: field, Detail: "could not be accessed"}
		}
	}
	return &ConfigError{Field: field, Detail: err.Error()}
}
