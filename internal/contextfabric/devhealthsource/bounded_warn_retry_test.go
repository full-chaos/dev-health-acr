package devhealthsource

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextpacket"
)

// boundedRetryRig is an overlap pass whose first page is not bounded and whose second page is: table full holds
// 600 positions of two statement rows each (a page reads 100 of them), so every page but the last ends at the
// last row of full once a row of table later lies past it. The repository rows give each page a batch to apply.
type boundedRetryRig struct {
	t                               *testing.T
	plan                            sourcePlan
	logs                            *bytes.Buffer
	repos                           *liveKeysetRows
	later                           keysetRows
	failAfter                       string
	failAtLines                     int
	reads, failAtRead, failedAtRead int
	now                             time.Time
	frontier                        cursorState
}

var errBoundedRetryRead = errors.New("read of table full failed")

func newBoundedRetryRig(t *testing.T) *boundedRetryRig {
	t.Helper()
	var full keysetRows
	for n := 0; n < 600; n++ {
		key := fmt.Sprintf("a%03d", n)
		full = append(full, keysetRow{at: pageCutStamp, key: key}, keysetRow{at: pageCutStamp, key: key})
	}
	r := &boundedRetryRig{
		t:        t,
		logs:     &bytes.Buffer{},
		repos:    &liveKeysetRows{rows: keysetRows{{at: pageCutStamp.Add(-time.Second), key: "00000000-0000-4000-8000-000000000001"}}},
		frontier: cursorState{Since: pageCutStamp.Add(time.Minute), After: "zz"},
	}
	fullTable := keysetTable("full", full)
	query := fullTable.query
	fullTable.query = func(ctx context.Context, client contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		if r.failAfter != "" && cursor.After == r.failAfter {
			return nil, false, errBoundedRetryRead
		}
		r.reads++
		if r.failAtLines > 0 && strings.Count(r.logs.String(), boundedPageMessage) >= r.failAtLines {
			r.failedAtRead = r.reads
			return nil, false, errBoundedRetryRead
		}
		if r.failAtRead > 0 && r.reads == r.failAtRead {
			return nil, false, errBoundedRetryRead
		}
		return query(ctx, client, orgID, cursor, limit)
	}
	laterTable := entityTable{name: "later", query: func(ctx context.Context, _ contextpacket.ClickHouseQueryClient, orgID string, cursor cursorState, limit int) ([]candidate, bool, error) {
		return fetch(ctx, r.later, "", rowLimitBindings(orgID, cursor, limit), limit, scanKeysetRow)
	}}
	r.now = pageCutStamp.Add(10 * time.Minute)
	r.plan = sourcePlan{
		client: keysetRows{}, source: "bounded_warn_retry_test", version: ClickHouseSourceVersion,
		tables:         []entityTable{repositoryKeysetTable(r.repos), fullTable, laterTable},
		now:            func() time.Time { return r.now },
		overlap:        15 * time.Minute,
		window:         newWindowMemo(),
		windowScope:    windowScopeFor("org", 0),
		windowInterval: time.Hour,
		logger:         slog.New(slog.NewJSONHandler(r.logs, nil)),
	}
	r.plan.windowPagesPerCall = 3
	return r
}

// call runs one overlap call and returns the batch's ack, empty when no batch was emitted.
func (r *boundedRetryRig) call() (string, error) {
	r.t.Helper()
	r.reads = 0
	cursor, err := encodeCursor(r.frontier)
	if err != nil {
		r.t.Fatal(err)
	}
	batch, available, err := r.plan.overlapBatch(context.Background(), "org", cursor, r.frontier)
	if err != nil || !available {
		return "", err
	}
	next, err := decodeCursor(batch.NextCursor)
	if err != nil {
		r.t.Fatal(err)
	}
	return next.Ack, nil
}

// landSecondPage lands the rows that make the second page of the pass end at the bound: a repository row between
// the first page's last row and the bound, and a row of table later past it.
func (r *boundedRetryRig) landSecondPage() {
	r.repos.rows = append(r.repos.rows, keysetRow{at: pageCutStamp, key: "a150z"})
	r.later = keysetRows{{at: pageCutStamp.Add(30 * time.Second), key: "0"}}
}

