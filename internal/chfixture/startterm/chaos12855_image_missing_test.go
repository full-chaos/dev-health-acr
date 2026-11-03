package startterm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// CHAOS-12855: testcontainers v0.44 (attemptToPullImage) reads the pull stream into io.Discard, so a registry error
// inside the stream is dropped and the pull returns nil with no image. The daemon then refuses the create with
// "No such image". On acr #2000 (run 41672061049) that one refusal failed every shared-ClickHouse test of the job,
// while a later start of the same image in the same process served.

const missingRef = "ghcr.io/full-chaos/dev-health-acr/clickhouse/clickhouse-server:26.7"

// imageMissing is the daemon's refusal as testcontainers wraps it (generic.go:81, docker.go:1322). The live test in
// chaos12855_image_missing_live_test.go pins the daemon side of it against a real daemon.
func imageMissing() error {
	daemon := fmt.Errorf("Error response from daemon: No such image: %s: %w", missingRef, errdefs.ErrNotFound)
	return fmt.Errorf("create container: container create: %w", daemon)
}

type startScript struct {
	errs  []error
	calls int
}

func (s *startScript) next() error {
	s.calls++
	if s.calls <= len(s.errs) {
		return s.errs[s.calls-1]
	}
	return nil
}

func (s *startScript) generic(c testcontainers.Container) genericFunc {
	return func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
		if err := s.next(); err != nil {
			return nil, err
		}
		return c, nil
	}
}

func (s *startScript) postgres(pc *tcpostgres.PostgresContainer) postgresFunc {
	return func(context.Context, string, ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
		if err := s.next(); err != nil {
			return nil, err
		}
		return pc, nil
	}
}

// quietRetries replaces the pause and the log for one test and records both.
func quietRetries(t *testing.T, pauseErr error) (pauses *int, lines *[]string) {
	t.Helper()
	oldPause, oldLog := pause, logf
	pauses, lines = new(int), new([]string)
	pause = func(context.Context, time.Duration) error { *pauses++; return pauseErr }
	logf = func(format string, args ...any) { *lines = append(*lines, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { pause, logf = oldPause, oldLog })
	return pauses, lines
}

func TestGenericStartsAgainWhenCreateFindsNoImageAfterThePull(t *testing.T) {
	pauses, lines := quietRetries(t, nil)
	fake := &fakeContainer{}
	script := &startScript{errs: []error{imageMissing()}}
	got, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, script.generic(fake))
	if err != nil || got != testcontainers.Container(fake) {
		t.Fatalf("a start whose create found no image must be tried again and serve: container=%v err=%v", got, err)
	}
	if script.calls != 2 || *pauses != 1 {
		t.Fatalf("starts=%d pauses=%d, want 2 and 1", script.calls, *pauses)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "No such image") || !strings.Contains((*lines)[0], "CHAOS-12855") {
		t.Fatalf("each retry must be logged with the daemon's refusal and its cause, got %q", *lines)
	}
}

func TestGenericGivesUpAfterThreeStartsThatFindNoImage(t *testing.T) {
	pauses, lines := quietRetries(t, nil)
	script := &startScript{errs: []error{imageMissing(), imageMissing(), imageMissing(), imageMissing()}}
	_, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, script.generic(&fakeContainer{}))
	if err == nil || !errdefs.IsNotFound(err) || !strings.Contains(err.Error(), "no image after 3 starts") {
		t.Fatalf("an image that never arrives must fail loudly, with the daemon's refusal kept: %v", err)
	}
	if script.calls != 3 || *pauses != 2 || len(*lines) != 2 {
		t.Fatalf("starts=%d pauses=%d logged=%d, want 3, 2 and 2", script.calls, *pauses, len(*lines))
	}
}

func TestGenericDoesNotRetryAnyOtherFailure(t *testing.T) {
	pauses, lines := quietRetries(t, nil)
	for name, startErr := range map[string]error{
		"a readiness failure":       errors.New("clickhouse not serving"),
		"another not-found":         fmt.Errorf("create container: container create: Error response from daemon: network acr-net not found: %w", errdefs.ErrNotFound),
		"no image, not a not-found": errors.New("create container: No such image: but the daemon did not say not-found"),
	} {
		script := &startScript{errs: []error{startErr}}
		if _, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, script.generic(&fakeContainer{})); !errors.Is(err, startErr) {
			t.Fatalf("%s: the start error must be returned as is: %v", name, err)
		}
		if script.calls != 1 {
			t.Fatalf("%s: started %d times, want once", name, script.calls)
		}
	}
	if *pauses != 0 || len(*lines) != 0 {
		t.Fatalf("pauses=%d logged=%d, want none", *pauses, len(*lines))
	}
}

func TestGenericStopsRetryingWhenTheContextEnds(t *testing.T) {
	pauses, _ := quietRetries(t, context.Canceled)
	script := &startScript{errs: []error{imageMissing()}}
	_, err := generic(context.Background(), testcontainers.GenericContainerRequest{}, script.generic(&fakeContainer{}))
	if err == nil || !errdefs.IsNotFound(err) {
		t.Fatalf("the daemon's refusal must be returned when the wait ends early: %v", err)
	}
	if script.calls != 1 || *pauses != 1 {
		t.Fatalf("starts=%d pauses=%d, want 1 and 1", script.calls, *pauses)
	}
}

func TestPostgresStartsAgainWhenCreateFindsNoImageAfterThePull(t *testing.T) {
	pauses, _ := quietRetries(t, nil)
	pc := &tcpostgres.PostgresContainer{Container: &fakeContainer{}}
	script := &startScript{errs: []error{imageMissing()}}
	got, err := postgresRun(context.Background(), "img", nil, script.postgres(pc))
	if err != nil || got != pc {
		t.Fatalf("a postgres start whose create found no image must be tried again and serve: pc=%v err=%v", got, err)
	}
	if script.calls != 2 || *pauses != 1 {
		t.Fatalf("starts=%d pauses=%d, want 2 and 1", script.calls, *pauses)
	}
}
