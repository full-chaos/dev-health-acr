package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os/exec"
	"time"
)

const codeGraphWaitDelay = 100 * time.Millisecond

// classifyCodeGraphSpawnError turns a cmd.StdoutPipe()/cmd.Start() failure
// into the right sentinel instead of collapsing every failure into
// errCodeGraphExecutableAbsent (CHAOS-3861). Three buckets:
//
//   - fs.ErrNotExist / fs.ErrPermission: the executable genuinely isn't
//     there, or isn't usable as configured (wrong path, deleted, chmod'd
//     away). A persistent configuration problem -- errCodeGraphExecutableAbsent,
//     unchanged from before this fix.
//   - a transient host-resource errno (EAGAIN/EMFILE/ENOMEM on the
//     platforms that have them): the executable is fine, but the OS could
//     not fork a new process for it RIGHT NOW. Worth a bounded retry, so
//     it gets its own sentinel, errCodeGraphSpawnUnavailable, and -- unlike
//     the other two buckets -- the raw OS error is preserved in the chain
//     (wrapped, never swallowed) so a caller or log line can see exactly
//     what the host reported.
//   - anything else: propagated wrapped rather than forced into either
//     bucket. Unclassified, but truthful -- better than a confident wrong
//     answer.
func classifyCodeGraphSpawnError(err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, fs.ErrPermission):
		return errors.Join(errCodeGraphExecutableAbsent, ErrCodeGraphUnavailable)
	case isTransientCodeGraphSpawnErrno(err):
		return errors.Join(errCodeGraphSpawnUnavailable, ErrCodeGraphUnavailable, err)
	default:
		return errors.Join(ErrCodeGraphUnavailable, err)
	}
}

func runCodeGraphJSON(ctx context.Context, path, gitRoot string, command codeGraphRunCommand, arguments []string, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, arguments...)
	cmd.Dir = gitRoot
	cmd.Env = credentialSafeEnviron()
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	configureKeyringProcessGroup(cmd)
	cmd.Cancel = func() error { return killKeyringProcessGroup(cmd) }
	cmd.WaitDelay = codeGraphWaitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, classifyCodeGraphSpawnError(err)
	}
	cmd.Stderr = &boundedBuffer{limit: maxCodeGraphStderrBytes}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, classifyCodeGraphSpawnError(err)
	}
	processGroup := captureKeyringProcessGroup(cmd)
	output, readErr := decodeCodeGraphJSON(stdout)
	if readErr != nil {
		if errors.Is(readErr, io.EOF) {
			waitErr := waitCodeGraphProcessGroup(cmd, processGroup)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if waitErr != nil {
				if command == codeGraphRunStatus {
					return nil, errors.Join(errCodeGraphMissing, ErrCodeGraphUnavailable)
				}
				return nil, errors.Join(errCodeGraphUnsupported, ErrCodeGraphUnavailable)
			}
			return nil, errors.Join(errCodeGraphDecode, ErrCodeGraphUnavailable)
		}
		_ = stdout.Close()
		_ = killKeyringProcessGroupID(processGroup)
		_ = waitCodeGraphProcessGroup(cmd, processGroup)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(readErr, ErrCodeGraphOutputTooLarge) {
			return nil, ErrCodeGraphOutputTooLarge
		}
		return nil, errors.Join(errCodeGraphDecode, ErrCodeGraphUnavailable)
	}
	if err := waitCodeGraphProcessGroup(cmd, processGroup); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if command == codeGraphRunStatus {
			return nil, errors.Join(errCodeGraphMissing, ErrCodeGraphUnavailable)
		}
		return nil, errors.Join(errCodeGraphUnsupported, ErrCodeGraphUnavailable)
	}
	return output, nil
}

func waitCodeGraphProcessGroup(cmd *exec.Cmd, processGroup int) error {
	waitErr := cmd.Wait()
	_ = killKeyringProcessGroupID(processGroup)
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		// ErrWaitDelay means the command itself exited successfully and only
		// pipe-copy cleanup exceeded WaitDelay. Preserve the command status;
		// callers classify any independent decode error separately.
		return nil
	}
	return waitErr
}

func decodeCodeGraphJSON(reader io.Reader) ([]byte, error) {
	limited := &codeGraphOutputReader{reader: reader}
	decoder := json.NewDecoder(limited)
	var output json.RawMessage
	if err := decoder.Decode(&output); err != nil {
		if limited.exceeded {
			return nil, ErrCodeGraphOutputTooLarge
		}
		return nil, err
	}
	if limited.exceeded {
		return nil, ErrCodeGraphOutputTooLarge
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if limited.exceeded {
			return nil, ErrCodeGraphOutputTooLarge
		}
		if err == nil {
			return nil, errors.New("codegraph returned multiple JSON values")
		}
		return nil, err
	}
	return output, nil
}

type codeGraphOutputReader struct {
	reader   io.Reader
	read     int
	exceeded bool
}

func (r *codeGraphOutputReader) Read(p []byte) (int, error) {
	if r.read > maxCodeGraphStdoutBytes {
		return 0, ErrCodeGraphOutputTooLarge
	}
	if remaining := maxCodeGraphStdoutBytes + 1 - r.read; len(p) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	r.read += n
	if r.read > maxCodeGraphStdoutBytes {
		r.exceeded = true
		return n, ErrCodeGraphOutputTooLarge
	}
	return n, err
}
