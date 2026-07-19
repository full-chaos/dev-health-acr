package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	maxCodeGraphStdoutBytes = 1 << 20
	maxCodeGraphStderrBytes = 8 << 10
	codeGraphWaitDelay      = time.Second
)

var (
	ErrCodeGraphUnavailable       = errors.New("codegraph local index is unavailable")
	ErrCodeGraphOutputTooLarge    = errors.New("codegraph local index output exceeded the maximum size")
	ErrCodeGraphArgumentsRejected = errors.New("codegraph arguments are not permitted")
)

// CodeGraphRunner executes only fixed, read-only CodeGraph JSON commands.
type CodeGraphRunner struct {
	Config            LocalIndexConfig
	resolveExecutable func(string) (string, error)
}

// Status executes the fixed `codegraph status --json` contract command.
func (r CodeGraphRunner) Status(ctx context.Context) ([]byte, error) {
	return r.Run(ctx, "status", nil)
}

// Run rejects all caller-supplied arguments. The only supported command in
// this foundation is the fixed status probe; later adapters add typed commands.
func (r CodeGraphRunner) Run(ctx context.Context, command string, arguments []string) ([]byte, error) {
	if len(arguments) != 0 {
		return nil, ErrCodeGraphArgumentsRejected
	}
	if command != "status" {
		return nil, ErrCodeGraphArgumentsRejected
	}
	if r.Config.Provider == LocalIndexProviderDisabled || r.Config.Err != nil {
		return nil, ErrCodeGraphUnavailable
	}
	path, err := r.executable()
	if err != nil {
		return nil, ErrCodeGraphUnavailable
	}
	deadline, cancel := context.WithTimeout(ctx, r.timeout())
	defer cancel()
	return runCodeGraphStatus(deadline, path)
}

func (r CodeGraphRunner) executable() (string, error) {
	if r.resolveExecutable != nil {
		return r.resolveExecutable("codegraph")
	}
	if r.Config.Executable == "" {
		return currentExecutableResolver("codegraph")
	}
	if !filepath.IsAbs(r.Config.Executable) {
		return "", ErrUntrustedExecutable
	}
	path, err := filepath.EvalSymlinks(r.Config.Executable)
	if err != nil {
		return "", ErrUntrustedExecutable
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", ErrUntrustedExecutable
	}
	if err := verifyTrustedExecutableOwnership(info); err != nil {
		return "", err
	}
	return path, nil
}

func (r CodeGraphRunner) timeout() time.Duration {
	if r.Config.Timeout == 0 {
		return 3 * time.Second
	}
	return r.Config.Timeout
}

func runCodeGraphStatus(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, "status", "--json")
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
		return nil, fmt.Errorf("codegraph status failed: %w", ErrCodeGraphUnavailable)
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
