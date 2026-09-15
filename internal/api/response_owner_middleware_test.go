package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// responseOwnerBlockingWriter controls the response boundary independently
// of the route. A route can return from its work before Write returns, so the
// test can observe the real membership permit while the synchronous response
// is still in progress.
type responseOwnerBlockingWriter struct {
	header http.Header
	status int
	body   bytes.Buffer

	writeStarted chan struct{}
	unblock      chan struct{}
	startOnce    sync.Once

	writeErr   error
	partialN   int
	panicCount int
	lastN      int
}

func newResponseOwnerBlockingWriter() *responseOwnerBlockingWriter {
	return &responseOwnerBlockingWriter{
		header:       make(http.Header),
		writeStarted: make(chan struct{}),
		unblock:      make(chan struct{}),
	}
}

func (w *responseOwnerBlockingWriter) Header() http.Header { return w.header }

func (w *responseOwnerBlockingWriter) WriteHeader(status int) { w.status = status }

func (w *responseOwnerBlockingWriter) Write(p []byte) (int, error) {
	w.startOnce.Do(func() { close(w.writeStarted) })
	<-w.unblock
	if w.panicCount != 0 {
		if w.panicCount > 0 {
			w.panicCount--
		}
		panic("synthetic response writer panic")
	}
	if w.writeErr != nil {
		n := w.partialN
		if n < 0 {
			n = 0
		}
		if n > len(p) {
			n = len(p)
		}
		w.lastN = n
		if n > 0 {
			_, _ = w.body.Write(p[:n])
		}
		return n, w.writeErr
	}
	n, err := w.body.Write(p)
	w.lastN = n
	return n, err
}

func waitForResponseOwnerWrite(t *testing.T, writer *responseOwnerBlockingWriter) {
	t.Helper()
	select {
	case <-writer.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("instrumented handler did not reach the controlled response write")
	}
}

func waitForResponseOwnerHandler(t *testing.T, done <-chan any) any {
	t.Helper()
	select {
	case recovered := <-done:
		return recovered
	case <-time.After(time.Second):
		t.Fatal("instrumented handler did not exit")
		return nil
	}
}

func receiveResponseOwnerTestValue[T any](t *testing.T, values <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", label)
		return zero
	}
}

func startResponseOwnerHandler(handler http.Handler, writer http.ResponseWriter, request *http.Request) <-chan any {
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		handler.ServeHTTP(writer, request)
	}()
	return done
}

func retainResponseOwnerTestLease(r *http.Request, gate *contextfabric.WorkItemMembershipGate) error {
	owner, ok := contextfabric.WorkItemResponseOwnerFromContext(r.Context())
	if !ok {
		return errors.New("response owner missing from request context")
	}
	lease, err := gate.Acquire(r.Context())
	if err != nil {
		return err
	}
	if err := owner.Retain(lease); err != nil {
		lease.Release()
		return err
	}
	return nil
}

func assertResponseOwnerGateHeld(t *testing.T, gate *contextfabric.WorkItemMembershipGate) {
	t.Helper()
	if got := gate.Stats().InFlight; got != 1 {
		t.Fatalf("gate in-flight occupancy = %d, want 1 while response Write is blocked", got)
	}
	if _, err := gate.Acquire(context.Background()); !errors.Is(err, contextfabric.ErrWorkItemMembershipQueueFull) {
		t.Fatalf("second gate.Acquire = %v, want ErrWorkItemMembershipQueueFull while response Write is blocked", err)
	}
}

func assertResponseOwnerGateFree(t *testing.T, gate *contextfabric.WorkItemMembershipGate) {
	t.Helper()
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("gate in-flight occupancy = %d, want 0 after instrumented handler exit", got)
	}
	lease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("gate.Acquire after instrumented handler exit: %v", err)
	}
	lease.Release()
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("gate in-flight occupancy after availability probe = %d, want 0", got)
	}
}

func newResponseOwnerAPITestGate(t *testing.T) *contextfabric.WorkItemMembershipGate {
	t.Helper()
	gate, err := contextfabric.NewWorkItemMembershipGate(1, 0)
	if err != nil {
		t.Fatalf("NewWorkItemMembershipGate: %v", err)
	}
	return gate
}

func acquireResponseOwnerAPITestLease(t *testing.T, gate *contextfabric.WorkItemMembershipGate) *contextfabric.WorkItemMembershipLease {
	t.Helper()
	lease, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatalf("gate.Acquire: %v", err)
	}
	return lease
}

