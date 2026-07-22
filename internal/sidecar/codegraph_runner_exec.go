package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

const codeGraphWaitDelay = 100 * time.Millisecond

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
		return nil, errors.Join(errCodeGraphExecutableAbsent, ErrCodeGraphUnavailable)
	}
	cmd.Stderr = &boundedBuffer{limit: maxCodeGraphStderrBytes}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.Join(errCodeGraphExecutableAbsent, ErrCodeGraphUnavailable)
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
