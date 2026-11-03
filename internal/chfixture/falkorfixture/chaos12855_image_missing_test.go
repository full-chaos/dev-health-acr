package falkorfixture

import (
	"context"
	"fmt"
	"testing"

	"github.com/containerd/errdefs"
	"github.com/testcontainers/testcontainers-go"
)

// CHAOS-12855 (Codex r1 P2 on #2006): this fixture hands testcontainers.GenericContainer to its own wrapper, so the
// startterm retry did not reach it. A create the daemon refuses because the image is missing after testcontainers'
// pull must be started again here too, and a port-unreachable restart must still follow its own rule.
func TestStartStartsAgainWhenCreateFindsNoImageAfterThePull(t *testing.T) {
	served := &fakeContainer{}
	var calls int
	got, err := startRetrying(context.Background(), &recordingT{}, "falkordb/falkordb", nil, func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		calls++
		if calls == 1 {
			daemon := fmt.Errorf("Error response from daemon: No such image: falkordb/falkordb:latest: %w", errdefs.ErrNotFound)
			return nil, fmt.Errorf("create container: container create: %w", daemon)
		}
		return served, nil
	})
	if err != nil || got != testcontainers.Container(served) || calls != 2 {
		t.Fatalf("a start whose create found no image must be tried again and serve: err=%v calls=%d", err, calls)
	}
	if served.terminated.Load() != 0 {
		t.Fatal("the serving container must stay up")
	}
}
