// Package startterm starts test containers and terminates one whose start fails. testcontainers returns the
// container together with the start error and removes nothing, so a readiness timeout or probe error leaks one
// container per failed start unless the caller terminates it. Every fixture start in the module goes through here.
package startterm

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

const terminateBudget = 30 * time.Second

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
	ctx, cancel := context.WithTimeout(context.Background(), terminateBudget)
	defer cancel()
	if termErr := c.Terminate(ctx); termErr != nil {
		return errors.Join(err, fmt.Errorf("terminate after failed start: %w", termErr))
	}
	return err
}

func generic(ctx context.Context, req testcontainers.GenericContainerRequest, create genericFunc) (testcontainers.Container, error) {
	c, err := create(ctx, req)
	if err != nil {
		err = TerminateFailed(c, err)
	}
	return c, err
}

func postgresRun(ctx context.Context, image string, opts []testcontainers.ContainerCustomizer, run postgresFunc) (*tcpostgres.PostgresContainer, error) {
	pc, err := run(ctx, image, opts...)
	if err != nil && pc != nil {
		err = TerminateFailed(pc, err)
	}
	return pc, err
}

func isNil(c testcontainers.Container) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	return v.Kind() == reflect.Pointer && v.IsNil()
}
