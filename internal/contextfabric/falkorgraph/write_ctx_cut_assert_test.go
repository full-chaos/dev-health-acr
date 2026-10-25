package falkorgraph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

func TestCutLandedMeasuresTheDeadlineNotTheReturnTime(t *testing.T) {
	deadline := time.UnixMilli(1_000_050).Add(500 * time.Microsecond)
	deadlineMs := deadline.UnixMilli()
	at := func(ms int64) time.Time { return time.UnixMilli(ms) }
	for name, tc := range map[string]struct {
		sent    time.Time
		landing probeLanding
		want    cutVerdict
	}{
		"sent before, committed after: a cut, however late the caller returned": {at(1_000_001), probeLanding{1, 1_000_400}, cutMidCommand},
		"committed one ms after the deadline":                                   {at(1_000_001), probeLanding{1, deadlineMs + 1}, cutMidCommand},
		"committed at the deadline is not a cut":                                {at(1_000_001), probeLanding{1, deadlineMs}, cutCommittedBeforeDeadline},
		"committed before the deadline is not a cut":                            {at(1_000_001), probeLanding{1, deadlineMs - 1}, cutCommittedBeforeDeadline},
		"sent and never landed":                                                 {at(1_000_001), probeLanding{0, 0}, cutNotLanded},
		"sent after the deadline: no cut was measured":                          {deadline.Add(5 * time.Millisecond), probeLanding{1, deadlineMs + 400}, cutNeverSent},
		"sent at the deadline: no cut was measured":                             {deadline, probeLanding{1, deadlineMs + 400}, cutNeverSent},
		"never sent": {time.Time{}, probeLanding{0, 0}, cutNeverSent},
		"sent in the deadline's millisecond but before it: a cut, committed after": {deadline.Add(-200 * time.Microsecond), probeLanding{1, deadlineMs + 300}, cutMidCommand},
	} {
		if got := classifyCut(tc.sent, deadline, tc.landing); got != tc.want {
			t.Errorf("%s: verdict %d, want %d", name, got, tc.want)
		}
	}
}

// A recording gate stamps the first send and holds nothing back.
func TestRecordingGateStampsTheSendAndHoldsNoReply(t *testing.T) {
	gate := newReplyGate()
	gate.record()
	if _, holding := gate.holding(); holding {
		t.Fatal("a recording gate holds replies back")
	}
	if !gate.recording() {
		t.Fatal("a recording gate is not recording")
	}
	gate.arm()
	if _, holding := gate.holding(); !holding {
		t.Fatal("an armed gate does not hold replies back")
	}
	gate.open()
}

// A held probe command reaches the peer only after open, each held command is
// a counted send (a resend after the cut is a second one), and a Close that
// came first takes effect only after the held commands are written.
func TestHeldSendReachesTheWireOnlyAfterOpen(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	gate := newReplyGate()
	conn := &gatedConn{Conn: client, gate: gate}
	command := []byte("*4\r\n$11\r\nGRAPH.QUERY\r\n$3\r\nkey\r\n$8\r\nRETURN 1\r\n$9\r\n--compact\r\n")
	gate.holdSends()
	accepted := make(chan error, 1)
	go func() {
		for i := 0; i < 2; i++ {
			if n, err := conn.Write(command); err != nil || n != len(command) {
				accepted <- fmt.Errorf("write %d: n=%d err=%v", i, n, err)
				return
			}
		}
		accepted <- conn.Close()
	}()
	select {
	case err := <-accepted:
		if err != nil {
			t.Fatalf("held writes and close: %v", err)
		}
	case <-time.After(stallBound):
		t.Fatal("a held write blocked: the command went to the wire before the release")
	}
	if got := gate.writes.Load(); got != 2 {
		t.Fatalf("sends = %d, want 2: every held command is a counted send", got)
	}
	if gate.sentAt.Load() == 0 {
		t.Fatal("a held write was not stamped as sent")
	}
	received := make(chan []byte, 1)
	go func() {
		got, _ := io.ReadAll(server)
		received <- got
	}()
	gate.open()
	gate.flushes.Wait()
	if failed := gate.flushErrors.Load(); failed != 0 {
		t.Fatalf("%d held command(s) failed to reach the peer", failed)
	}
	select {
	case got := <-received:
		if want := append(append([]byte(nil), command...), command...); !bytes.Equal(got, want) {
			t.Fatalf("peer received %q, want both held commands %q", got, want)
		}
	case <-time.After(stallBound):
		t.Fatal("the connection was not closed after the held commands were written")
	}
}

// A held command is refused once its write deadline has passed, as the
// socket refuses it, and is neither held nor counted.
func TestHeldSendPastTheWriteDeadlineIsRefused(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()
	gate := newReplyGate()
	conn := &gatedConn{Conn: client, gate: gate}
	gate.holdSends()
	if err := conn.SetWriteDeadline(time.Now().Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte("GRAPH.QUERY")); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write past the deadline = %v, want os.ErrDeadlineExceeded", err)
	}
	if got := gate.writes.Load(); got != 0 {
		t.Fatalf("sends = %d, want 0", got)
	}
	gate.open()
	gate.flushes.Wait()
}
