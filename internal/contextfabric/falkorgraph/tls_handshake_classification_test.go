package falkorgraph

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// TestQueryClassifiesATLSHandshakeTimeoutAgainstAPlaintextServer is CHAOS-
// 3809's second red-first regression: the "discriminating evidence" in the
// ticket showed that dialing a genuinely plaintext RESP server with
// TLS-enabled config surfaces, today, as a bare context.DeadlineExceeded --
// indistinguishable from a slow or unreachable server. This spins up a REAL
// plaintext TCP listener (not a mock/stub) that accepts the connection and
// then never speaks -- exactly what a plaintext FalkorDB server does when a
// TLS ClientHello arrives on its plain RESP port: the bytes are unintelligible
// noise it never acknowledges, so the client's TLS handshake hangs until its
// own deadline. Config is built through ConfigFromEnv (not a bare Config{}
// literal), matching the real composition path.
func TestQueryClassifiesATLSHandshakeTimeoutAgainstAPlaintextServer(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Accept the TCP connection but never write a byte back -- a
			// plaintext RESP server has no TLS ServerHello to send, so a
			// TLS ClientHello arriving on it goes unanswered forever. Drain
			// whatever the client sends so the connection stays open rather
			// than being reset.
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	values := map[string]string{
		EnvAddr: listener.Addr().String(),
		// Kept at validate()'s one-second floor so this test stays fast
		// while still exercising the real DialTimeout/ReadTimeout/
		// WriteTimeout wiring newSDKAPI applies from RequestTimeout.
		EnvRequestTimeout: "1s",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	cfg, err := ConfigFromEnv(lookup)
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if !cfg.TLS {
		t.Fatal("cfg.TLS = false, want true (default) -- this test only reproduces the trap when TLS is actually attempted")
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}

	c, err := newSDKAPI(cfg)
	if err != nil {
		t.Fatalf("newSDKAPI() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err = c.query(ctx, "acr-cf-test", "RETURN 1", nil, true)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("query() error = nil, want a TLS handshake timeout")
	}
	if elapsed > 4*time.Second {
		t.Fatalf("query() took %s, want it bounded by RequestTimeout (1s), not the 5s test-level ctx deadline -- the config timeout is not reaching the handshake", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query() error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if !strings.Contains(err.Error(), EnvTLS) {
		t.Fatalf("query() error = %q, want it to name %s so an operator can diagnose a TLS/plaintext mismatch instead of a bare deadline", err.Error(), EnvTLS)
	}
}

// TestClassifyConnErrorDoesNotDecorateADeadlineOnceTheConnectionHasWorked is
// Codex round-1 P1's negative control: classifyConnError must NOT claim a
// TLS/plaintext mismatch for an ordinary query-level timeout on a connection
// that has already proven it works (the everConnected gate). Without this
// guard, a slow query against a genuinely healthy TLS-speaking server would
// be misdiagnosed and an operator told to disable TLS -- unsafe advice for a
// problem TLS has nothing to do with. White-box (same package): constructs
// sdkAPI directly, no network needed -- classifyConnError's decision is pure
// given (config.TLS, everConnected, err).
func TestClassifyConnErrorDoesNotDecorateADeadlineOnceTheConnectionHasWorked(t *testing.T) {
	t.Parallel()
	api := &sdkAPI{config: Config{TLS: true}}

	// Before any success: a bare deadline IS decorated (matches the
	// end-to-end test above -- this is the shape the ticket describes,
	// every call failing identically from the very first one).
	first := api.classifyConnError("query context graph", context.DeadlineExceeded)
	if !strings.Contains(first.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() before any success = %q, want it to name %s", first.Error(), EnvTLS)
	}

	// After a success, the SAME bare deadline must NOT be decorated: the
	// connection has already proven it works, so a later deadline is a
	// query-level timeout, not a handshake problem.
	api.everConnected.Store(true)
	second := api.classifyConnError("query context graph", context.DeadlineExceeded)
	if strings.Contains(second.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() after a prior success = %q, want no TLS mention -- this is an ordinary query timeout on a working connection", second.Error())
	}
	if !errors.Is(second, context.DeadlineExceeded) {
		t.Fatalf("classifyConnError() after a prior success = %v, want it to still wrap context.DeadlineExceeded", second)
	}
}

// TestClassifyConnErrorEverConnectedProofOfLifeTable is the single source of
// truth for "what counts as proof this connection has ever worked" -- the
// design team-lead ordered after four straight Codex rounds each found a
// different way a BLOCKLIST ("anything that isn't context.Canceled or
// context.DeadlineExceeded is proof of life") accepted a shape that was not
// actually proof of a working connection:
//
//   - R1: FalkorDB's own "Query timed out" server response was blanket-
//     decorated as a TLS handshake timeout (it needed to be an exception
//     TO the deadline case, not folded into "not a deadline = proof").
//   - R2: createConstraint/createIndex's idempotent "already exists" reply
//     succeeds via a CLASSIFIED error, not a nil one, so the nil-error-only
//     success marking at each call site never saw it.
//   - R3: the R2 fix's "any non-deadline classification = proof of life"
//     rule also caught context.Canceled, which carries ZERO information
//     about connection health (a caller cancellation can land before,
//     during, or after a real handshake succeeds).
//   - R4 (the design-breaking one): the SAME rule also caught
//     classifyFalkorError's generic, unclassified fallback -- which is
//     exactly what a genuine connection-refused, dropped-mid-handshake EOF,
//     or TLS alert reduces to. That is the STRONGEST possible signal the
//     connection never worked, and the blocklist marked it as proof it did.
//
// A blocklist fails toward "assumed connected": every future/unrecognized
// error shape defaults to proof of life, the unsafe direction (a false
// positive here silently suppresses this ticket's whole diagnosis for a
// connection's entire remaining lifetime). This table is an ALLOWLIST
// instead: everConnectedProofSentinels (client.go) enumerates the CLOSED set
// of classifications that can only be reached via a genuine FalkorDB
// protocol round-trip, plus the literal "Query timed out" text (never
// itself a classified sentinel, but the same proof). Every row below that
// is NOT on that allowlist -- including context.Canceled, a bare
// context.DeadlineExceeded, and, critically, R4's generic transport-failure
// shape -- must leave everConnected false. A future classification added to
// classifyFalkorError without a deliberate allowlist decision is caught by
// this table simply falling through to "not proof" by default, never the
// reverse.
func TestClassifyConnErrorEverConnectedProofOfLifeTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name          string
		raw           error
		wantConnected bool
	}{
		{"already indexed (errAlreadyExists)", errors.New("boom: already indexed"), true},
		{"already exists (errAlreadyExists)", errors.New("relationship type already exists"), true},
		{"unique constraint violation (ErrConstraintViolation)", errors.New("unique constraint violation on node of type X"), true},
		{"no such index (errIndexNotFound)", errors.New("Unable to drop index on :Subject(embedding): no such index"), true},
		{"invalid graph operation on empty key (ErrNotFound)", errors.New("Invalid graph operation on empty key"), true},
		{"WRONGPASS (ErrUnauthorized)", errors.New("WRONGPASS invalid username-password pair"), true},
		{"NOAUTH (ErrUnauthorized)", errors.New("NOAUTH Authentication required"), true},
		{"Query timed out (FalkorDB's own server response)", errors.New("Query timed out"), true},

		{"context.Canceled", context.Canceled, false},
		{"bare context.DeadlineExceeded", context.DeadlineExceeded, false},
		// R4's exact defect: a genuine connection-refused is the STRONGEST
		// possible signal the connection never worked, and the pre-fix
		// blocklist marked it as proof it did (red-first proof of this row:
		// verified failing against the pre-allowlist implementation before
		// this fix, per CHAOS-3809's PR body).
		{"connection refused (generic/unclassified)", errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"), false},
		{"EOF (generic/unclassified)", errors.New("EOF"), false},
		{"TLS alert (generic/unclassified)", errors.New("tls: first record does not look like a TLS handshake"), false},
		{"an error classifyFalkorError has never seen (generic/unclassified)", errors.New("something FalkorDB never documented"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := &sdkAPI{config: Config{TLS: true}}
			api.classifyConnError("query context graph", tc.raw)
			if got := api.everConnected.Load(); got != tc.wantConnected {
				t.Fatalf("classifyConnError(%v) -> everConnected = %v, want %v", tc.raw, got, tc.wantConnected)
			}
		})
	}
}

// TestClassifyConnErrorDoesNotTreatCancellationAsProofOfLife is Codex
// round-3's finding, kept as its own end-to-end sequence test (the table
// above proves ONLY the marking decision; this proves the full consequence
// chain): a cancellation on the very first attempt must not be decorated
// (it is not a timeout) and must not poison the diagnosis for a REAL
// handshake timeout immediately afterward.
func TestClassifyConnErrorDoesNotTreatCancellationAsProofOfLife(t *testing.T) {
	t.Parallel()
	api := &sdkAPI{config: Config{TLS: true}}

	canceled := api.classifyConnError("query context graph", context.Canceled)
	if strings.Contains(canceled.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() for a cancellation = %q, want no TLS mention -- a cancellation is not a timeout", canceled.Error())
	}

	later := api.classifyConnError("query context graph", context.DeadlineExceeded)
	if !strings.Contains(later.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() after an earlier cancellation = %q, want it to still name %s -- the cancellation must not have suppressed this real diagnosis", later.Error(), EnvTLS)
	}
}

// TestClassifyConnErrorRecoversFromAnEarlierGenericTransportFailure is Codex
// round-4's finding, kept as its own end-to-end sequence test alongside the
// table above: an earlier connection-refused (or any other generic,
// unclassified transport failure) must not poison the diagnosis for a REAL
// handshake timeout immediately afterward, the same property R3 proved for
// cancellation.
func TestClassifyConnErrorRecoversFromAnEarlierGenericTransportFailure(t *testing.T) {
	t.Parallel()
	api := &sdkAPI{config: Config{TLS: true}}

	refused := api.classifyConnError("query context graph", errors.New("dial tcp 127.0.0.1:6379: connect: connection refused"))
	if strings.Contains(refused.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() for a connection-refused = %q, want no TLS mention -- refused is a generic transport failure, not a deadline", refused.Error())
	}

	later := api.classifyConnError("query context graph", context.DeadlineExceeded)
	if !strings.Contains(later.Error(), EnvTLS) {
		t.Fatalf("classifyConnError() after an earlier connection-refused = %q, want it to still name %s -- the earlier failure must not have suppressed this real diagnosis", later.Error(), EnvTLS)
	}
}