func TestInstrumentedHandlerResponseOwnerHoldsPermitThroughSuccessfulWrite(t *testing.T) {
	app := testApp(t)
	gate := newResponseOwnerAPITestGate(t)
	writer := newResponseOwnerBlockingWriter()
	routeErr := make(chan error, 1)
	ownerSeen := make(chan *contextfabric.WorkItemResponseOwner, 1)
	handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner, ok := contextfabric.WorkItemResponseOwnerFromContext(r.Context())
		if !ok {
			routeErr <- errors.New("response owner missing from full instrumented chain")
			return
		}
		ownerSeen <- owner
		routeErr <- retainResponseOwnerTestLease(r, gate)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("response-complete"))
	}))

	done := startResponseOwnerHandler(handler, writer, httptest.NewRequest(http.MethodGet, "/owner", nil))
	if err := receiveResponseOwnerTestValue(t, routeErr, "successful route owner setup"); err != nil {
		t.Fatalf("route owner setup: %v", err)
	}
	waitForResponseOwnerWrite(t, writer)
	assertResponseOwnerGateHeld(t, gate)
	close(writer.unblock)
	if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
		t.Fatalf("successful response unexpectedly panicked: %v", recovered)
	}
	if got := receiveResponseOwnerTestValue(t, ownerSeen, "successful response owner"); got == nil {
		t.Fatal("instrumented handler supplied a nil response owner")
	}
	if writer.status != http.StatusOK || writer.body.String() != "response-complete" {
		t.Fatalf("response = status %d body %q, want 200 and response-complete", writer.status, writer.body.String())
	}
	assertResponseOwnerGateFree(t, gate)
}

func TestInstrumentedHandlerResponseOwnerBorrowsExistingOwner(t *testing.T) {
	app := testApp(t)
	gate := newResponseOwnerAPITestGate(t)
	lease := acquireResponseOwnerAPITestLease(t, gate)
	owner := contextfabric.NewWorkItemResponseOwner()
	if err := owner.Retain(lease); err != nil {
		t.Fatalf("owner.Retain: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/borrow", nil)
	request = request.WithContext(contextfabric.WithWorkItemResponseOwner(request.Context(), owner))
	seen := make(chan *contextfabric.WorkItemResponseOwner, 1)
	handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		borrowed, ok := contextfabric.WorkItemResponseOwnerFromContext(r.Context())
		if !ok {
			return
		}
		seen <- borrowed
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(httptest.NewRecorder(), request)
	if got := receiveResponseOwnerTestValue(t, seen, "borrowed response owner"); got != owner {
		t.Fatalf("borrowed owner = %p, want existing owner %p", got, owner)
	}
	assertResponseOwnerGateHeld(t, gate)
	// The middleware borrowed this owner, so the creator still controls the
	// completion boundary.
	owner.Complete()
	assertResponseOwnerGateFree(t, gate)
}

func TestInstrumentedHandlerResponseOwnerHoldsPermitThroughErrorWrite(t *testing.T) {
	app := testApp(t)
	gate := newResponseOwnerAPITestGate(t)
	writer := newResponseOwnerBlockingWriter()
	routeErr := make(chan error, 1)
	handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := retainResponseOwnerTestLease(r, gate)
		routeErr <- err
		if err != nil {
			return
		}
		writeError(w, r, http.StatusBadGateway, "synthetic_upstream", "Synthetic upstream failure", true, nil)
	}))

	done := startResponseOwnerHandler(handler, writer, httptest.NewRequest(http.MethodGet, "/error", nil))
	waitForResponseOwnerWrite(t, writer)
	assertResponseOwnerGateHeld(t, gate)
	close(writer.unblock)
	if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
		t.Fatalf("error response unexpectedly panicked: %v", recovered)
	}
	if err := receiveResponseOwnerTestValue(t, routeErr, "error route owner setup"); err != nil {
		t.Fatalf("route lease setup: %v", err)
	}
	if writer.status != http.StatusBadGateway {
		t.Fatalf("error response status = %d, want %d", writer.status, http.StatusBadGateway)
	}
	assertResponseOwnerGateFree(t, gate)
}

func TestInstrumentedHandlerResponseOwnerHoldsPermitThroughRecoveredPanicWrite(t *testing.T) {
	app := testApp(t)
	gate := newResponseOwnerAPITestGate(t)
	writer := newResponseOwnerBlockingWriter()
	routeErr := make(chan error, 1)
	handler := app.InstrumentedHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		routeErr <- retainResponseOwnerTestLease(r, gate)
		panic("synthetic route panic")
	}))

	done := startResponseOwnerHandler(handler, writer, httptest.NewRequest(http.MethodGet, "/panic", nil))
	waitForResponseOwnerWrite(t, writer)
	assertResponseOwnerGateHeld(t, gate)
	close(writer.unblock)
	if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
		t.Fatalf("recovery middleware leaked a panic: %v", recovered)
	}
	if err := receiveResponseOwnerTestValue(t, routeErr, "panic route owner setup"); err != nil {
		t.Fatalf("route lease setup: %v", err)
	}
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("recovered response status = %d, want %d", writer.status, http.StatusInternalServerError)
	}
	assertResponseOwnerGateFree(t, gate)
}

