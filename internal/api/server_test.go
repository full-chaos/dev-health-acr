package api

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServerGracefulShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(ServerConfig{
		ListenAddress:     listener.Addr().String(),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       time.Second,
		WriteTimeout:      time.Second,
		IdleTimeout:       time.Second,
		ShutdownTimeout:   time.Second,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()

	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(time.Second)
	for {
		response, requestErr := client.Get("http://" + listener.Addr().String())
		if requestErr == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not become reachable: %v", requestErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}

// Shutdown drains the handler's detached work inside the shutdown bound: the
// drain gets a context that ends at the bound, and a drain that runs to the
// bound does not fail the shutdown.
func TestServerShutdownDrainsWithinTheBound(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	drained := make(chan time.Duration, 1)
	server, err := NewServer(ServerConfig{
		ListenAddress: listener.Addr().String(), ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		WriteTimeout: time.Second, IdleTimeout: time.Second, ShutdownTimeout: 300 * time.Millisecond,
		Drain: func(ctx context.Context) error {
			deadline, ok := ctx.Deadline()
			if !ok {
				drained <- -1
				return nil
			}
			drained <- time.Until(deadline)
			<-ctx.Done()
			return ctx.Err()
		},
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), slog.New(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx, listener) }()
	cancel()
	select {
	case left := <-drained:
		if left <= 0 || left > 300*time.Millisecond {
			t.Fatalf("drain bound left %s, want within the 300ms shutdown timeout", left)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not drain")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a drain that ran to the bound failed the shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down")
	}
}
