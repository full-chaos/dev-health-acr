// Package startterm starts test containers and terminates one whose start fails. testcontainers returns the
// container together with the start error and removes nothing, so a readiness timeout or probe error leaks one
// container per failed start unless the caller terminates it. Every fixture start in the module goes through here.
//
// It also starts again, a bounded number of times and with a log line each time, a start whose create the daemon
// refused because the image is not there after testcontainers' pull (CHAOS-12855): see ImageMissingAtCreate.
//
// A start that the caller's context ends is logged and its error names the last lifecycle stage it reached, so a
// stalled image pull, create, start or readiness wait is told apart from the deadline alone: see endedByCaller.
package startterm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/containerd/errdefs"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const terminateBudget = 30 * time.Second

// imageMissingStarts bounds the starts of one fixture whose create finds no image; imageMissingPause is the wait
// between them. On acr #2000 (run 41672061049) a start of the same image 5 s after the refused one served.
const (
	imageMissingStarts = 3
	imageMissingPause  = 2 * time.Second
)

// pause waits between starts and returns ctx's error when ctx ends first. logf reports each retry. Tests replace both.
var (
	pause = func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	logf = log.Printf
)

type genericFunc func(context.Context, testcontainers.GenericContainerRequest) (testcontainers.Container, error)

type postgresFunc func(context.Context, string, ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error)

// Generic is testcontainers.GenericContainer that terminates the container when the start fails.
func Generic(ctx context.Context, req testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
	return generic(ctx, req, testcontainers.GenericContainer)
}

// PostgresRun is the postgres module's Run that terminates the container when the start fails.
func PostgresRun(ctx context.Context, image string, opts ...testcontainers.ContainerCustomizer) (*tcpostgres.PostgresContainer, error) {
	return postgresRun(ctx, image, opts, tcpostgres.Run)
}

// TerminateFailed terminates the container of a failed start on a fresh context, so a cancelled or expired
// caller context cannot stop the removal, and joins a terminate failure to err. It returns err unchanged when
// there is no container.
func TerminateFailed(c testcontainers.Container, err error) error {
	if err == nil || isNil(c) {
		return err
	}
	if termErr := Terminate(c); termErr != nil {
		return JoinTerminateFailure(err, termErr)
	}
	return err
}

// JoinTerminateFailure joins a failed removal of a failed start's container to the start error, as TerminateFailed
// does, for a caller that terminates the container itself. A start that left a container behind is never started
// again (StartUntilImageServes).
func JoinTerminateFailure(err, termErr error) error {
	return errors.Join(err, &terminateError{err: termErr})
}

// terminateError is a failed removal of a failed start's container. A start that left one behind is never started
// again (startUntilImageServes): a later start that serves would hide it.
type terminateError struct{ err error }

func (e *terminateError) Error() string { return "terminate after failed start: " + e.err.Error() }
func (e *terminateError) Unwrap() error { return e.err }

// Terminate terminates the container of a failed start on a fresh context and returns the terminate error only.
func Terminate(c testcontainers.Container) error {
	if isNil(c) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), terminateBudget)
	defer cancel()
	return c.Terminate(ctx)
}

func generic(ctx context.Context, req testcontainers.GenericContainerRequest, create genericFunc) (testcontainers.Container, error) {
	return startUntilImageServes(ctx, func() (testcontainers.Container, error) {
		stage := &startStage{}
		began := time.Now()
		tracked := req
		tracked.LifecycleHooks = stage.track(req.LifecycleHooks)
		c, err := create(ctx, tracked)
		if err != nil {
			err = endedByCaller(ctx, req.Image, began, stage, err)
			err = TerminateFailed(c, err)
		}
		return c, err
	})
}

func postgresRun(ctx context.Context, image string, opts []testcontainers.ContainerCustomizer, run postgresFunc) (*tcpostgres.PostgresContainer, error) {
	return startUntilImageServes(ctx, func() (*tcpostgres.PostgresContainer, error) {
		stage := &startStage{}
		began := time.Now()
		// Last, so it sees the hooks of every customizer, including one that assigns the list.
		track := testcontainers.CustomizeRequestOption(func(req *testcontainers.GenericContainerRequest) error {
			req.LifecycleHooks = stage.track(req.LifecycleHooks)
			return nil
		})
		tracked := append(slices.Clone(opts), track)
		pc, err := run(ctx, image, tracked...)
		if err != nil {
			err = endedByCaller(ctx, image, began, stage, err)
		}
		if err != nil && pc != nil {
			err = TerminateFailed(pc, err)
		}
		return pc, err
	})
}

// Stages of one start, in lifecycle order. A stage is entered at the hook that precedes it: testcontainers runs a
// caller's PreCreates hook right before the create call and a caller's PostStarts hook before its own readiness
// wait, so a start that ends in a stage ended inside that step. The image inspect and pull run before any hook.
const (
	stagePull int32 = iota
	stageCreate
	stageStart
	stageReadiness
	stageReady
)

var stageNames = [...]string{
	stagePull:      "image inspect or pull (no create hook ran)",
	stageCreate:    "container create",
	stageStart:     "container start",
	stageReadiness: "readiness wait",
	stageReady:     "post-ready hooks",
}

// startStage records the last lifecycle stage one start reached and the stage of the first caller hook that failed.
// testcontainers runs the rest of a phase's callbacks after one fails, so a stage marker that follows the failed
// hook advances reached: the failed hook's own stage wins (failed holds stage+1; 0 = none).
type startStage struct{ reached, failed atomic.Int32 }

