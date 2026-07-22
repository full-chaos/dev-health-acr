//go:build !darwin && !linux

package sidecar

import "os/exec"

// configureKeyringProcessGroup is a no-op on platforms without POSIX
// process groups. defaultKeyringLookup never calls runKeyringCommand on
// these platforms in the first place (see its switch on runtime.GOOS),
// so this exists only so the package still compiles there.
func configureKeyringProcessGroup(cmd *exec.Cmd) {}

// killKeyringProcessGroup falls back to killing only cmd.Process itself
// on platforms without POSIX process groups; see
// configureKeyringProcessGroup's doc comment for why this path is
// unreachable in production today.
func killKeyringProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func captureKeyringProcessGroup(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func killKeyringProcessGroupID(_ int) error { return nil }
