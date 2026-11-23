package falkorgraph

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	contractsv1 "github.com/full-chaos/dev-health-acr/internal/contracts/v1"
)

// timeoutRecorder is a RESP server that records the TIMEOUT argument of every
// GRAPH.RO_QUERY and closes the connection without a reply on the first drops
// of them, so the real read client retries.
type timeoutRecorder struct {
	mu    sync.Mutex
	drops int
	sent  []int64
}

func (r *timeoutRecorder) record(args []string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	ms := int64(-1)
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "TIMEOUT" {
			ms, _ = strconv.ParseInt(args[i+1], 10, 64)
		}
	}
	r.sent = append(r.sent, ms)
	if r.drops > 0 {
		r.drops--
		return true
	}
	return false
}

func (r *timeoutRecorder) timeouts() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64(nil), r.sent...)
}

func timeoutRecordingServer(t *testing.T, r *timeoutRecorder) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := bufio.NewReader(c)
				for {
					raw, name, err := readRawRESPCommand(br)
					if err != nil {
						return
					}
					reply := "+OK\r\n"
					switch name {
					case "HELLO":
						reply = "-ERR unknown command 'HELLO'\r\n"
					case "GRAPH.RO_QUERY":
						if r.record(respArgs(raw)) {
							return
						}
						reply = "-ERR test server answers no rows\r\n"
					}
					if _, err := io.WriteString(c, reply); err != nil {
						return
					}
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

// respArgs splits a raw RESP array of bulk strings whose values hold no CRLF.
func respArgs(raw []byte) []string {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\r\n"), "\r\n")
	var args []string
	for i := 2; i < len(lines); i += 2 {
		args = append(args, lines[i])
	}
	return args
}

// A retry of a count read sends what is left of the caller's budget at that
// attempt, not the value the first attempt computed; a retry that would fall
// inside the margin is not sent and the count fails as over budget, never as
// an unavailable dependency.
func TestACountReadRetryRecomputesItsServerTimeout(t *testing.T) {
	t.Run("a retry sends less than the first attempt", func(t *testing.T) {
		rec := &timeoutRecorder{drops: 1}
		s := retrySDKAPI(t, timeoutRecordingServer(t, rec), 3, nil)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = s.query(withServerTimeout(ctx, edgeCountTimeoutCap, edgeCountTimeoutMargin), "g", "MATCH (n) RETURN n", nil, true)
		sent := rec.timeouts()
		if len(sent) != 2 || sent[0] <= 0 || sent[0] > (3*time.Second-edgeCountTimeoutMargin).Milliseconds() || sent[1] <= 0 || sent[1] >= sent[0] {
			t.Fatalf("TIMEOUT per attempt = %v, want two attempts, the retry below the first and both under %d ms", sent, (3*time.Second - edgeCountTimeoutMargin).Milliseconds())
		}
	})
	t.Run("a retry inside the margin is not sent", func(t *testing.T) {
		rec := &timeoutRecorder{drops: 1}
		s := retrySDKAPI(t, timeoutRecordingServer(t, rec), 3, nil)
		adapter, err := newWithAPI(Config{
			Addr: "fake:6379", GraphPrefix: "acr-cf-retry", RequestTimeout: 3 * time.Second, MaxAttempts: 3, MaxResults: 25, PoolSize: 2, AllowInsecure: true,
		}, s)
		if err != nil {
			t.Fatal(err)
		}
		// 290 ms: the first attempt has 40 ms past the margin; after the first
		// retry wait (50 ms) none is left.
		ctx, cancel := context.WithTimeout(context.Background(), edgeCountTimeoutMargin+40*time.Millisecond)
		defer cancel()
		_, err = adapter.CountEdges(ctx, "org-1", contextfabric.EdgeCountKey{Relation: contractsv1.ContextFabricRelationshipRelatedTo})
		sent := rec.timeouts()
		class := projectionrun.ClassifyFailure(err)
		if len(sent) != 1 || sent[0] <= 0 || sent[0] > 40 || !errors.Is(err, contextfabric.ErrQueryBudgetExceeded) || errors.Is(err, contextfabric.ErrUnavailable) || class != "query_budget_exceeded" {
			t.Fatalf("TIMEOUT per attempt = %v, err %v (class %q); want one attempt of at most 40 ms and query_budget_exceeded", sent, err, class)
		}
	})
}
