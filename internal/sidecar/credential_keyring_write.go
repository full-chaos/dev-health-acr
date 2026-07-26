package sidecar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
)

var errKeyringWriteUnavailable = errors.New("acr: keyring write unavailable")
var errKeyringWriteFailed = errors.New("acr: keyring write failed")

// KeyringWriter stores a token without exposing it in argv or the child
// environment. Tests replace this seam instead of touching OS secret stores.
type KeyringWriter func(ctx context.Context, service, account, token string) error

// KeyringDeleter removes a previously stored token. It accepts only stable
// identifiers, never the secret itself.
type KeyringDeleter func(ctx context.Context, service, account string) error

var currentKeyringWriter KeyringWriter = defaultKeyringWriter
var currentKeyringDeleter KeyringDeleter = defaultKeyringDeleter

func defaultKeyringWriter(ctx context.Context, service, account, token string) error {
	if runtime.GOOS != "linux" {
		return errKeyringWriteUnavailable
	}
	return runKeyringMutation(ctx, strings.NewReader(token), "secret-tool", "store", "--label=Dev Health ACR credential", "service", service, "account", account)
}

func defaultKeyringDeleter(ctx context.Context, service, account string) error {
	switch runtime.GOOS {
	case "darwin":
		return runKeyringMutation(ctx, nil, "security", "delete-generic-password", "-a", account, "-s", service)
	case "linux":
		return runKeyringMutation(ctx, nil, "secret-tool", "clear", "service", service, "account", account)
	default:
		return errKeyringWriteUnavailable
	}
}

// runKeyringMutation shares the lookup subprocess hardening: trusted binary
// resolution, sanitized environment, bounded completion, and process-group
// cancellation. The token reaches secret-tool only through stdin.
func runKeyringMutation(ctx context.Context, stdin io.Reader, name string, args ...string) error {
	path, err := currentExecutableResolver(name)
	if err != nil {
		if errors.Is(err, ErrExecutableUnavailable) {
			return errKeyringWriteUnavailable
		}
		return fmt.Errorf("resolve trusted keyring executable: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = credentialSafeEnviron()
	cmd.Stdin = stdin
	cmd.Stdout = &boundedBuffer{limit: maxKeyringOutputBytes}
	cmd.Stderr = &boundedBuffer{limit: maxKeyringStderrBytes}
	configureKeyringProcessGroup(cmd)
	cmd.Cancel = func() error { return killKeyringProcessGroup(cmd) }
	cmd.WaitDelay = keyringCancelWaitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", errKeyringWriteFailed, err)
	}
	return nil
}
