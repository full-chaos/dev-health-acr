package api

import (
	"errors"
	"sync"

	"github.com/full-chaos/dev-health-acr/internal/auth"
)

type appClosers struct {
	mu    sync.Mutex
	once  sync.Once
	items []func() error
	err   error
}

func (a *App) trackAuthenticator(authenticator *auth.Authenticator) {
	if a == nil || authenticator == nil {
		return
	}
	a.closers.mu.Lock()
	defer a.closers.mu.Unlock()
	a.closers.items = append(a.closers.items, authenticator.Close)
}

// Close releases application-owned authenticators before their runtime stores.
func (a *App) Close() error {
	if a == nil {
		return nil
	}
	a.closers.once.Do(func() {
		a.closers.mu.Lock()
		closers := append([]func() error(nil), a.closers.items...)
		a.closers.mu.Unlock()
		var errs []error
		for _, closeResource := range closers {
			errs = append(errs, closeResource())
		}
		a.closers.err = errors.Join(errs...)
	})
	return a.closers.err
}
