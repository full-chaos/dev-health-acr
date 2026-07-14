package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type apiProcess struct {
	command  *exec.Cmd
	done     chan struct{}
	mu       sync.Mutex
	waitErr  error
	stdout   []string
	stderr   bytes.Buffer
	stopOnce sync.Once
	stopErr  error
}

type apiProcessRequest struct {
	binary      string
	environment map[string]string
}

func buildACRAPIBinary(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join(workingDirectory, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "acr-api")
	command := exec.Command("go", "build", "-o", binary, "./cmd/acr-api")
	command.Dir = repositoryRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build acr-api: %v\n%s", err, output)
	}
	return binary
}

func reserveAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func startAPIProcess(t *testing.T, ctx context.Context, request apiProcessRequest) *apiProcess {
	t.Helper()
	command := exec.Command(request.binary, "serve")
	command.Env = mergedEnvironment(request.environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	process := &apiProcess{command: command, done: make(chan struct{})}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			line := scanner.Text()
			process.mu.Lock()
			process.stdout = append(process.stdout, line)
			process.mu.Unlock()
			if strings.Contains(line, `"msg":"HTTP server started"`) {
				select {
				case started <- struct{}{}:
				default:
				}
			}
		}
	}()
	go func() {
		var captured bytes.Buffer
		_, readErr := captured.ReadFrom(stderr)
		process.mu.Lock()
		process.stderr.Write(captured.Bytes())
		if readErr != nil {
			process.stderr.WriteString(readErr.Error())
		}
		process.mu.Unlock()
	}()
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	select {
	case <-started:
	case <-process.done:
		t.Fatalf("acr-api exited before listening: %v; logs=%s", process.WaitError(), process.Output())
	case <-ctx.Done():
		t.Fatalf("wait for acr-api listener: %v; logs=%s", ctx.Err(), process.Output())
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := process.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	return process
}

func assertAPIStartupFails(t *testing.T, ctx context.Context, request apiProcessRequest) {
	t.Helper()
	startupCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.Command(request.binary, "serve")
	command.Env = mergedEnvironment(request.environment)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"msg":"HTTP server started"`) {
				started <- struct{}{}
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-started:
		if signalErr := command.Process.Signal(os.Interrupt); signalErr != nil {
			t.Logf("signal unexpectedly started acr-api: %v", signalErr)
		}
		<-done
		t.Fatal("acr-api listened with an unusable hosted runtime")
	case err := <-done:
		if err == nil {
			t.Fatal("acr-api exited successfully with an unusable hosted runtime")
		}
	case <-startupCtx.Done():
		if killErr := command.Process.Kill(); killErr != nil {
			t.Logf("kill stuck acr-api: %v", killErr)
		}
		<-done
		t.Fatalf("acr-api did not fail startup: %v; stderr=%s", startupCtx.Err(), stderr.String())
	}
}

func (p *apiProcess) Stop(ctx context.Context) error {
	p.stopOnce.Do(func() {
		select {
		case <-p.done:
			p.stopErr = p.WaitError()
			return
		default:
		}
		if err := p.command.Process.Signal(os.Interrupt); err != nil {
			p.stopErr = err
			return
		}
		select {
		case <-p.done:
			p.stopErr = p.WaitError()
		case <-ctx.Done():
			p.stopErr = errors.Join(ctx.Err(), p.command.Process.Kill())
		}
	})
	return p.stopErr
}

func (p *apiProcess) WaitError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitErr
}

func (p *apiProcess) Output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.stdout, "\n") + "\n" + p.stderr.String()
}

func mergedEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	maps.Copy(values, overrides)
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}
