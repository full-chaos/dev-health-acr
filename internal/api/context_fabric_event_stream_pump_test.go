package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// panicOnSecondWrite is a ResponseWriter that panics on its second Write:
// the first frame (`accepted`) is written by the handler, the second (the
// first heartbeat) by the pump goroutine. It unwraps to the real writer, so
// the write deadline and the flush still reach the connection.
type panicOnSecondWrite struct {
	http.ResponseWriter
	writes atomic.Int32
}

func (p *panicOnSecondWrite) Write(b []byte) (int, error) {
	if p.writes.Add(1) == 2 {
		panic("planted: a write on the pump goroutine")
	}
	return p.ResponseWriter.Write(b)
}

func (p *panicOnSecondWrite) Unwrap() http.ResponseWriter { return p.ResponseWriter }

// TestAPanicOnThePumpGoroutineIsContained (CHAOS-12053): the pump runs on
// its own goroutine, so the handler's recovery cannot see a panic there. It
// must be recovered where it happens -- logged as `request panic recovered`
// (`event_stream_panic`), the stream marked failed (nothing more is written
// to a connection whose writer panicked) and the run cancelled -- instead of
// crashing the process.
func TestAPanicOnThePumpGoroutineIsContained(t *testing.T) {
	cancelled := make(chan struct{})
	app, token, logs := newStreamTestApp(t, investigatorOf(func(ctx context.Context, _ contextfabric.InvestigationRequest) (contextfabric.InvestigationResult, error) {
		select {
		case <-ctx.Done():
			close(cancelled)
			return contextfabric.InvestigationResult{}, ctx.Err()
		case <-time.After(5 * time.Second):
			return contextfabric.InvestigationResult{}, fmt.Errorf("the run was never cancelled")
		}
	}))
	app.config.RequestTimeout = 10 * time.Second
	app.eventStream.heartbeat = 20 * time.Millisecond
	handler := app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&panicOnSecondWrite{ResponseWriter: w}, r)
	}))
	defer server.Close()
	response, err := server.Client().Do(investigationHTTPRequest(t, server.URL, token, contractsv1.ContextFabricInvestigationEventStreamMediaType, investigationRequestBody()))
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	_, _ = body.ReadFrom(response.Body)
	response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the run was not cancelled after the pump panicked")
	}
	server.Close()
	line := logLine(t, logs, "request panic recovered")
	if line["failure_class"] != "event_stream_panic" || line["request_id"] != streamTestRequestID {
		t.Fatalf("panic line %v", line)
	}
	// Nothing is written after the panic: the body is `accepted` alone, no
	// terminal (a consumer reads it as interrupted).
	events, err := contractsv1.DecodeContextFabricInvestigationEventFrames(bytes.NewReader(body.Bytes()), 8<<20)
	if err != nil {
		t.Fatalf("frames: %v\n%s", err, body.String())
	}
	if len(events) != 1 || events[0].Type != contractsv1.ContextFabricInvestigationEventAccepted {
		t.Fatalf("%d events after the pump panicked, want accepted alone:\n%s", len(events), body.String())
	}
	if cancel := logLine(t, logs, "context fabric investigation cancel effective"); cancel["cause"] != "write_failed" {
		t.Fatalf("cancel line %v, want cause write_failed", cancel)
	}
	if closed := logLine(t, logs, "context fabric investigation event stream closed"); closed["write_failed"] != true || closed["terminal"] != "" {
		t.Fatalf("closed line %v, want write_failed and no terminal", closed)
	}
}

// TestTheCancelMomentIsStampedBeforeTheStreamLock (CHAOS-12053): a frame
// write holds the stream lock for as long as the connection takes (up to the
// write deadline). t_eff is the moment the run context ended, so it is
// stamped before the cancel line waits for that lock, not after.
func TestTheCancelMomentIsStampedBeforeTheStreamLock(t *testing.T) {
	app, _, logs := newStreamTestApp(t, investigatorOf(failingWith(fmt.Errorf("unused"))))
	stream, _ := newUnitStreamWithLogs(t, &deadlineRecorder{})
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()
	stream.mu.Lock() // a frame write in progress
	called := time.Now()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		app.logInvestigationCancelEffective(context.Background(), requestCtx, stream, streamTestRequestID, "result_12345678")
	}()
	time.Sleep(300 * time.Millisecond)
	stream.mu.Unlock()
	wg.Wait()
	line := logLine(t, logs, "context fabric investigation cancel effective")
	tEff, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["t_eff"]))
	if err != nil {
		t.Fatal(err)
	}
	if late := tEff.Sub(called); late > 100*time.Millisecond {
		t.Fatalf("t_eff is %s after the cancel line was called: it was stamped after the stream lock was released", late)
	}
}
