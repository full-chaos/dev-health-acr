package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Confined file access (sol r1 M6). Every path below the data root is
// walked component by component with openat(O_NOFOLLOW|O_DIRECTORY), so no
// symlink anywhere below the root is followed, and every file operation is
// relative to a directory descriptor. The root itself is resolved once.

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// validName is the rule for every path component the runner derives: run
// ids, set names, artifact names. No separators, no "..", no leading dot.
func validName(name string) error {
	if !namePattern.MatchString(name) || strings.Contains(name, "..") {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

type cdir struct {
	fd   int
	path string
}

func openRoot(path string) (*cdir, error) {
	if path == "" {
		return nil, errors.New("--data-root is required")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("data root: %w", err)
	}
	fd, err := unix.Open(resolved, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open data root: %w", err)
	}
	return &cdir{fd: fd, path: resolved}, nil
}

func (d *cdir) close() {
	if d != nil && d.fd >= 0 {
		_ = unix.Close(d.fd)
		d.fd = -1
	}
}

// sub opens (optionally creating, mode 0700) one directory component. The
// directory must not be a symlink and must not be accessible by group or
// others.
func (d *cdir) sub(name string, create bool) (*cdir, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(d.fd, name, flags, 0)
	if errors.Is(err, unix.ENOENT) && create {
		if mkErr := unix.Mkdirat(d.fd, name, 0o700); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
			return nil, fmt.Errorf("create %s: %w", name, mkErr)
		}
		fd, err = unix.Openat(d.fd, name, flags, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("open directory %s (symlinks are refused): %w", filepath.Join(d.path, name), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	// Directories the runner creates are 0700. Directories the Python side
	// creates (data/review, data/heldout) may be 0755: only group or other
	// WRITE access is refused, since that could swap files under the runner.
	if st.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("directory %s must not be writable by group or others", filepath.Join(d.path, name))
	}
	return &cdir{fd: fd, path: filepath.Join(d.path, name)}, nil
}

// walk opens a chain of components below d.
func (d *cdir) walk(create bool, names ...string) (*cdir, error) {
	cur := d
	for i, n := range names {
		next, err := cur.sub(n, create)
		if i > 0 {
			cur.close()
		}
		if err != nil {
			return nil, err
		}
		cur = next
	}
	if cur == d {
		return nil, errors.New("empty path")
	}
	return cur, nil
}

// openFile opens a regular, private file without following symlinks.
func (d *cdir) openFile(name string, flags int, create bool) (*os.File, error) {
	if err := validName(name); err != nil {
		return nil, err
	}
	all := flags | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if create {
		all |= unix.O_CREAT
	}
	fd, err := unix.Openat(d.fd, name, all, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s (symlinks are refused): %w", filepath.Join(d.path, name), err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	// Files the runner creates are 0600. Files the Python side writes
	// (seals.jsonl, heldout-openings.jsonl) are read if they are regular
	// and not writable by group or others.
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o022 != 0 {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("%s must be a regular file not writable by group or others", filepath.Join(d.path, name))
	}
	return os.NewFile(uintptr(fd), filepath.Join(d.path, name)), nil
}

func (d *cdir) readFile(name string) ([]byte, error) {
	f, err := d.openFile(name, unix.O_RDONLY, false)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func (d *cdir) exists(name string) bool {
	var st unix.Stat_t
	return unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil
}

func (d *cdir) sync() error { return unix.Fsync(d.fd) }

func (d *cdir) tempName(prefix string) (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(nonce[:]), nil
}

func (d *cdir) writeTemp(data []byte, prefix string) (string, error) {
	name, err := d.tempName(prefix)
	if err != nil {
		return "", err
	}
	fd, err := unix.Openat(d.fd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), name)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(d.fd, name, 0)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = unix.Unlinkat(d.fd, name, 0)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = unix.Unlinkat(d.fd, name, 0)
		return "", err
	}
	return name, nil
}

// publishWriteOnce: temp file, fsync, linkat (atomic, no-replace), unlink
// temp, fsync the directory. A plain rename is never used here.
func (d *cdir) publishWriteOnce(name string, data []byte, fault func(string) error) error {
	if err := validName(name); err != nil {
		return err
	}
	temp, err := d.writeTemp(data, ".tmp-")
	if err != nil {
		return err
	}
	if err := fault("after_temp_write"); err != nil {
		return err
	}
	if err := unix.Linkat(d.fd, temp, d.fd, name, 0); err != nil {
		_ = unix.Unlinkat(d.fd, temp, 0)
		return fmt.Errorf("publish %s: %w", name, err)
	}
	if err := fault("after_link"); err != nil {
		return err
	}
	if err := unix.Unlinkat(d.fd, temp, 0); err != nil {
		return err
	}
	return d.sync()
}

// replaceDerived regenerates a derived file: the only replacement allowed.
func (d *cdir) replaceDerived(name string, data []byte) error {
	if err := validName(name); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%s is not a regular file", name)
	}
	temp, err := d.writeTemp(data, ".tmp-derived-")
	if err != nil {
		return err
	}
	if err := unix.Renameat(d.fd, temp, d.fd, name); err != nil {
		_ = unix.Unlinkat(d.fd, temp, 0)
		return err
	}
	return d.sync()
}

// removeTemps drops left-over temp files; they are never canonical.
func (d *cdir) removeTemps() error {
	f, err := unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(f), d.path)
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return err
	}
	for _, n := range names {
		if strings.HasPrefix(n, ".tmp-") {
			if err := unix.Unlinkat(d.fd, n, 0); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *cdir) listNames() ([]string, error) {
	f, err := unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(f), d.path)
	defer dir.Close()
	return dir.Readdirnames(-1)
}

// lockFile takes an exclusive flock with a bounded wait.
func lockFile(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("lock %s: %w", filepath.Base(f.Name()), err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
