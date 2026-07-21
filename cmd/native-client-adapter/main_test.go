package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/nativeadapters"
)

func TestRunner_builds_isolated_invocation_when_flags_are_valid(t *testing.T) {
	// Given
	var got nativeadapters.Invocation
	r := runner{execute: func(_ context.Context, invocation nativeadapters.Invocation) (nativeadapters.Result, error) {
		got = invocation
		return nativeadapters.Result{}, nil
	}}
	output := &bytes.Buffer{}

	// When
	err := r.run(context.Background(), []string{
		"--client", "codex", "--binary", "/tmp/bin/codex", "--home", "/tmp/home",
		"--config", "/tmp/config", "--work", "/tmp/work", "--sidecar", "/tmp/bin/acr-mcp",
	}, output)

	// Then
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got.Client != nativeadapters.Codex || got.Binary != "/tmp/bin/codex" || got.Dir != "/tmp/work" {
		t.Fatalf("invocation = %#v", got)
	}
	if output.String() != "NATIVE_CLIENT_ADAPTER_OK client=codex result=validated\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunner_redacts_client_binary_when_execution_fails(t *testing.T) {
	// Given
	r := runner{execute: func(context.Context, nativeadapters.Invocation) (nativeadapters.Result, error) {
		return nativeadapters.Result{}, errors.New("native adapter start: /private/client-secret: no such file")
	}}

	// When
	err := r.run(context.Background(), []string{
		"--client", "codex", "--binary", "/private/client-secret", "--home", "/tmp/home",
		"--config", "/tmp/config", "--work", "/tmp/work", "--sidecar", "/tmp/bin/acr-mcp",
	}, &bytes.Buffer{})

	// Then
	if err == nil || bytes.Contains([]byte(err.Error()), []byte("/private/client-secret")) {
		t.Fatalf("error leaked binary path: %v", err)
	}
}

func TestRunner_rejects_invalid_flags_before_execution(t *testing.T) {
	// Given
	executed := false
	r := runner{execute: func(context.Context, nativeadapters.Invocation) (nativeadapters.Result, error) {
		executed = true
		return nativeadapters.Result{}, nil
	}}

	// When
	err := r.run(context.Background(), []string{"--client", "codex", "--binary", "codex", "--home", "/tmp/home", "--config", "/tmp/config", "--work", "/tmp/work", "--sidecar", "/tmp/bin/acr-mcp"}, &bytes.Buffer{})

	// Then
	if err == nil || executed {
		t.Fatalf("err = %v, executed = %t", err, executed)
	}
}
