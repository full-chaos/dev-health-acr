//go:build !darwin && !linux

package sidecar

// isTransientCodeGraphSpawnErrno never classifies a spawn failure as
// host-resource-transient on a platform without POSIX fork/exec errno
// semantics (EAGAIN/EMFILE/ENOMEM do not apply the same way to Windows'
// CreateProcess failure modes). A cmd.Start()/cmd.StdoutPipe() failure
// there falls through classifyCodeGraphSpawnError's default case:
// propagated wrapped and truthful, just not classified as either
// errCodeGraphExecutableAbsent or errCodeGraphSpawnUnavailable. See
// credential_keyring_procgroup_other.go for the established pattern this
// file follows.
func isTransientCodeGraphSpawnErrno(err error) bool { return false }