func (s *startStage) name() string {
	if f := s.failed.Load(); f != 0 {
		return stageNames[f-1]
	}
	return stageNames[s.reached.Load()]
}

// track returns a fresh hook list: each of the caller's callbacks wrapped to record the stage it ran in when it
// fails, then the stage markers. The caller's list and its backing array are not written.
func (s *startStage) track(caller []testcontainers.ContainerLifecycleHooks) []testcontainers.ContainerLifecycleHooks {
	out := make([]testcontainers.ContainerLifecycleHooks, 0, len(caller)+1)
	for _, h := range caller {
		h.PreCreates = wrapRequestHooks(s, stageCreate, h.PreCreates)
		h.PostCreates = wrapContainerHooks(s, stageCreate, h.PostCreates)
		h.PostStarts = wrapContainerHooks(s, stageStart, h.PostStarts)
		h.PostReadies = wrapContainerHooks(s, stageReady, h.PostReadies)
		out = append(out, h)
	}
	return append(out, s.hooks())
}

func (s *startStage) fail(stage int32) { s.failed.CompareAndSwap(0, stage+1) }

func wrapRequestHooks(s *startStage, stage int32, hooks []testcontainers.ContainerRequestHook) []testcontainers.ContainerRequestHook {
	var out []testcontainers.ContainerRequestHook
	for _, hook := range hooks {
		out = append(out, func(ctx context.Context, req testcontainers.ContainerRequest) error {
			err := hook(ctx, req)
			if err != nil {
				s.fail(stage)
			}
			return err
		})
	}
	return out
}

func wrapContainerHooks(s *startStage, stage int32, hooks []testcontainers.ContainerHook) []testcontainers.ContainerHook {
	var out []testcontainers.ContainerHook
	for _, hook := range hooks {
		out = append(out, func(ctx context.Context, c testcontainers.Container) error {
			err := hook(ctx, c)
			if err != nil {
				s.fail(stage)
			}
			return err
		})
	}
	return out
}

func (s *startStage) hooks() testcontainers.ContainerLifecycleHooks {
	enter := func(stage int32) func() { return func() { s.reached.Store(stage) } }
	onRequest := func(enter func()) []testcontainers.ContainerRequestHook {
		return []testcontainers.ContainerRequestHook{func(context.Context, testcontainers.ContainerRequest) error {
			enter()
			return nil
		}}
	}
	onContainer := func(enter func()) []testcontainers.ContainerHook {
		return []testcontainers.ContainerHook{func(context.Context, testcontainers.Container) error {
			enter()
			return nil
		}}
	}
	return testcontainers.ContainerLifecycleHooks{
		PreCreates:  onRequest(enter(stageCreate)),
		PostCreates: onContainer(enter(stageStart)),
		PostStarts:  onContainer(enter(stageReadiness)),
		PostReadies: onContainer(enter(stageReady)),
	}
}

// endedByCaller logs a failed start whose context had ended and names in the error the stage the start was in. A
// start that ran into its caller's deadline is a fallback path, not a slow start: without the stage, an image pull
// that never finished and a readiness probe that never passed read the same ("context deadline exceeded").
func endedByCaller(ctx context.Context, image string, began time.Time, stage *startStage, err error) error {
	if ctx.Err() == nil {
		return err
	}
	elapsed := time.Since(began).Round(time.Millisecond)
	logf("startterm: start of %q ended by the caller's context (%v) after %s, during %s: %v",
		image, context.Cause(ctx), elapsed, stage.name(), err)
	return fmt.Errorf("start ended by the caller's context (%v) after %s, during %s: %w", context.Cause(ctx), elapsed, stage.name(), err)
}

// StartUntilImageServes is the retry Generic applies, for the one fixture that hands
// testcontainers.GenericContainer to its own terminate wrapper (falkorfixture) and so cannot call Generic. start
// runs one start; a start whose create finds no image must not leave its container behind (terminate it first,
// with TerminateFailed, whose failure ends the retry). The retry has no budget of its own: run it outside any start
// that has one.
func StartUntilImageServes(ctx context.Context, start func() (testcontainers.Container, error)) (testcontainers.Container, error) {
	return startUntilImageServes(ctx, start)
}

// ImageMissingAtCreate reports a start that the daemon refused at create because the image is not there.
// testcontainers v0.44 (attemptToPullImage) reads the pull stream into io.Discard, so a registry error reported
// inside the stream is dropped: the pull returns nil with no image, and only the create sees it ("No such image").
// Any other failure, another not-found among them, is not this one and is never retried.
func ImageMissingAtCreate(err error) bool {
	return errdefs.IsNotFound(err) && strings.Contains(err.Error(), "No such image")
}

// startUntilImageServes runs start until it serves, fails for another reason, leaves a container it could not
// remove, or imageMissingStarts starts all found no image. Every start it repeats is logged: a retry that is not
// seen would hide a registry or daemon fault.
func startUntilImageServes[T any](ctx context.Context, start func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		c, err := start()
		var leftBehind *terminateError
		if err == nil || !ImageMissingAtCreate(err) || errors.As(err, &leftBehind) {
			return c, err
		}
		if attempt == imageMissingStarts {
			return c, fmt.Errorf("no image after %d starts: %w", attempt, err)
		}
		logf("startterm: start %d of %d found no image (%v); testcontainers drops errors inside the pull stream (CHAOS-12855), so the image is pulled again",
			attempt, imageMissingStarts, err)
		if pause(ctx, imageMissingPause) != nil {
			return c, err
		}
	}
}

func isNil(c testcontainers.Container) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
