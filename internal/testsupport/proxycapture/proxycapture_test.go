package proxycapture

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

func send(ctx context.Context, client *http.Client, target string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		panic(err)
	}
	request.Header.Set("Authorization", "Bearer "+Canary)
	if response, err := client.Do(request); err == nil {
		_ = response.Body.Close()
	}
}

// The control the whole method rests on: a client on http.DefaultTransport hands its request, credential included, to
// the proxy the child's environment names. If this stops being seen, every "nothing reached" row proves nothing.
func TestADefaultTransportClientReachesTheCaptureWithItsCredential(t *testing.T) {
	capture := Check(t, func(ctx context.Context) { send(ctx, &http.Client{}, Target) })
	if os.Getenv(childEnv) != "" {
		return
	}
	t.Logf("capture: %+v", capture)
	if capture.Connections == 0 || !capture.SawCanary {
		t.Fatalf("a DefaultTransport client did not reach the capture proxy with its credential: %+v", capture)
	}
}

func TestADefaultTransportClientTunnelsHTTPSThroughTheCapture(t *testing.T) {
	capture := Check(t, func(ctx context.Context) { send(ctx, &http.Client{}, "https://upstream.invalid/") })
	if os.Getenv(childEnv) != "" {
		return
	}
	t.Logf("capture: %+v", capture)
	if capture.Connections == 0 || !strings.HasPrefix(capture.FirstLines[0], "CONNECT upstream.invalid:443") {
		t.Fatalf("a DefaultTransport client did not ask the capture proxy for a tunnel: %+v", capture)
	}
}

func TestATransportWithoutProxyReachesNothing(t *testing.T) {
	Check(t, func(ctx context.Context) {
		send(ctx, &http.Client{Transport: &http.Transport{}}, Target)
		send(ctx, &http.Client{Transport: &http.Transport{}}, "https://upstream.invalid/")
	}).AssertNothingReached(t)
}

func TestTheRunPatternAnchorsEverySubtestLevel(t *testing.T) {
	if got, want := runPattern("TestA/b.c"), `^TestA$/^b\.c$`; got != want {
		t.Fatalf("runPattern = %q, want %q", got, want)
	}
}

func TestMustAttemptDialAcceptsOnlyARealNetworkFailure(t *testing.T) {
	refused := func(err error) (refused bool) {
		defer func() { refused = recover() != nil }()
		MustAttemptDial(err)
		return false
	}
	if !refused(nil) || !refused(errors.New("returned early")) {
		t.Fatal("a nil or non-network error must be refused")
	}
	if refused(&net.OpError{Op: "dial", Err: errors.New("x")}) || refused(fmt.Errorf("wrapped: %w", &net.DNSError{Name: "upstream.invalid"})) {
		t.Fatal("a network failure must be accepted")
	}
}

func TestMustAttemptDialRefusesGenericTransportText(t *testing.T) {
	for _, text := range []string{"transport unavailable", "unavailable", "transport"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%q was accepted as a network attempt", text)
				}
			}()
			MustAttemptDial(errors.New(text))
		}()
	}
}

func TestMustFailWithNeedsTheNamedText(t *testing.T) {
	MustFailWith(errors.New("x: failed in transport"), "failed in transport")
	for _, err := range []error{nil, errors.New("other")} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%v was accepted", err)
				}
			}()
			MustFailWith(err, "failed in transport")
		}()
	}
}

