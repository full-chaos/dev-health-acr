package embedprovider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIsTransientEmbedErrorClassifiesPackageSentinelsAsPersistent pins that
// this package's own structural/identity errors are never worth retrying:
// nothing about a malformed response, a dimension mismatch, or a wrong
// serving model changes on an identical retry (CHAOS-4259).
func TestIsTransientEmbedErrorClassifiesPackageSentinelsAsPersistent(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{ErrResponseShape, ErrDimensionMismatch, ErrModelIdentityMismatch} {
		if IsTransientEmbedError(sentinel) {
			t.Fatalf("%v must classify as PERSISTENT (no retry)", sentinel)
		}
		// The real call sites wrap sentinels (fmt.Errorf("...: %w", ...)); the
		// classifier must see through that wrapping via errors.Is/As.
		wrapped := errWrap(sentinel)
		if IsTransientEmbedError(wrapped) {
			t.Fatalf("wrapped %v must still classify as PERSISTENT", sentinel)
		}
	}
}

func errWrap(err error) error {
	return &wrappedErr{err}
}

type wrappedErr struct{ err error }

func (w *wrappedErr) Error() string { return "wrapped: " + w.err.Error() }
func (w *wrappedErr) Unwrap() error { return w.err }

func TestIsTransientEmbedErrorNilIsPersistent(t *testing.T) {
	t.Parallel()
	if IsTransientEmbedError(nil) {
		t.Fatal("a nil error must classify as PERSISTENT (nothing to retry)")
	}
}

func TestIsTransientEmbedErrorContextCancelIsPersistent(t *testing.T) {
	t.Parallel()
	if IsTransientEmbedError(context.Canceled) {
		t.Fatal("a caller cancellation must not be retried by this classifier")
	}
}

// TestIsTransientEmbedErrorClassifiesByHTTPStatus pins the exact HTTP-status
// boundary: 429 and every 5xx are transient (worth a bounded retry); every
// other definitive status the server returned (400/401/403/404) is
// persistent -- an identical retry gets the identical judgment.
func TestIsTransientEmbedErrorClassifiesByHTTPStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		status    int
		transient bool
	}{
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusGatewayTimeout, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
	}
	for _, tc := range cases {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			t.Parallel()
			server := embeddingsServer(t, func(inputs []string) (int, any) {
				return tc.status, map[string]any{"error": map[string]any{"message": "denied"}}
			})
			embedder, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, embedErr := embedder.Embed(context.Background(), []string{"a"})
			if embedErr == nil {
				t.Fatalf("status %d must surface as an error", tc.status)
			}
			if got := IsTransientEmbedError(embedErr); got != tc.transient {
				t.Fatalf("IsTransientEmbedError(status %d) = %v, want %v (err: %v)", tc.status, got, tc.transient, embedErr)
			}
		})
	}
}

// TestIsTransientEmbedErrorDeadlineExceededIsTransient pins that a call
// timeout (Timeout or BatchTimeout expiring) is worth a bounded retry -- a
// slow response is exactly the "network blip" case the retry exists for.
func TestIsTransientEmbedErrorDeadlineExceededIsTransient(t *testing.T) {
	t.Parallel()
	cfg := testConfig("")
	cfg.Timeout = 10 * time.Millisecond
	cfg.BatchTimeout = 10 * time.Millisecond
	server := embeddingsServer(t, func(inputs []string) (int, any) {
		time.Sleep(200 * time.Millisecond)
		return http.StatusOK, okResponse(embeddingItem(0, 1))
	})
	cfg.BaseURL = server.URL
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, embedErr := embedder.Embed(context.Background(), []string{"a"})
	if embedErr == nil {
		t.Fatal("expected a timeout error")
	}
	if !IsTransientEmbedError(embedErr) {
		t.Fatalf("a call timeout must classify as TRANSIENT, got persistent for: %v", embedErr)
	}
}

// TestIsTransientEmbedErrorMalformedTwoHundredResponseIsPersistent pins
// codex R1 finding 2: a 2xx response whose body is NOT valid JSON was
// received (unlike the transport-failure default branch, which covers a
// request that never got a response at all) -- a server returning
// unparseable JSON for a given request shape is very likely to keep doing
// so, so this must NOT fall into the "no response received, assume network
// blip" transient default.
func TestIsTransientEmbedErrorMalformedTwoHundredResponseIsPersistent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not valid json"))
	}))
	t.Cleanup(server.Close)
	embedder, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, embedErr := embedder.Embed(context.Background(), []string{"a"})
	if embedErr == nil {
		t.Fatal("a malformed 2xx JSON body must surface as an error")
	}
	if IsTransientEmbedError(embedErr) {
		t.Fatalf("a malformed 2xx response body must classify as PERSISTENT (a response WAS received, just unparseable), got transient for: %v", embedErr)
	}
}

// TestIsTransientEmbedErrorEmptyOrTruncatedTwoHundredResponseIsPersistent
// pins codex R2 finding 1: json.Decode surfaces a fully empty 2xx body as
// io.EOF and a truncated one as io.ErrUnexpectedEOF, neither of which is a
// *json.SyntaxError or *json.UnmarshalTypeError -- so before this fix both
// fell through the malformed-2xx-body classifier (codex R1 finding 2) into
// the "no response received, assume network blip" transient default, even
// though a response WAS received (same reasoning as the malformed-JSON
// case above).
func TestIsTransientEmbedErrorEmptyOrTruncatedTwoHundredResponseIsPersistent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{name: "fully empty 2xx body (io.EOF)", body: ""},
		{name: "truncated 2xx body (io.ErrUnexpectedEOF)", body: `{"data":[{"embedding":[0.1,`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(server.Close)
			embedder, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, embedErr := embedder.Embed(context.Background(), []string{"a"})
			if embedErr == nil {
				t.Fatal("an empty or truncated 2xx body must surface as an error")
			}
			if IsTransientEmbedError(embedErr) {
				t.Fatalf("must classify as PERSISTENT (a response WAS received, just empty/truncated), got transient for: %v", embedErr)
			}
		})
	}
}

// TestIsTransientEmbedErrorConnectionFailureIsTransient pins that a
// transport-layer failure that never reached the server (nothing listening
// on the port) classifies as transient: no *openai.Error exists to inspect
// because no HTTP response was ever received.
func TestIsTransientEmbedErrorConnectionFailureIsTransient(t *testing.T) {
	t.Parallel()
	cfg := testConfig("https://127.0.0.1:1")
	cfg.Timeout = 2 * time.Second
	embedder, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, embedErr := embedder.Embed(context.Background(), []string{"a"})
	if embedErr == nil {
		t.Fatal("expected a connection error dialing a closed port")
	}
	if errors.Is(embedErr, context.DeadlineExceeded) {
		t.Skip("dial resolved as a timeout on this host rather than a refusal; deadline-exceeded case is covered separately")
	}
	if !IsTransientEmbedError(embedErr) {
		t.Fatalf("a connection failure must classify as TRANSIENT, got persistent for: %v", embedErr)
	}
}
