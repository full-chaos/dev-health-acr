//go:build darwin || linux

package sidecar

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var credentialDirectorySync = unix.Fsync

func writeCredentialFile(path, token string) error {
	parent := filepath.Dir(path)
	if err := ensureCredentialParent(parent); err != nil {
		return err
	}
	directory, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("open credential directory: %w", err)
	}
	defer unix.Close(directory)
	name := filepath.Base(path)
	if err := rejectCredentialSymlink(directory, name); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	temporary, err := credentialTemporaryName()
	if err != nil {
		return err
	}
	fileDescriptor, err := unix.Openat(directory, temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("create credential temporary file: %w", err)
	}
	file := os.NewFile(uintptr(fileDescriptor), temporary)
	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(directory, temporary, 0)
	}()
	if _, err := io.WriteString(file, token+"\n"); err != nil {
		return fmt.Errorf("write credential temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync credential temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close credential temporary file: %w", err)
	}
	if err := unix.Renameat(directory, temporary, directory, name); err != nil {
		return fmt.Errorf("replace credential file: %w", err)
	}
	if err := credentialDirectorySync(directory); err != nil {
		// The rename above already published the new credential at the
		// target name, so this failure leaves a readable credential file
		// behind even though the write is reported as failed. Tag it so
		// PersistCredential can hand its caller the exact locator to purge.
		return fmt.Errorf("%w: sync credential directory: %w", errCredentialWriteAmbiguous, err)
	}
	return nil
}

func removeCredentialFile(path string) error {
	parent := filepath.Dir(path)
	directory, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open credential directory: %w", err)
	}
	defer unix.Close(directory)
	name := filepath.Base(path)
	if err := rejectCredentialSymlink(directory, name); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if err := unix.Unlinkat(directory, name, 0); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return fmt.Errorf("remove credential file: %w", err)
	}
	if err := credentialDirectorySync(directory); err != nil {
		return fmt.Errorf("sync credential directory: %w", err)
	}
	return nil
}

// rejectSharedWritableCredentialParent refuses to treat a credential file as
// removable when its parent directory grants write access to group or world.
//
// Removal proves the target is an ACR credential (no-follow open, regular-file
// fstat, restrictive mode, ACR token shape) and then unlinks it by name. Those
// are two operations on a path, so a window exists between them no matter what
// this function does.
//
// What this narrows is who can act in that window. On a group- or
// world-writable parent, any local user can replace the entry between the proof
// and the unlink, which turns removal into the arbitrary-file delete the
// content proof exists to prevent, reached through the directory instead of the
// path. Refusing such a parent leaves only principals who can already write the
// directory -- the owner, and root. It does NOT make the unlink atomic with
// respect to the check, and nothing here should be read as claiming it does:
// this is a reduction of the attacker set, not the elimination of a race.
//
// A parent that does not exist is not a failure: there is nothing to remove,
// and removeCredentialFile already treats that as the goal state.
func rejectSharedWritableCredentialParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect credential directory before removal: %w", err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("credential directory grants group or world write access; another local user could replace the credential between the pre-removal check and the unlink")
	}
	return nil
}

// ensureCredentialParent guarantees the credential file's parent directory is
// a real, non-symlinked directory that no other local user can write into.
//
// Only a directory this function created is chmod-ed. The parent is an
// operator-supplied location -- ACR_API_TOKEN_FILE can point anywhere -- and
// unconditionally rewriting its mode changed a directory ACR does not own:
// pointing the token file at $HOME/token silently reduced the entire home
// directory to 0700. A pre-existing parent is inspected instead: group- or
// world-writable is refused outright, since any local user could then swap
// the credential file, while every other mode bit is left exactly as found.
func ensureCredentialParent(parent string) error {
	info, err := os.Lstat(parent)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(parent, 0o700); err != nil {
			return fmt.Errorf("create credential directory: %w", err)
		}
		if err := os.Chmod(parent, 0o700); err != nil {
			return fmt.Errorf("restrict credential directory: %w", err)
		}
		info, err = os.Lstat(parent)
	}
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential directory must be a real directory")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("credential directory must not grant group or world write access")
	}
	return nil
}

func rejectCredentialSymlink(directory int, name string) error {
	var info unix.Stat_t
	err := unix.Fstatat(directory, name, &info, unix.AT_SYMLINK_NOFOLLOW)
	if err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT == unix.S_IFLNK {
		return errors.New("credential file must not be a symlink")
	}
	if info.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("credential file must be regular")
	}
	return nil
}

func credentialTemporaryName() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate credential temporary name: %w", err)
	}
	return fmt.Sprintf(".token.%x", random), nil
}
