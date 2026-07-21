//go:build windows

package nativeadapters

import "os/exec"

func configureProcess(*exec.Cmd)    {}
func stopProcess(command *exec.Cmd) { _ = command.Process.Kill() }
func killProcess(command *exec.Cmd) { _ = command.Process.Kill() }
