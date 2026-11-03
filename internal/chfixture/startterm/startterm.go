// Package startterm starts test containers and terminates one whose start fails. testcontainers returns the
// container together with the start error and removes nothing, so a readiness timeout or probe error leaks one
// container per failed start unless the caller terminates it. Every fixture start in the module goes through here.
//
// It also starts again, a bounded number of times and with a log line each time, a start whose create the daemon
// refused because the image is not there after testcontainers' pull (CHAOS-12855): see ImageMissingAtCreate.
package startterm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
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
		return errors.Join(err, &terminateError{err: termErr})
	}
	return err
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
	return startUntilImageServes(ctx, time.Time{}, func() (testcontainers.Container, error) {
		c, err := create(ctx, req)
		if err != nil {
			err = TerminateFailed(c, err)
		}
		return c, err
	})
}

func postgresRun(ctx context.Context, image string, opts []testcontainers.ContainerCustomizer, run postgresFunc) (*tcpostgres.PostgresContainer, error) {
	return startUntilImageServes(ctx, time.Time{}, func() (*tcpostgres.PostgresContainer, error) {
		pc, err := run(ctx, image, opts...)
		if err != nil && pc != nil {
			err = TerminateFailed(pc, err)
		}
		return pc, err
	})
}

// StartUntilImageServes is the retry Generic applies, for the one fixture that hands
// testcontainers.GenericContainer to its own terminate wrapper (falkorfixture) and so cannot call Generic. start
// builds and runs one start; a start whose create finds no image must return no container (terminate it first,
// with TerminateFailed). A zero deadline sets no deadline.
func StartUntilImageServes(ctx context.Context, deadline time.Time, start func() (testcontainers.Container, error)) (testcontainers.Container, error) {
	return startUntilImageServes(ctx, deadline, start)
}

// ImageMissingAtCreate reports a start that the daemon refused at create because the image is not there.
// testcontainers v0.44 (attemptToPullImage) reads the pull stream into io.Discard, so a registry error reported
// inside the stream is dropped: the pull returns nil with no image, and only the create sees it ("No such image").
// Any other failure, another not-found among them, is not this one and is never retried.
func ImageMissingAtCreate(err error) bool {
	return errdefs.IsNotFound(err) && strings.Contains(err.Error(), "No such image")
}

// startUntilImageServes runs start until it serves, fails for another reason, leaves a container it could not
// remove, imageMissingStarts starts all found no image, or the pause before the next start would end after deadline
// (zero: none). Every start it repeats is logged: a retry that is not seen would hide a registry or daemon fault.
func startUntilImageServes[T any](ctx context.Context, deadline time.Time, start func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		c, err := start()
		var leftBehind *terminateError
		if err == nil || !ImageMissingAtCreate(err) || errors.As(err, &leftBehind) {
			return c, err
		}
		if attempt == imageMissingStarts {
			return c, fmt.Errorf("no image after %d starts: %w", attempt, err)
		}
		if !deadline.IsZero() && time.Until(deadline) <= imageMissingPause {
			return c, fmt.Errorf("no image after %d start(s), and no time for another before the deadline: %w", attempt, err)
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