// passAtSecondPage runs the first call and applies its batch, so the pass is open and its next page is the first
// one the bound ends.
func (r *boundedRetryRig) passAtSecondPage() {
	r.t.Helper()
	ack, err := r.call()
	if err != nil || ack == "" {
		r.t.Fatalf("first call: ack=%q err=%v, want the batch of a page the bound did not end", ack, err)
	}
	if lines := boundedPageLines(r.t, r.logs); len(lines) != 0 {
		r.t.Fatalf("the first page wrote %d bounded-page lines, want none: %v", len(lines), lines)
	}
	r.plan.window.settle(r.plan.windowScope, ack)
	if pass := r.plan.window.pass(r.plan.windowScope); !pass.walking || pass.bounded != 0 {
		r.t.Fatalf("pass after the first batch = walking %v, bounded %d, want an open pass with no bounded page", pass.walking, pass.bounded)
	}
	r.landSecondPage()
}

func (r *boundedRetryRig) requireOneLineAndOneBoundedPage(after string) {
	r.t.Helper()
	if lines := boundedPageLines(r.t, r.logs); len(lines) != 1 {
		r.t.Fatalf("after %s the pass wrote %d bounded-page lines, want 1: %v", after, len(lines), lines)
	}
	if got := r.plan.window.pass(r.plan.windowScope).bounded; got != 1 {
		r.t.Fatalf("after %s pass_bounded_pages = %d, want 1: a retry of the page must not count it again", after, got)
	}
}

// A window batch that is not acknowledged puts the pass back to where it stood before the page; the retry walks
// the same bounded page again.
func TestBoundedPageLineIsNotWrittenAgainWhenTheRetryFollowsAnUnackedBatch(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.passAtSecondPage()
	if ack, err := r.call(); err != nil || ack == "" {
		t.Fatalf("second call: ack=%q err=%v, want the batch of the bounded page", ack, err)
	}
	r.requireOneLineAndOneBoundedPage("the bounded page")
	r.plan.window.settle(r.plan.windowScope, "")
	for retry := 1; retry <= 2; retry++ {
		ack, err := r.call()
		if err != nil || ack == "" {
			t.Fatalf("retry %d: ack=%q err=%v, want the same batch again", retry, ack, err)
		}
		r.requireOneLineAndOneBoundedPage(fmt.Sprintf("retry %d", retry))
		r.plan.window.settle(r.plan.windowScope, "")
	}
}

// A table read that fails after the bounded page was walked returns before the pass is saved; the retry walks the
// page again.
func TestBoundedPageLineIsNotWrittenAgainWhenTheRetryFollowsAFailedRead(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.passAtSecondPage()
	var second []candidate
	for n := 100; n < 200; n++ {
		second = append(second, candidate{table: "full", observedAt: pageCutStamp, sortKey: fmt.Sprintf("a%03d", n)})
	}
	second = append(second, candidate{table: "repos", observedAt: pageCutStamp, sortKey: "a150z"})
	r.plan.window.record(r.plan.windowScope, second, r.plan.overlap)
	r.failAfter = "a199"
	for retry := 1; retry <= 2; retry++ {
		if _, err := r.call(); !errors.Is(err, errBoundedRetryRead) {
			t.Fatalf("retry %d: err = %v, want the read failure on the page after the bounded one", retry, err)
		}
		if lines := boundedPageLines(t, r.logs); len(lines) != 1 {
			t.Fatalf("after failed attempt %d the pass wrote %d bounded-page lines, want 1: %v", retry, len(lines), lines)
		}
		if got := r.plan.window.pass(r.plan.windowScope).bounded; got != 0 {
			t.Fatalf("a failed read saved the pass: pass_bounded_pages = %d, want the 0 it held before", got)
		}
	}
}

// The first page of a pass is the bounded one and its batch is not acknowledged: the pass goes back to before it
// started, and the retry starts it again. That is still the same pass.
func TestBoundedPageLineIsNotWrittenAgainWhenTheRetryStartsTheRolledBackPassAgain(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.landSecondPage()
	for attempt := 1; attempt <= 3; attempt++ {
		ack, err := r.call()
		if err != nil || ack == "" {
			t.Fatalf("attempt %d: ack=%q err=%v, want the batch of the bounded first page", attempt, ack, err)
		}
		r.requireOneLineAndOneBoundedPage(fmt.Sprintf("attempt %d", attempt))
		r.plan.window.settle(r.plan.windowScope, "")
	}
}

