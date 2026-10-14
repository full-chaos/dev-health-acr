package factoracle

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVenuePlanesPaceAndCountRateLimitWaits(t *testing.T) {
	var calls atomic.Int32
	var stamps [4]time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if int(n) <= len(stamps) {
			stamps[n-1] = time.Now()
		}
		if n == 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{"structuredContent":{"ok":true}}}`)
	}))
	defer server.Close()
	token := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(token, []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logs []string
	planes := &VenuePlanes{URL: server.URL, TokenFile: token, MinInterval: 300 * time.Millisecond,
		Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for i := 0; i < 2; i++ {
		if _, err := planes.call(ctx, "read_facts", map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
	count, waited := planes.LimitedCalls()
	if count != 1 || waited != time.Second {
		t.Fatalf("limited answers %d waited %s, want 1 and 1s", count, waited)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "429 count 1") {
		t.Fatalf("the Retry-After wait was not logged once: %v", logs)
	}
	if got := stamps[1].Sub(stamps[0]); got < 250*time.Millisecond {
		t.Fatalf("two calls %s apart, want about the 300ms pacing interval", got)
	}
}