func TestInstrumentedHandlerResponseOwnerReleasesAfterWriteErrorAndPartialWrite(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		partialN int
	}{
		{name: "write_error", partialN: 0},
		{name: "partial_write", partialN: 3},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := testApp(t)
			gate := newResponseOwnerAPITestGate(t)
			writer := newResponseOwnerBlockingWriter()
			writer.writeErr = errors.New("synthetic response write failure")
			writer.partialN = testCase.partialN
			routeErr := make(chan error, 1)
			handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routeErr <- retainResponseOwnerTestLease(r, gate)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("write-failure-payload"))
			}))

			done := startResponseOwnerHandler(handler, writer, httptest.NewRequest(http.MethodGet, "/write-error", nil))
			waitForResponseOwnerWrite(t, writer)
			assertResponseOwnerGateHeld(t, gate)
			close(writer.unblock)
			if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
				t.Fatalf("write failure response unexpectedly panicked: %v", recovered)
			}
			if err := receiveResponseOwnerTestValue(t, routeErr, "write failure route owner setup"); err != nil {
				t.Fatalf("route lease setup: %v", err)
			}
			if writer.lastN != testCase.partialN {
				t.Fatalf("underlying write count = %d, want %d", writer.lastN, testCase.partialN)
			}
			assertResponseOwnerGateFree(t, gate)
		})
	}
}

func TestInstrumentedHandlerResponseOwnerReleasesAfterResponseWriterPanic(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		panicCount int
		wantPanic  bool
	}{
		{name: "panic_then_recovery_write", panicCount: 1},
		{name: "panic_during_recovery_unwind", panicCount: -1, wantPanic: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			app := testApp(t)
			gate := newResponseOwnerAPITestGate(t)
			writer := newResponseOwnerBlockingWriter()
			writer.panicCount = testCase.panicCount
			routeErr := make(chan error, 1)
			handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routeErr <- retainResponseOwnerTestLease(r, gate)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("panic-payload"))
			}))

			done := startResponseOwnerHandler(handler, writer, httptest.NewRequest(http.MethodGet, "/write-panic", nil))
			waitForResponseOwnerWrite(t, writer)
			assertResponseOwnerGateHeld(t, gate)
			close(writer.unblock)
			recovered := waitForResponseOwnerHandler(t, done)
			if (recovered != nil) != testCase.wantPanic {
				t.Fatalf("recovered panic = %v, want panic=%t", recovered, testCase.wantPanic)
			}
			if err := receiveResponseOwnerTestValue(t, routeErr, "write panic route owner setup"); err != nil {
				t.Fatalf("route lease setup: %v", err)
			}
			assertResponseOwnerGateFree(t, gate)
		})
	}
}

func TestInstrumentedHandlerResponseOwnerDoesNotReleaseOnCancellationWhileWriteBlocked(t *testing.T) {
	app := testApp(t)
	app.config.RequestTimeout = time.Minute
	gate := newResponseOwnerAPITestGate(t)
	writer := newResponseOwnerBlockingWriter()
	routeErr := make(chan error, 1)
	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := app.InstrumentedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		routeErr <- retainResponseOwnerTestLease(r, gate)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("cancellation-payload"))
	}))

	request := httptest.NewRequest(http.MethodGet, "/cancel", nil).WithContext(requestContext)
	done := startResponseOwnerHandler(handler, writer, request)
	waitForResponseOwnerWrite(t, writer)
	assertResponseOwnerGateHeld(t, gate)
	cancel()
	select {
	case recovered := <-done:
		t.Fatalf("handler exited while response Write remained blocked after cancellation: %v", recovered)
	case <-time.After(50 * time.Millisecond):
	}
	assertResponseOwnerGateHeld(t, gate)

	close(writer.unblock)
	if recovered := waitForResponseOwnerHandler(t, done); recovered != nil {
		t.Fatalf("cancelled response unexpectedly panicked after Write returned: %v", recovered)
	}
	if err := receiveResponseOwnerTestValue(t, routeErr, "cancel route owner setup"); err != nil {
		t.Fatalf("route lease setup: %v", err)
	}
	assertResponseOwnerGateFree(t, gate)
}