// After the rollback the frontier moved (rows were paged in), so the pass the retry starts is another window and
// writes its own line.
func TestBoundedPageLineIsWrittenByAnotherWindowStartedAfterARollback(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.landSecondPage()
	if ack, err := r.call(); err != nil || ack == "" {
		t.Fatalf("first call: ack=%q err=%v, want the batch of the bounded first page", ack, err)
	}
	r.requireOneLineAndOneBoundedPage("the bounded first page")
	r.plan.window.settle(r.plan.windowScope, "")
	r.frontier = cursorState{Since: pageCutStamp.Add(2 * time.Minute), After: "zz"}
	if ack, err := r.call(); err != nil || ack == "" {
		t.Fatalf("call after the frontier moved: ack=%q err=%v, want a batch", ack, err)
	}
	if lines := boundedPageLines(t, r.logs); len(lines) != 2 {
		t.Fatalf("the pass of the new window wrote %d bounded-page lines in all, want 2", len(lines))
	}
}

// Passes that start one after another on a clock that does not move can cover the same window; each still writes
// its own line, one per pass started.
func TestBoundedPageLineIsWrittenOncePerPassOverTheSameWindow(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.plan.windowInterval = 0
	r.landSecondPage()
	for call := 0; call < 80 && r.plan.window.pass(r.plan.windowScope).seq < 4; call++ {
		ack, err := r.call()
		if err != nil {
			t.Fatal(err)
		}
		if ack != "" {
			r.plan.window.settle(r.plan.windowScope, ack)
		}
	}
	started := r.plan.window.pass(r.plan.windowScope).seq
	if started < 4 {
		t.Fatalf("only %d passes started in 80 calls", started)
	}
	if lines := boundedPageLines(t, r.logs); len(lines) != started {
		t.Fatalf("%d passes started and %d bounded-page lines were written, want one per pass", started, len(lines))
	}
}

// A new pass writes its own line: the marker names the pass, it is not kept for the scope's life.
func TestBoundedPageLineIsWrittenAgainByTheNextPass(t *testing.T) {
	r := newBoundedRetryRig(t)
	r.passAtSecondPage()
	drain := func(until func() bool) {
		t.Helper()
		for call := 0; call < 60 && !until(); call++ {
			ack, err := r.call()
			if err != nil {
				t.Fatal(err)
			}
			if ack != "" {
				r.plan.window.settle(r.plan.windowScope, ack)
			}
		}
	}
	drain(func() bool { return !r.plan.window.pass(r.plan.windowScope).walking })
	if lines := boundedPageLines(t, r.logs); len(lines) != 1 {
		t.Fatalf("the first pass wrote %d bounded-page lines, want 1: one per pass", len(lines))
	}
	r.now = r.now.Add(2 * time.Hour)
	drain(func() bool { return len(boundedPageLines(t, r.logs)) == 2 })
	if lines := boundedPageLines(t, r.logs); len(lines) != 2 {
		t.Fatalf("the second pass wrote %d bounded-page lines in all, want 2: one per pass", len(lines))
	}
}

// twoPassCallRig leaves the next call to end the resumed pass and start the next one in the same call: every row
// the resumed pass has left is seen, and one row landed behind its walk, which the next pass emits.
func twoPassCallRig(t *testing.T) *boundedRetryRig {
	t.Helper()
	r := newBoundedRetryRig(t)
	r.plan.windowInterval = 0
	r.plan.windowPagesPerCall = 50
	r.landSecondPage()
	ack, err := r.call()
	if err != nil || ack == "" {
		t.Fatalf("first call: ack=%q err=%v, want the batch of the bounded first page", ack, err)
	}
	r.plan.window.settle(r.plan.windowScope, ack)
	var seen []candidate
	for n := 100; n < 600; n++ {
		seen = append(seen, candidate{table: "full", observedAt: pageCutStamp, sortKey: fmt.Sprintf("a%03d", n)})
	}
	seen = append(seen,
		candidate{table: "repos", observedAt: pageCutStamp, sortKey: "a150z"},
		candidate{table: "later", observedAt: pageCutStamp.Add(30 * time.Second), sortKey: "0"})
	r.plan.window.record(r.plan.windowScope, seen, r.plan.overlap)
	r.repos.rows = append(r.repos.rows, keysetRow{at: pageCutStamp.Add(-2 * time.Second), key: "00000000-0000-4000-8000-000000000002"})
	return r
}

func (r *boundedRetryRig) seq() int { return r.plan.window.pass(r.plan.windowScope).seq }

