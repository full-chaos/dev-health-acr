//go:build darwin || linux

package sidecar

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

const credentialLifecycleLockSubprocessEnvironment = "TEST_CREDENTIAL_LIFECYCLE_LOCK_SUBPROCESS"

func TestCredentialLifecycleLockRejectsUnsafeObjects(t *testing.T) {
	// Given
	cases := []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hardlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				source := path + ".source"
				if err := os.WriteFile(source, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(source, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "group readable file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o640); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "world writable file",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o602); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credential.lock")
			testCase.setup(t, path)

			// When
			closeLock, err := acquireCredentialLifecycleLockFile(path)

			// Then
			if closeLock != nil {
				t.Cleanup(func() { _ = closeLock() })
				t.Fatal("unsafe lock object acquired")
			}
			if !errors.Is(err, errCredentialLifecycleLockUnsafe) {
				t.Fatalf("acquire error = %v, want errCredentialLifecycleLockUnsafe", err)
			}
		})
	}
}

func TestCredentialLifecycleLockFileMetadataRequiresRegularOwnedPrivateUnlinkedFile(t *testing.T) {
	// Given
	safe := syscall.Stat_t{Mode: syscall.S_IFREG | 0o600, Uid: uint32(os.Geteuid()), Nlink: 1}
	cases := []struct {
		name string
		stat syscall.Stat_t
	}{
		{name: "regular file", stat: safe},
		{name: "foreign owner", stat: func() syscall.Stat_t { stat := safe; stat.Uid++; return stat }()},
		{name: "directory", stat: func() syscall.Stat_t { stat := safe; stat.Mode = syscall.S_IFDIR | 0o700; return stat }()},
		{name: "world readable", stat: func() syscall.Stat_t { stat := safe; stat.Mode |= 0o004; return stat }()},
		{name: "multiple links", stat: func() syscall.Stat_t { stat := safe; stat.Nlink = 2; return stat }()},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			safe := credentialLifecycleLockFileMetadataIsSafe(testCase.stat)

			// Then
			if safe != (testCase.name == "regular file") {
				t.Fatalf("metadata safety = %t for %s", safe, testCase.name)
			}
		})
	}
}

func TestCredentialLifecycleLockParentValidation(t *testing.T) {
	// Given
	tests := []struct {
		name  string
		mode  os.FileMode
		owner uint32
		want  error
	}{
		{"safe sticky root directory", os.ModeDir | os.ModeSticky | 0o777, 0, nil},
		{"root owned nonsticky directory", os.ModeDir | 0o755, 0, errCredentialLifecycleLockUnsafe},
		{"nonroot sticky directory", os.ModeDir | os.ModeSticky | 0o777, 1, errCredentialLifecycleLockUnsafe},
		{"symlink", os.ModeSymlink | os.ModeSticky | 0o777, 0, errCredentialLifecycleLockUnsafe},
		{"regular file", 0o600, 0, errCredentialLifecycleLockUnsafe},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// When
			err := validateCredentialLifecycleLockParentMetadata(testCase.mode, testCase.owner)

			// Then
			if !errors.Is(err, testCase.want) {
				t.Fatalf("validation error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestCredentialLifecycleLockRejectsSymlinkedParent(t *testing.T) {
	// Given
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink("/var/tmp", parent); err != nil {
		t.Fatal(err)
	}

	// When
	closeLock, err := acquireCredentialLifecycleLockAt(filepath.Join(parent, "credential.lock"))

	// Then
	if closeLock != nil {
		t.Cleanup(func() { _ = closeLock() })
		t.Fatal("lock acquired through symlinked parent")
	}
	if !errors.Is(err, errCredentialLifecycleLockUnsafe) {
		t.Fatalf("acquire error = %v, want errCredentialLifecycleLockUnsafe", err)
	}
}

func TestCredentialLifecycleLockUsesCanonicalVarTmpPath(t *testing.T) {
	// Given
	t.Setenv("TMPDIR", t.TempDir())
	path := filepath.Join("/var/tmp", "acr-credential-lifecycle-"+strconv.Itoa(os.Geteuid())+".lock")

	// When
	closeLock, err := acquireCredentialLifecycleLock()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = closeLock()
		_ = os.Remove(path)
	})
	_, err = os.Stat(path)

	// Then
	if err != nil {
		t.Fatalf("canonical lifecycle lock path %q was not created: %v", path, err)
	}
}

func TestCredentialLifecycleLockContendsAcrossProcessesWithDifferentTMPDIR(t *testing.T) {
	if os.Getenv(credentialLifecycleLockSubprocessEnvironment) == "1" {
		// Given

		// When
		session, err := BeginCredentialLifecycleSession()

		// Then
		if session != nil || !errors.Is(err, errCredentialLifecycleBusy) {
			t.Fatalf("subprocess session = %v, %v; want busy", session, err)
		}
		return
	}

	// Given
	t.Setenv("TMPDIR", t.TempDir())
	session, err := BeginCredentialLifecycleSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestCredentialLifecycleLockContendsAcrossProcessesWithDifferentTMPDIR$")
	command.Env = append(os.Environ(), credentialLifecycleLockSubprocessEnvironment+"=1", "TMPDIR="+t.TempDir())

	// When
	output, err := command.CombinedOutput()

	// Then
	if err != nil {
		t.Fatalf("subprocess contention test failed: %v\n%s", err, output)
	}
}
