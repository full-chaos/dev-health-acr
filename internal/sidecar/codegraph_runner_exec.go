package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"
)

const codeGraphWaitDelay = time.Second

func runCodeGraphJSON(ctx context.Context, path, gitRoot string, arguments []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, arguments...)
	cmd.Dir = gitRoot
	cmd.Env = credentialSafeEnviron()
	cmd.Stdin = nil
	configureKeyringProcessGroup(cmd)
	cmd.Cancel = func() error { return killKeyringProcessGroup(cmd) }
	cmd.WaitDelay = codeGraphWaitDelay

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, ErrCodeGraphUnavailable
	}
	cmd.Stderr = &boundedBuffer{limit: maxCodeGraphStderrBytes}
	if err := cmd.Start(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrCodeGraphUnavailable
	}
	output, readErr := decodeCodeGraphJSON(stdout)
	if readErr != nil {
		_ = killKeyringProcessGroup(cmd)
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(readErr, ErrCodeGraphOutputTooLarge) {
			return nil, ErrCodeGraphOutputTooLarge
		}
		return nil, ErrCodeGraphUnavailable
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("codegraph command failed: %w", ErrCodeGraphUnavailable)
	}
	return output, nil
}

func decodeCodeGraphJSON(reader io.Reader) ([]byte, error) {
	limited := &codeGraphOutputReader{reader: reader}
	decoder := json.NewDecoder(limited)
	var output json.RawMessage
	if err := decoder.Decode(&output); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("codegraph returned multiple JSON values")
		}
		return nil, err
	}
	return output, nil
}

type codeGraphOutputReader struct {
	reader io.Reader
	read   int
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
		return n, ErrCodeGraphOutputTooLarge
	}
	return n, err
}
