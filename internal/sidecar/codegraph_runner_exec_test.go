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
		wantRawErrPreserved  bool
	}{
		{
			name:       "no such file or directory (ENOENT) classifies as absent",
			err:        &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ENOENT},
			wantAbsent: true,
		},
		{
			name:       "permission denied (EACCES) classifies as absent",
			err:        &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.EACCES},
			wantAbsent: true,
		},
		{
			// The exact CHAOS-3861 CI/repro signature: fork/exec ...: resource
			// temporarily unavailable, captured live under an artificially
			// lowered process ulimit while investigating the flake.
			name:                 "resource temporarily unavailable (EAGAIN) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.EAGAIN},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
		},
		{
			// cmd.StdoutPipe()'s os.Pipe() call can hit this independently of
			// cmd.Start() -- same transient-resource bucket.
			name:                 "too many open files (EMFILE) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "pipe", Path: "|", Err: syscall.EMFILE},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
		},
		{
			name:                 "out of memory (ENOMEM) classifies as spawn-unavailable",
			err:                  &fs.PathError{Op: "fork/exec", Path: "/tmp/codegraph", Err: syscall.ENOMEM},
			wantSpawnUnavailable: true,
			wantRawErrPreserved:  true,
		},
		{
			// Neither bucket: propagated wrapped and truthful rather than
			// forced into a wrong classification.
			name:                "an unrecognized failure is propagated truthfully, not force-classified",
			err:                 errors.New("some other os/exec failure this classifier has never seen"),
			wantRawErrPreserved: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyCodeGraphSpawnError(test.err)

			require.ErrorIs(t, got, ErrCodeGraphUnavailable, "every classification still joins the umbrella sentinel")
			require.Equal(t, test.wantAbsent, errors.Is(got, errCodeGraphExecutableAbsent), "errCodeGraphExecutableAbsent classification")
			require.Equal(t, test.wantSpawnUnavailable, errors.Is(got, errCodeGraphSpawnUnavailable), "errCodeGraphSpawnUnavailable classification")
			if test.wantRawErrPreserved {
				require.ErrorIs(t, got, test.err, "the raw OS error must survive in the chain for a transient or unclassified failure, not be swallowed")
			}
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
