package falkorgraph

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
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
// a counted send (a resend after the cut is a second one), each one's reply is
// read back before the next goes out, and a Close that came first takes
// effect only after the last reply is read.
func TestHeldSendReachesTheWireOnlyAfterOpen(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	gate := newReplyGate()
	conn := &gatedConn{Conn: client, gate: gate}
	command := []byte("*4\r\n$11\r\nGRAPH.QUERY\r\n$3\r\nkey\r\n$8\r\nRETURN 1\r\n$9\r\n--compact\r\n")
	reply := []byte("*3\r\n*0\r\n*0\r\n*1\r\n$5\r\nstats\r\n")
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
	type peerView struct {
		commands [][]byte
		err      error
		rest     []byte
	}
	seen := make(chan peerView, 1)
	go func() {
		view := peerView{}
		for i := 0; i < 2; i++ {
			got := make([]byte, len(command))
			if _, err := io.ReadFull(server, got); err != nil {
				view.err = fmt.Errorf("read command %d: %w", i, err)
				seen <- view
				return
			}
			view.commands = append(view.commands, got)
			if _, err := server.Write(reply); err != nil {
				view.err = fmt.Errorf("reply %d: %w (the connection closed before the reply was read)", i, err)
				seen <- view
				return
			}
		}
		view.rest, view.err = io.ReadAll(server)
		seen <- view
	}()
	gate.open()
	drained, err := gate.settle()
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if drained != 2 {
		t.Fatalf("replies drained = %d, want 2", drained)
	}
	select {
	case view := <-seen:
		if view.err != nil {
			t.Fatalf("peer: %v", view.err)
		}
		if len(view.commands) != 2 || !bytes.Equal(view.commands[0], command) || !bytes.Equal(view.commands[1], command) {
			t.Fatalf("peer received %q, want both held commands", view.commands)
		}
		if len(view.rest) != 0 {
			t.Fatalf("peer received %q after the held commands", view.rest)
		}
	case <-time.After(stallBound):
		t.Fatal("the connection was not closed after the held commands were answered")
	}
}

// A held command whose reply never comes fails the flush, naming the reply read.
func TestHeldSendWithNoReplyFailsTheFlush(t *testing.T) {
	client, server := net.Pipe()
	gate := newReplyGate()
	conn := &gatedConn{Conn: client, gate: gate}
	gate.holdSends()
	go func() {
		got := make([]byte, len("GRAPH.QUERY"))
		_, _ = io.ReadFull(server, got)
		_ = server.Close()
	}()
	if _, err := conn.Write([]byte("GRAPH.QUERY")); err != nil {
		t.Fatal(err)
	}
	gate.open()
	drained, err := gate.settle()
	if err == nil || !strings.Contains(err.Error(), "read the reply") {
		t.Fatalf("flush error = %v, want a failed reply read", err)
	}
	if drained != 0 {
		t.Fatalf("replies drained = %d, want 0", drained)
	}
	_ = conn.Close()
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
	if _, err := gate.settle(); err != nil {
		t.Fatal(err)
	}
}
