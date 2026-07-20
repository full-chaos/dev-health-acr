//go:build darwin || linux

package sidecar

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureKeyringProcessGroup starts cmd's child as the leader of a new
// process group (Setpgid) so killKeyringProcessGroup can later terminate
// the whole process tree a keyring backend spawns, not just that
// top-level process. This matters because the immediate child (for
// example the POSIX shell this package's own tests drive directly, or a
// real `security`/`secret-tool` backend that forks a helper of its own)
// may fork a genuine child process rather than exec-replacing itself,
// and that descendant inherits the same stdout/stderr pipe file
// descriptors runKeyringCommand reads from: as long as any process
// anywhere still holds the pipe's write end open, a read on the
// parent's end blocks for that descendant's own lifetime, not the
// immediate child's, regardless of whether the immediate child itself
// has already been killed (see https://go.dev/issue/23019 for the same
// class of problem upstream in os/exec). Without a distinct process
// group, the child instead inherits this Go process's own group, so
// signaling "everything in the child's group" would also reach this
// process itself; Setpgid is what makes the group safe to target
// exclusively.
func configureKeyringProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killKeyringProcessGroup sends SIGKILL to every process in cmd's own
// process group -- the negative of cmd.Process's PID, since
// configureKeyringProcessGroup made it the group leader -- rather than
// only cmd.Process itself, so a descendant the backend forked (see
// configureKeyringProcessGroup's doc comment) is terminated too instead
// of being orphaned to keep running, and keep the pipe open, for
// however long it likes.
func killKeyringProcessGroup(cmd *exec.Cmd) error {
	return killKeyringProcessGroupID(captureKeyringProcessGroup(cmd))
}

func captureKeyringProcessGroup(cmd *exec.Cmd) int {
	if cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func killKeyringProcessGroupID(processGroup int) error {
	if processGroup <= 0 {
		return nil
	}
	err := syscall.Kill(-processGroup, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
