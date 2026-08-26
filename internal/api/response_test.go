package api

import (
	"errors"
	"net"
	"testing"
)

// timeoutError is the minimal net.Error implementation whose Timeout()
// reports true -- exactly the shape http.Server.WriteTimeout's underlying
// deadline-exceeded write error has (net.OpError wrapping a timeout).
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// CHAOS-4330: classifyWriteError must reduce a failed Write's error to a
// closed bucket, never the raw error text (this repo's own observability
// rule -- docs/observability.md -- forbids raw error text as a log
// attribute; a *net.OpError can carry a remote address).
func TestClassifyWriteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"a deadline-exceeded write (http.Server.WriteTimeout firing)", timeoutError{}, "timeout"},
		{"the connection already closed", net.ErrClosed, "client_disconnected"},
		{"a wrapped closed-connection error", errors.Join(errors.New("write: "), net.ErrClosed), "client_disconnected"},
		{"any other write failure", errors.New("write tcp 10.0.0.1:8080->10.0.0.2:1: broken pipe"), "write_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyWriteError(tt.err); got != tt.want {
				t.Fatalf("classifyWriteError(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}