// panicOf runs f and returns what it panicked with, "" when it did not panic.
func panicOf(f func()) (message string) {
	defer func() {
		if r := recover(); r != nil {
			message = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

// shortCallTimeout makes Call's own deadline short for one test, and restores it.
func shortCallTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	saved := callTimeout
	callTimeout = d
	t.Cleanup(func() { callTimeout = saved })
}

// CHAOS-12868: a call's own error is returned as it is, and a call the deadline ended fails as the timeout, loudly,
// never as the client's error.
func TestCallReturnsTheCallsOwnErrorAndFailsLoudlyOnItsDeadline(t *testing.T) {
	shortCallTimeout(t, time.Second)
	want := errors.New("x: failed in transport")
	if got := Call(context.Background(), func(context.Context) error { return want }); !errors.Is(got, want) {
		t.Fatalf("Call returned %v, want the call's own error", got)
	}
	var remaining time.Duration
	message := panicOf(func() {
		_ = Call(context.Background(), func(ctx context.Context) error {
			// Read at the call's start, so load cannot skew it: the deadline is callTimeout itself.
			if deadline, ok := ctx.Deadline(); ok {
				remaining = time.Until(deadline)
			}
			<-ctx.Done()
			return errors.New("Post request timeout")
		})
	})
	if remaining <= 500*time.Millisecond || remaining > time.Second {
		t.Fatalf("the call's deadline was %v away at its start, want callTimeout (1 s)", remaining)
	}
	if !strings.HasPrefix(message, "TIMEOUT FALLBACK: the call's own 1s deadline ended it after ") ||
		!strings.Contains(message, "(it returned Post request timeout)") {
		t.Fatalf("a call the deadline ended gave %q, want the TIMEOUT FALLBACK failure", message)
	}
}

// The budget is per call: calls that each fit their deadline pass however long they take together, and a parent
// context that has already ended does not end the next call.
func TestCallGivesEachCallItsOwnBudget(t *testing.T) {
	shortCallTimeout(t, time.Second)
	parent, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	for i := range 3 {
		message := panicOf(func() {
			_ = Call(parent, func(ctx context.Context) error {
				time.Sleep(300 * time.Millisecond)
				return ctx.Err()
			})
		})
		if message != "" {
			t.Fatalf("call %d of 3 (each 300 ms, budget 1 s each, parent 400 ms in all) failed: %s", i+1, message)
		}
	}
	if parent.Err() == nil {
		t.Fatal("the parent context had not ended: the test proves nothing")
	}
}

// The fallback names the branch that was in flight: the HTTP events the call saw, each with its offset.
func TestCallTellsTheTimelineOfACallItsDeadlineEnded(t *testing.T) {
	shortCallTimeout(t, 100*time.Millisecond)
	message := panicOf(func() {
		_ = Call(context.Background(), func(ctx context.Context) error {
			trace := httptrace.ContextClientTrace(ctx)
			trace.DNSStart(httptrace.DNSStartInfo{Host: "upstream.invalid"})
			trace.DNSDone(httptrace.DNSDoneInfo{Err: errors.New("no such host")})
			trace.DNSStart(httptrace.DNSStartInfo{Host: "upstream.invalid"})
			trace.DNSDone(httptrace.DNSDoneInfo{})
			trace.ConnectStart("tcp", "192.0.2.1:80")
			trace.ConnectDone("tcp", "192.0.2.1:80", errors.New("refused"))
			trace.ConnectStart("tcp", "192.0.2.2:80")
			trace.ConnectDone("tcp", "192.0.2.2:80", nil)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	// Offsets vary with load: compare the events in order, without them.
	events := regexp.MustCompile(`\+[0-9.]+(?:ns|µs|ms|s) `).ReplaceAllString(message, "")
	want := "timeline: dns start upstream.invalid, dns failed, dns start upstream.invalid, dns done, " +
		"connect start 192.0.2.1:80, connect failed 192.0.2.1:80, connect start 192.0.2.2:80, connect done 192.0.2.2:80"
	if !strings.Contains(events, want) || !regexp.MustCompile(`timeline: \+[0-9.]+(?:ns|µs|ms|s) dns start`).MatchString(message) {
		t.Fatalf("the fallback said %q, want its timeline to be %q, each event with its offset", message, want)
	}
	if got := panicOf(func() {
		_ = Call(context.Background(), func(ctx context.Context) error { <-ctx.Done(); return nil })
	}); !strings.Contains(got, "timeline: no DNS or connect event") {
		t.Fatalf("a call with no HTTP event gave %q", got)
	}
}
