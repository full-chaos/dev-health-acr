//go:build darwin || linux

package sidecar

import (
	"errors"
	"io/fs"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestClassifyCodeGraphSpawnError_PinsTheClassificationBoundary is the
// CHAOS-3861 fix's core test: classifyCodeGraphSpawnError is what decides
// whether a cmd.StdoutPipe()/cmd.Start() failure gets reported as the
// executable being absent (persistent, not worth retrying) or the host
// being unable to fork right now (transient, worth a bounded retry). This
// pins that boundary directly against synthetic errno-shaped errors,
// rather than only through the (necessarily nondeterministic) happy path
// of actually exhausting host process resources.
func TestClassifyCodeGraphSpawnError_PinsTheClassificationBoundary(t *testing.T) {
	for _, test := range []struct {
		name                 string
		err                  error
		wantAbsent           bool
		wantSpawnUnavailable bool
		wantSpawnFailed      bool
		wantRawErrPreserved  bool
		// wantCode is asserted through localIndexErrorCodeFor/localIndexFailure,
		// not just the sentinel -- sol review F1 (CHAOS-3861): the sentinel
		// alone proved classifyCodeGraphSpawnError picked the right bucket,
		// but localIndexErrorCodeFor's own default case independently
		// mapped an unrecognized sentinel to LocalIndexErrorMalformed, which
		// is what actually reaches an operator (acr-mcp doctor, a receipt
		// reader). A sentinel-only test would not have caught that.
		wantCode LocalIndexErrorCode
	}{
		{
			name:       "no such file or directory (ENOENT) classifies as absent",
			err:        &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ENOENT},
			wantAbsent: true,
			wantCode:   LocalIndexErrorExecutableAbsent,
		},
		{
			name:       "permission denied (EACCES) classifies as absent",
			err:        &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.EACCES},
			wantAbsent: true,
			wantCode:   LocalIndexErrorExecutableAbsent,
		},
		{
			// sol review F1: a present, executable-bit-set, but broken
			// binary (truncated, bad #!, wrong architecture) is neither
			// ENOENT nor EACCES -- it passes CodeGraphRunner.executable()'s
			// preflight checks and fails only when the kernel actually
			// tries to exec it. Same bucket as ENOENT/EACCES: a persistent,
			// non-retryable configuration problem, not something worth a
			// spawn retry.
			name:       "exec format error (ENOEXEC, a broken binary) classifies as absent",
			err:        &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ENOEXEC},
			wantAbsent: true,
			wantCode:   LocalIndexErrorExecutableAbsent,
		},
		{
			// The exact CHAOS-3861 CI/repro signature: fork/exec ...: resource
			// temporarily unavailable, captured live under an artificially
			// lowered process ulimit while investigating the flake.
			name:                 "resource temporarily unavailable (EAGAIN) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.EAGAIN},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
			wantCode:             LocalIndexErrorSpawnUnavailable,
		},
		{
			// cmd.StdoutPipe()'s os.Pipe() call can hit this independently of
			// cmd.Start() -- same transient-resource bucket.
			name:                 "too many open files (EMFILE) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "pipe", Path: "|", Err: syscall.EMFILE},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
			wantCode:             LocalIndexErrorSpawnUnavailable,
		},
		{
			name:                 "out of memory (ENOMEM) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ENOMEM},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
			wantCode:             LocalIndexErrorSpawnUnavailable,
		},
		{
			// CHAOS-3878: the exact CI signature -- 'fork/exec
			// .../codegraph: text file busy' -- from a concurrent-fork
			// race (golang#22315 family): a sibling forked at the wrong
			// moment briefly inherits an open write fd on the just-built
			// codegraph binary, and the kernel refuses to exec a target
			// with a write-open fd. Self-clearing, retry-worthy -- same
			// bucket as EAGAIN/EMFILE/ENOMEM, not "missing or unusable".
			name:                 "text file busy (ETXTBSY) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ETXTBSY},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
			wantCode:             LocalIndexErrorSpawnUnavailable,
		},
		{
			// Neither bucket: propagated wrapped and truthful rather than
			// forced into a wrong classification. sol review F1: this used
			// to map to LocalIndexErrorMalformed (the localIndexErrorCodeFor
			// default case) -- an operator-facing lie, since "malformed"
			// implies the process ran and produced bad output, not that it
			// never started. errCodeGraphSpawnFailed exists so this case
			// gets its own honest code instead.
			name:                "an unrecognized failure classifies as spawn-failed, NOT malformed",
			err:                 errors.New("some other os/exec failure this classifier has never seen"),
			wantSpawnFailed:     true,
			wantRawErrPreserved: true,
			wantCode:            LocalIndexErrorSpawnFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyCodeGraphSpawnError(test.err)

			require.ErrorIs(t, got, ErrCodeGraphUnavailable, "every classification still joins the umbrella sentinel")
			require.Equal(t, test.wantAbsent, errors.Is(got, errCodeGraphExecutableAbsent), "errCodeGraphExecutableAbsent classification")
			require.Equal(t, test.wantSpawnUnavailable, errors.Is(got, errCodeGraphSpawnUnavailable), "errCodeGraphSpawnUnavailable classification")
			require.Equal(t, test.wantSpawnFailed, errors.Is(got, errCodeGraphSpawnFailed), "errCodeGraphSpawnFailed classification")
			if test.wantRawErrPreserved {
				require.ErrorIs(t, got, test.err, "the raw OS error must survive in the chain for a transient or unclassified failure, not be swallowed")
			}

			localErr, ok := localIndexFailure(got).(*LocalIndexError)
			require.True(t, ok)
			require.Equal(t, test.wantCode, localErr.Code(), "the code an operator/receipt actually sees must match the classification, not fall through to a default")
		})
	}
}

// TestClassifyCodeGraphSpawnError_AbsentBucketStaysPathFree pins the
// pre-existing, deliberate convention (see exec_resolver.go's doc comment
// and TestCodeGraphRunner_RedactsPath) that a genuinely-absent executable
// never echoes a filesystem path in its error text -- unchanged by this
// fix. The transient bucket is allowed to carry the raw OS error (which
// does include the path) because LocalIndexError.Error() never renders
// its wrapped cause; see TestCodeGraphDegradation-style assertions for
// that boundary instead.
func TestClassifyCodeGraphSpawnError_AbsentBucketStaysPathFree(t *testing.T) {
	const sensitivePath = "/Users/chris/.secret-deploy-layout/codegraph"
	err := classifyCodeGraphSpawnError(&fs.PathError{Op: "fork/exec", Path: sensitivePath, Err: syscall.ENOENT})
	require.NotContains(t, err.Error(), sensitivePath)
}
