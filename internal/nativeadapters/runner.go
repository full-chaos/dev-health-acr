package nativeadapters

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const maxOutput = 64 * 1024

var ErrOutputLimit = errors.New("native adapter: combined output exceeded 64 KiB")

type Result struct{ Output []byte }

func Run(ctx context.Context, invocation Invocation) (Result, error) {
	command := exec.Command(invocation.Binary, invocation.Args...)
	command.Env = invocation.Env
	command.Dir = invocation.Dir
	output := &limitedBuffer{limit: maxOutput, overflow: make(chan struct{}, 1)}
	configureProcess(command)
	command.Stdout, command.Stderr = output, output
	if err := command.Start(); err != nil {
		return Result{}, fmt.Errorf("native adapter start: %w", err)
	}
	written := make(chan error, 1)
	go func() { written <- command.Wait() }()
	select {
	case err := <-written:
		if output.exceeded {
			return Result{}, ErrOutputLimit
		}
		if err != nil {
			return Result{}, fmt.Errorf("native adapter exit: %w", err)
		}
	case <-output.overflow:
		stopProcess(command)
		<-written
		return Result{}, ErrOutputLimit
	case <-ctx.Done():
		stopProcess(command)
		select {
		case <-written:
		case <-time.After(2 * time.Second):
			killProcess(command)
			<-written
		}
		return Result{}, fmt.Errorf("native adapter deadline: %w", ctx.Err())
	}
	if err := Parse(invocation.Client, output.Bytes()); err != nil {
		return Result{}, err
	}
	return Result{Output: output.Bytes()}, nil
}

type limitedBuffer struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	overflow chan struct{}
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	if buffer.buffer.Len()+len(value) > buffer.limit {
		remaining := buffer.limit - buffer.buffer.Len()
		if remaining > 0 {
			_, _ = buffer.buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		select {
		case buffer.overflow <- struct{}{}:
		default:
		}
		return len(value), nil
	}
	return buffer.buffer.Write(value)
}

func (buffer *limitedBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func Redact(value string, roots Roots) string {
	for _, secret := range []string{"not-a-secret", "ACR_NATIVE_DUMMY_TOKEN"} {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	for _, path := range []string{roots.Home, roots.Config, roots.Work, roots.Sidecar} {
		value = strings.ReplaceAll(value, path, "[ISOLATED_PATH]")
	}
	return value
}
