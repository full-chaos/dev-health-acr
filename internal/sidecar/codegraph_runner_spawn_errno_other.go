//go:build !darwin && !linux

package sidecar

// This build tag (!darwin && !linux) covers more than Windows -- it also
// matches the BSDs and other POSIX-like unixes this repo does not target
// (see the Makefile: only darwin, linux, and windows are built/vetted).
// Those platforms' syscall packages likely DO define EAGAIN/EMFILE/ENOMEM
// the same way darwin/linux do; this file's `false` here is deliberately
// conservative for ALL of them, not a claim that they lack the errno
// semantics. What genuinely differs is Windows' CreateProcess failure
// model, which does not map to POSIX fork/exec errno at all -- that is
// the platform this fallback exists FOR.
//
// isTransientCodeGraphSpawnErrno never classifies a spawn failure as
// host-resource-transient here. A cmd.Start()/cmd.StdoutPipe() failure
// falls through classifyCodeGraphSpawnError's default case: propagated
// wrapped and truthful, just not classified as either
// errCodeGraphExecutableAbsent or errCodeGraphSpawnUnavailable. See
// credential_keyring_procgroup_other.go for the established pattern this
// file follows.
func isTransientCodeGraphSpawnErrno(err error) bool { return false }

// isCodeGraphExecFormatError never classifies a spawn failure as an
// ENOEXEC-shaped broken binary on a platform without that POSIX errno
// (sol review F1, CHAOS-3861). Falls through classifyCodeGraphSpawnError's
// default case, same reasoning as isTransientCodeGraphSpawnErrno above.
func isCodeGraphExecFormatError(err error) bool { return false }
