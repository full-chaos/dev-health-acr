package diagnostics

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteBundleRejectsNestedSymlinkParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on Windows")
	}
	// Given
	root := realTempDir(t)
	target := filepath.Join(root, "target", "nested")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "outer", "link")
	if err := os.Mkdir(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target"), link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(link, "nested", "bundle.tar")

	// When
	err := WriteBundle(path, []byte("bundle"))

	// Then
	if !errors.Is(err, ErrDestinationInvalid) {
		t.Fatalf("WriteBundle() error = %v, want ErrDestinationInvalid", err)
	}
	if _, statErr := os.Stat(filepath.Join(target, "bundle.tar")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bundle was written through nested symlink: %v", statErr)
	}
}

func realTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	return path
}
