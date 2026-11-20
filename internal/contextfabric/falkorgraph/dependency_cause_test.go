package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/projectionrun"
	"github.com/redis/go-redis/v9"
)

const plantedAddress = "10.1.2.3:6379"

func opErrorAt(errno syscall.Errno) error {
	return &net.OpError{Op: "read", Net: "tcp", Addr: &net.TCPAddr{IP: net.IPv4(10, 1, 2, 3), Port: 6379}, Err: &os.SyscallError{Syscall: "read", Err: errno}}
}

func servedDependencyText(operation string) string {
	return fmt.Errorf("context fabric graph dependency error during %s: %w", operation, contextfabric.ErrUnavailable).Error()
}

func requireDependencyCause(t *testing.T, err error, want string) {
	t.Helper()
	got, ok := contextfabric.DependencyCause(err)
	if !ok || got != want {
		t.Fatalf("DependencyCause(%v) = %q, %v; want %q, true", err, got, ok, want)
	}
}

// TestTheProbeCausesMapToTheirReadFailureTokens is the premise probe's five
// causes at the FalkorDB SDK boundary (classifyFalkorError): each maps to its
// read failure token; only the cause with no token maps to "other".
func TestTheProbeCausesMapToTheirReadFailureTokens(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  error
		want string
	}{
		{"eof", io.EOF, ReadFailureEOF},
		{"reset with an address", opErrorAt(syscall.ECONNRESET), ReadFailureConnectionReset},
		{"real refused dial", realRefused(t), ReadFailureConnectionRefused},
		{"timeout", os.ErrDeadlineExceeded, ReadFailureTimeout},
		{"tls alert, no token", errors.New("remote error: tls: bad certificate"), DependencyCauseOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireDependencyCause(t, classifyFalkorError("bootstrap relationship index", tc.raw), tc.want)
		})
	}
}

// TestEveryDriverCauseMapsToItsClassAndKeepsTheServedText: the residual arm
// keeps today's text byte for byte and still reads as ErrUnavailable.
func TestEveryDriverCauseMapsToItsClassAndKeepsTheServedText(t *testing.T) {
	const operation = "query context graph"
	for _, tc := range []struct {
		name string
		raw  error
		want string
	}{
		{"real EOF", realEOF(t), ReadFailureEOF},
		{"unexpected EOF", fmt.Errorf("read: %w", io.ErrUnexpectedEOF), ReadFailureEOF},
		{"real refused dial", realRefused(t), ReadFailureConnectionRefused},
		{"real read timeout", realReadTimeout(t), ReadFailureTimeout},
		{"connection reset", opErrorAt(syscall.ECONNRESET), ReadFailureConnectionReset},
		{"broken pipe", opErrorAt(syscall.EPIPE), ReadFailureBrokenPipe},
		{"connection aborted", opErrorAt(syscall.ECONNABORTED), ReadFailureConnectionAborted},
		{"bare loading", errors.New("LOADING"), ReadFailureLoading},
		{"loading with detail", errors.New("LOADING Redis is loading the dataset in memory"), ReadFailureLoading},
		{"pool timeout", redis.ErrPoolTimeout, ReadFailurePoolTimeout},
		{"unknown reply", errors.New("ERR something new"), DependencyCauseOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flat := classifyFalkorError(operation, tc.raw)
			requireDependencyCause(t, flat, tc.want)
			if got, want := flat.Error(), servedDependencyText(operation); got != want {
				t.Fatalf("text = %q, want the unchanged served text %q", got, want)
			}
			if !errors.Is(flat, contextfabric.ErrUnavailable) {
				t.Fatalf("%v does not wrap ErrUnavailable", flat)
			}
		})
	}
}

// TestAFlattenedDependencyErrorCarriesNoAddressOrSecret plants an address, a
// password and a host name in the raw error.
func TestAFlattenedDependencyErrorCarriesNoAddressOrSecret(t *testing.T) {
	raws := []error{
		opErrorAt(syscall.ECONNRESET),
		fmt.Errorf("dial falkordb.internal:6379 password=hunter2: %w", io.EOF),
		errors.New("WRONGTYPE at " + plantedAddress + " password=hunter2"),
	}
	for _, raw := range raws {
		flat := classifyFalkorError("project entity", raw)
		cause, _ := contextfabric.DependencyCause(flat)
		for _, planted := range []string{plantedAddress, "10.1.2.3", "hunter2", "falkordb.internal"} {
			if strings.Contains(flat.Error(), planted) || strings.Contains(cause, planted) {
				t.Fatalf("%q reached the flattened error: text=%q cause=%q", planted, flat.Error(), cause)
			}
		}
		var opErr *net.OpError
		if errors.As(flat, &opErr) || errors.Is(flat, raw) {
			t.Fatalf("the raw driver error is reachable from %v", flat)
		}
	}
}

