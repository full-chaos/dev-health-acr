package mcp_test

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

const lifecycleEdgeAddress = "203.0.113.70"

// edgeLifecycle is the hosted MCP endpoint (edge failure limit 3, settable
// clock) in front of a fixture acr-api that parks the calls of one bearer.
type edgeLifecycle struct {
	t       *testing.T
	e       *endpoint
	hosted  *hostedAPI
	hold    *bearerHold
	advance func(time.Duration)
	valid   string
	once    sync.Once
}

func newEdgeLifecycle(t *testing.T, heldIsValid bool) *edgeLifecycle {
	t.Helper()
	return newEdgeLifecycleWith(t, heldIsValid, 5*time.Second)
}

func newEdgeLifecycleWith(t *testing.T, heldIsValid bool, resolveTimeout time.Duration) *edgeLifecycle {
	t.Helper()
	hosted := newHostedAPI(t)
	e, advance := newClockedEndpointWith(t, hosted, 3, resolveTimeout)
	l := &edgeLifecycle{t: t, e: e, hosted: hosted, advance: advance, valid: hosted.issue(readScopes, []string{repoPlain}, nil).token}
	held := unknownWellFormedToken(t)
	if heldIsValid {
		held = l.valid
	}
	l.hold = newBearerHold(held)
	hosted.bearerHold.Store(l.hold)
	t.Cleanup(l.releaseHold)
	return l
}

func (l *edgeLifecycle) releaseHold() { l.once.Do(func() { close(l.hold.release) }) }

func (l *edgeLifecycle) call(bearer string) int {
	return gateStatus(l.t, l.e, bearer, lifecycleEdgeAddress)
}

func (l *edgeLifecycle) remainingBudget() int {
	for n := 0; n <= 3; n++ {
		if l.call("junk") == http.StatusTooManyRequests {
			return n
		}
	}
	l.t.Fatal("the edge failure budget never refused")
	return 0
}

// awaitLine waits for the request line of a well-formed bearer with this
// gate reason (lines are written after the response) and returns it.
func (l *edgeLifecycle) awaitLine(reason string) map[string]any {
	l.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, line := range requestLines(l.t, l.e, lifecycleEdgeAddress) {
			if line["gate_reason"] == reason && line["principal_class"] == "bearer" {
				return line
			}
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("no request line with gate_reason %s", reason)
		}
		time.Sleep(time.Millisecond)
	}
}

// start sends the held bearer in the background with a context the test can
// cancel and waits until its acr-api call is parked. The result channel
// carries the HTTP status, or 0 when the client abandoned the request.
func (l *edgeLifecycle) start() (result chan int, abandon func()) {
	l.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result = make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.e.url(), bytes.NewReader(rawToolsList()))
		if err != nil {
			result <- -1
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
		req.Header.Set("Mcp-Method", "tools/list")
		req.Header.Set("Authorization", "Bearer "+l.hold.token)
		req.Header.Set("X-Forwarded-For", lifecycleEdgeAddress)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			result <- 0
			return
		}
		_ = resp.Body.Close()
		result <- resp.StatusCode
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if parked, _, _ := l.hold.counts(); parked == 1 {
			break
		}
		if time.Now().After(deadline) {
			l.t.Fatal("acr-api call never parked")
		}
		time.Sleep(time.Millisecond)
	}
	return result, func() {
		cancel()
		if code := <-result; code != 0 {
			l.t.Fatalf("abandoned request answered %d", code)
		}
		l.awaitLine("not_counted")
	}
}

func TestEdgeAttemptAdmittedUnderBudgetAndVerifiedIsServedAndNotCounted(t *testing.T) {
	l := newEdgeLifecycle(t, true)
	result, _ := l.start()
	l.releaseHold()
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified = %d, want 200", code)
	}
	if n := l.remainingBudget(); n != 3 {
		t.Fatalf("failures accepted after a verified attempt = %d, want 3", n)
	}
}

func TestEdgeAttemptAdmittedUnderBudgetAndRejectedIsCountedAnd401(t *testing.T) {
	l := newEdgeLifecycle(t, false)
	result, _ := l.start()
	l.releaseHold()
	if code := <-result; code != http.StatusUnauthorized {
		t.Fatalf("rejected = %d, want 401", code)
	}
	if n := l.remainingBudget(); n != 2 {
		t.Fatalf("failures accepted after a rejected attempt = %d, want 2", n)
	}
}