// One call ends the resumed pass and starts the next; both write their line once, and a rollback of that call's
// batch makes neither write it again.
func TestBoundedPageLineIsNotWrittenAgainWhenOneCallRunsTwoPasses(t *testing.T) {
	r := twoPassCallRig(t)
	before := r.seq()
	ack, err := r.call()
	if err != nil || ack == "" {
		t.Fatalf("second call: ack=%q err=%v, want the batch of the row that landed late", ack, err)
	}
	if got := r.seq() - before; got != 1 {
		t.Fatalf("the call started %d passes, want the resumed pass to end and one more to start", got)
	}
	if lines := boundedPageLines(t, r.logs); len(lines) != 2 {
		t.Fatalf("the two passes wrote %d bounded-page lines, want one each", len(lines))
	}
	for retry := 1; retry <= 2; retry++ {
		r.plan.window.settle(r.plan.windowScope, "")
		if ack, err := r.call(); err != nil || ack == "" {
			t.Fatalf("retry %d: ack=%q err=%v, want the same batch again", retry, ack, err)
		}
		if lines := boundedPageLines(t, r.logs); len(lines) != 2 {
			t.Fatalf("after retry %d the passes wrote %d bounded-page lines in all, want 2", retry, len(lines))
		}
	}
}

const boundedPageMessage = "devhealthsource page ended at the last row a truncated table returned"

// retriesAcrossAPassBoundary runs four attempts of the call that ends the resumed pass B and starts a successor,
// each failing the way failed says (an unacknowledged batch, or a table read that fails right after the
// successor's line), with the frontier fixed or moving on each attempt. B writes its line once. Every window the
// successor starts on writes one more, once, so the lines are 1 + the windows.
func retriesAcrossAPassBoundary(t *testing.T, failed, advance bool) *boundedRetryRig {
	t.Helper()
	r := twoPassCallRig(t)
	if failed {
		r.repos.rows = r.repos.rows[:len(r.repos.rows)-1]
	}
	base := r.frontier
	lines := func() int { return len(boundedPageLines(t, r.logs)) }
	windows := map[time.Time]bool{}
	for attempt := 1; attempt <= 4; attempt++ {
		if advance {
			r.frontier = cursorState{Since: base.Since.Add(time.Duration(attempt) * time.Minute), After: base.After}
		}
		windows[r.frontier.Since] = true
		if failed {
			// The first attempt fails right after the successor's line; the later ones fail at the same read.
			if attempt == 1 {
				r.failAtLines = lines() + 1
			} else {
				r.failAtLines, r.failAtRead = 0, r.failedAtRead
			}
			if _, err := r.call(); !errors.Is(err, errBoundedRetryRead) {
				t.Fatalf("attempt %d: err = %v, want the read failure after the successor's line", attempt, err)
			}
		} else {
			if ack, err := r.call(); err != nil || ack == "" {
				t.Fatalf("attempt %d: ack=%q err=%v, want the batch of the row that landed late", attempt, ack, err)
			}
			r.plan.window.settle(r.plan.windowScope, "")
		}
		if want := 1 + len(windows); lines() != want {
			t.Fatalf("after attempt %d the passes wrote %d bounded-page lines, want %d: the resumed pass once and one per window of its successor", attempt, lines(), want)
		}
		r.plan.window.settle(r.plan.windowScope, "")
	}
	return r
}

func TestBoundedPageLineOfAResumedPassIsNotRepeatedAcrossAPassBoundary(t *testing.T) {
	for _, tc := range []struct {
		name            string
		failed, advance bool
	}{
		{"unacked batch, fixed frontier", false, false},
		{"unacked batch, frontier advances", false, true},
		{"failed read, fixed frontier", true, false},
		{"failed read, frontier advances", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) { retriesAcrossAPassBoundary(t, tc.failed, tc.advance) })
	}
}

// The markers kept are bounded by the failed attempts since the last acknowledged batch, and an acknowledged
// batch leaves the live pass only.
func TestBoundedPageMarkersAreBoundedByFailedAttemptsAndPrunedByAnAck(t *testing.T) {
	r := retriesAcrossAPassBoundary(t, false, true)
	scope := r.plan.window.scopes[r.plan.windowScope]
	if got := len(scope.boundedWarned); got != 5 {
		t.Fatalf("after 4 failed attempts the memo keeps %d markers, want at most 4 + 1: the resumed pass and one window per attempt", got)
	}
	ack, err := r.call()
	if err != nil || ack == "" {
		t.Fatalf("ack attempt: ack=%q err=%v, want a batch", ack, err)
	}
	r.plan.window.settle(r.plan.windowScope, ack)
	if got := len(scope.boundedWarned); got != 1 {
		t.Fatalf("after an acknowledged batch the memo keeps %d markers, want the live pass only", got)
	}
}