// TestARewrappedDependencyErrorKeepsItsCause: a caller that flattens an
// already flattened error (ProjectionWatermark does) keeps the first cause and
// takes its own operation text.
func TestARewrappedDependencyErrorKeepsItsCause(t *testing.T) {
	inner := classifyFalkorError("query context graph", opErrorAt(syscall.ECONNRESET))
	outer := safeDependencyError("read projection watermark", fmt.Errorf("wrapped: %w", inner))
	requireDependencyCause(t, outer, ReadFailureConnectionReset)
	if got, want := outer.Error(), servedDependencyText("read projection watermark"); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

// TestKnownSentinelsCarryNoDependencyCause: a classified error keeps today's
// result and has no driver cause.
func TestKnownSentinelsCarryNoDependencyCause(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"unauthorized", classifyFalkorError("query context graph", errors.New("WRONGPASS invalid username-password pair")), ErrUnauthorized},
		{"empty key", classifyFalkorError("query context graph", errors.New("Invalid graph operation on empty key")), ErrNotFound},
		{"canceled", context.Canceled, context.Canceled},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		got := safeDependencyError("read projection watermark", tc.err)
		if !errors.Is(got, tc.want) {
			t.Fatalf("%s: %v does not wrap %v", tc.name, got, tc.want)
		}
		if cause, ok := contextfabric.DependencyCause(got); ok {
			t.Fatalf("%s: a classified error carries dependency cause %q", tc.name, cause)
		}
	}
}

// TestRealSocketFailuresCarryTheirCause runs the real sdkAPI and go-redis.
func TestRealSocketFailuresCarryTheirCause(t *testing.T) {
	ctx := context.Background()
	d := newConnDropper()
	s := retrySDKAPI(t, droppingRESPServer(t, d), 1, nil)
	d.arm("GRAPH.QUERY", -1)
	_, err := s.query(ctx, "g", "CREATE (:Probe)", nil, false)
	requireDependencyCause(t, err, ReadFailureEOF)
	d.arm("GRAPH.RO_QUERY", -1)
	_, err = s.query(ctx, "g", "MATCH (n) RETURN n", nil, true)
	requireDependencyCause(t, err, ReadFailureEOF)

	closed := retrySDKAPI(t, closedTCPAddr(t), 1, nil)
	_, err = closed.query(ctx, "g", "CREATE (:Probe)", nil, false)
	requireDependencyCause(t, err, ReadFailureConnectionRefused)
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("the address reached the error text: %v", err)
	}
}

// TestTheProjectorTickLineNamesTheDependencyCause drives a real flattened
// error through the real projector observer: the line carries
// dependency_cause and no field carries the planted address.
func TestTheProjectorTickLineNamesTheDependencyCause(t *testing.T) {
	var buf bytes.Buffer
	observer := projectionrun.SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}
	flat := classifyFalkorError("project entity", opErrorAt(syscall.ECONNRESET))
	observer.ObserveProjectionOutcome(projectionrun.Outcome{OrgID: "org_1", Source: "devhealth", Duration: time.Second, Err: fmt.Errorf("apply batch: %w", flat)})
	line := buf.String()
	if !strings.Contains(line, `"dependency_cause":"connection_reset"`) {
		t.Fatalf("the tick line has no dependency_cause: %s", line)
	}
	if strings.Contains(line, "10.1.2.3") || strings.Contains(line, "6379") {
		t.Fatalf("the tick line carries the planted address: %s", line)
	}

	buf.Reset()
	observer.ObserveProjectionOutcome(projectionrun.Outcome{OrgID: "org_1", Source: "devhealth", Duration: time.Second, Err: errors.New("clickhouse: code 307")})
	if strings.Contains(buf.String(), "dependency_cause") {
		t.Fatalf("a non-graph error carries dependency_cause: %s", buf.String())
	}
}

type refusingEpochResolver struct{ err error }

func (r refusingEpochResolver) ResolveActiveEpoch(context.Context, string) (int64, error) {
	return 0, r.err
}

func (r refusingEpochResolver) ResolveBuildEpoch(context.Context, string) (int64, bool, error) {
	return 0, false, r.err
}

// TestOtherDependencyRefusalsCarryNoFalkorDBCause: safeDependencyError also
// flattens Postgres (epoch resolution) and ClickHouse (identity universe)
// errors. A connection refusal there gets no driver cause, on the error and on
// the projector line, while the same refusal at the FalkorDB SDK boundary does.
func TestOtherDependencyRefusalsCarryNoFalkorDBCause(t *testing.T) {
	refused := realRefused(t)
	adapter, err := newWithAPI(Config{Addr: "127.0.0.1:1", AllowInsecure: true, GraphPrefix: "acr-cf-cause", EpochResolver: refusingEpochResolver{err: refused}}, &fakeConn{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_, activeErr := adapter.resolveActiveEpoch(ctx, "org_1")
	_, buildErr := adapter.resolveWriteKey(ctx, "org_1")
	identityErr := safeDependencyError("read identity universe", refused)
	for name, flat := range map[string]error{"postgres active epoch": activeErr, "postgres build epoch": buildErr, "clickhouse identity universe": identityErr} {
		if !errors.Is(flat, contextfabric.ErrUnavailable) {
			t.Fatalf("%s: %v does not wrap ErrUnavailable", name, flat)
		}
		if cause, ok := contextfabric.DependencyCause(flat); ok {
			t.Fatalf("%s: a non-FalkorDB refusal carries dependency cause %q", name, cause)
		}
		var buf bytes.Buffer
		projectionrun.SlogObserver{Logger: slog.New(slog.NewJSONHandler(&buf, nil))}.ObserveProjectionOutcome(projectionrun.Outcome{OrgID: "org_1", Source: "devhealth", Duration: time.Second, Err: flat})
		if strings.Contains(buf.String(), "dependency_cause") {
			t.Fatalf("%s: the projector line carries dependency_cause: %s", name, buf.String())
		}
	}
	requireDependencyCause(t, classifyFalkorError("query context graph", refused), ReadFailureConnectionRefused)
}