func TestEdgeAttemptAdmittedUnderBudgetAndAbandonedIsNotCountedAndReleased(t *testing.T) {
	l := newEdgeLifecycle(t, false)
	_, abandon := l.start()
	abandon()
	if n := l.remainingBudget(); n != 3 {
		t.Fatalf("failures accepted after an abandoned attempt = %d, want 3", n)
	}
}

func TestEdgeAttemptAdmittedUnderBudgetAndRejectedAfterRolloverIsCountedInTheNewWindow(t *testing.T) {
	l := newEdgeLifecycle(t, false)
	l.call("junk")
	l.call("junk")
	result, _ := l.start()
	l.advance(2 * time.Minute)
	l.releaseHold()
	if code := <-result; code != http.StatusUnauthorized {
		t.Fatalf("rejected after rollover = %d, want 401", code)
	}
	if n := l.remainingBudget(); n != 2 {
		t.Fatalf("failures accepted in the new window = %d, want 2 (the rejection counted there)", n)
	}
}

func TestEdgeAttemptAdmittedUnderBudgetAndVerifiedAfterRolloverIsServed(t *testing.T) {
	l := newEdgeLifecycle(t, true)
	result, _ := l.start()
	l.advance(2 * time.Minute)
	l.releaseHold()
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified after rollover = %d, want 200", code)
	}
}

func TestEdgeAttemptInTheOverBudgetSlotAndVerifiedIsServedAndNotCounted(t *testing.T) {
	l := newEdgeLifecycle(t, true)
	l.remainingBudget()
	result, _ := l.start()
	l.releaseHold()
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified in the slot = %d, want 200", code)
	}
	l.awaitLine("verified_over_budget")
	if code := l.call("junk"); code != http.StatusTooManyRequests {
		t.Fatalf("malformed bearer after the slot is free = %d, want 429 (still over budget)", code)
	}
}

func TestEdgeAttemptInTheOverBudgetSlotAndRejectedIsCountedAnd429(t *testing.T) {
	l := newEdgeLifecycle(t, false)
	l.remainingBudget()
	result, _ := l.start()
	l.releaseHold()
	if code := <-result; code != http.StatusTooManyRequests {
		t.Fatalf("rejected in the slot = %d, want 429", code)
	}
	if line := l.awaitLine("rejected_counted"); line["gate_decision"] != "failure_budget" {
		t.Fatalf("slot rejection line = %v, want failure_budget/rejected_counted", line)
	}
	if code := l.call(l.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the slot is free = %d, want 200", code)
	}
}

func TestEdgeAttemptInTheOverBudgetSlotAndAbandonedIsNotCountedAndFreesTheSlot(t *testing.T) {
	l := newEdgeLifecycle(t, false)
	l.remainingBudget()
	_, abandon := l.start()
	if code := l.call(l.valid); code != http.StatusTooManyRequests {
		t.Fatalf("valid bearer while the slot is held = %d, want 429", code)
	}
	abandon()
	if code := l.call(l.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the slot was abandoned = %d, want 200", code)
	}
}

func TestEdgeAttemptInTheOverBudgetSlotAndVerifiedAfterRolloverIsServed(t *testing.T) {
	l := newEdgeLifecycle(t, true)
	l.remainingBudget()
	result, _ := l.start()
	l.advance(2 * time.Minute)
	l.releaseHold()
	if code := <-result; code != http.StatusOK {
		t.Fatalf("verified in the slot after rollover = %d, want 200", code)
	}
}

func TestEdgeAttemptAbandonedByResolveTimeoutIsNotCountedAndFreesTheSlot(t *testing.T) {
	l := newEdgeLifecycleWith(t, false, 200*time.Millisecond)
	l.remainingBudget()
	if code := l.call(l.hold.token); code != http.StatusServiceUnavailable {
		t.Fatalf("slot attempt whose acr-api call timed out = %d, want 503", code)
	}
	l.awaitLine("not_counted")
	if code := l.call(l.valid); code != http.StatusOK {
		t.Fatalf("valid bearer after the timed-out slot attempt = %d, want 200", code)
	}
}
