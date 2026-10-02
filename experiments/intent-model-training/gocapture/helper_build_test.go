//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// builtHelper is the interpretation helper the tests run. TestMain builds it
// from ../gohelper for every test run, so the tests never depend on a
// binary that someone built earlier, and a helper that does not build fails
// the run: a measurement that did not happen never passes.
var builtHelper string

func TestMain(m *testing.M) {
	os.Exit(runWithHelper(m))
}

func runWithHelper(m *testing.M) int {
	dir, err := os.MkdirTemp("", "interp-helper-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "gocapture tests: temp dir for the helper: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "interp-helper")
	build := exec.Command("go", "build", "-o", bin, "../gohelper")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "gocapture tests: the interpretation helper does not build: %v\n%s", err, out)
		return 1
	}
	builtHelper = bin
	return m.Run()
}
