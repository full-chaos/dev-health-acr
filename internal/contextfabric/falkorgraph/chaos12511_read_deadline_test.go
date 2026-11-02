package falkorgraph

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/hintsource"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// silentFalkor is a TCP server that speaks just enough RESP to bring up the real
// go-redis client, then takes a graph query and never answers it. It stands for
// a FalkorDB query in flight: the bytes are on the wire and no reply will come.
type silentFalkor struct {
	listener net.Listener
	queries  atomic.Int64
	seen     chan struct{}
	once     sync.Once
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    []net.Conn
}

func newSilentFalkor(t *testing.T) *silentFalkor {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &silentFalkor{listener: listener, seen: make(chan struct{})}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, conn)
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		s.mu.Lock()
		for _, conn := range s.conns {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

// serve answers the connection setup and stays silent on a graph query.
func (s *silentFalkor) serve(conn net.Conn) {
	reader := bufio.NewReader(conn)
	for {
		command, err := readRESPCommand(reader)
		if err != nil {
			return
		}
		name := strings.ToUpper(command[0])
		if strings.HasPrefix(name, "GRAPH.") {
			s.queries.Add(1)
			s.once.Do(func() { close(s.seen) })
			continue
		}
		reply := "+OK\r\n"
		if name == "HELLO" || name == "INFO" {
			reply = "-ERR unknown command\r\n"
		}
		if _, err := io.WriteString(conn, reply); err != nil {
			return
		}
	}
}

// TestAReadInFlightStopsAtItsContextDeadline (CHAOS-12511) is the grounding for
// the answer reuse recheck's sub-deadline, on the real sdkAPI and the real
// go-redis client over a real socket: a read whose reply never comes returns at
// its context deadline, not at the configured RequestTimeout, and it is not sent
// again. The write client is the control: it runs on the same socket, it does
// not honour a context deadline, and it holds to RequestTimeout. Without that
// control this test could not fail.
func TestAReadInFlightStopsAtItsContextDeadline(t *testing.T) {
	t.Parallel()
	const deadline = 150 * time.Millisecond
	const requestTimeout = 3 * time.Second
	for _, tc := range []struct {
		name     string
		readOnly bool
		min, max time.Duration
	}{
		{name: "the read client stops at the deadline", readOnly: true, min: deadline, max: requestTimeout / 2},
		{name: "control: the write client holds to RequestTimeout", readOnly: false, min: requestTimeout / 2, max: 3 * requestTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := newSilentFalkor(t)
			client, err := newSDKAPI(Config{Addr: server.listener.Addr().String(), GraphPrefix: "acr-cf-deadline", RequestTimeout: requestTimeout, MaxAttempts: 1, MaxResults: 25, PoolSize: 2, AllowInsecure: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), deadline)
			defer cancel()
			started := time.Now()
			_, err = client.query(ctx, "graph", "RETURN 1", nil, tc.readOnly)
			elapsed := time.Since(started)
			t.Logf("read_only=%t elapsed=%s queries_sent=%d err=%v", tc.readOnly, elapsed.Round(time.Millisecond), server.queries.Load(), err)
			if err == nil {
				t.Fatal("a query with no reply returned no error")
			}
			select {
			case <-server.seen:
			default:
				t.Fatal("the server never saw the query, so nothing was in flight")
			}
			if elapsed < tc.min || elapsed > tc.max {
				t.Fatalf("returned after %s, want between %s and %s", elapsed, tc.min, tc.max)
			}
			if tc.readOnly && server.queries.Load() != 1 {
				t.Fatalf("the read was sent %d times, want 1: a retry past the deadline would break the bound", server.queries.Load())
			}
		})
	}
}

// reuseGateStore offers one stored answer for reuse.
type reuseGateStore struct {
	contextfabric.InvestigationResultStore
	candidate contextfabric.InvestigationResult
}

func (s reuseGateStore) FindReusable(_ context.Context, principal storage.Principal, _ contextfabric.ReuseKey) (contextfabric.StoredInvestigationResult, bool, contextfabric.ReuseMissReason, error) {
	return contextfabric.StoredInvestigationResult{Result: s.candidate, GrantDigest: contextfabric.StoredResultGrantDigest(principal), SemanticStateRead: contextfabric.SemanticStateReadAbsent}, true, "", nil
}

// admittingAdapter is the real adapter with the stored-result gate open, so the
// reads under test are the recheck's.
type admittingAdapter struct{ *Adapter }

func (admittingAdapter) AuthorizeStoredSubjects(_ context.Context, _ storage.Principal, _ contextfabric.ResolvedGraphBinding, subjects []contextfabric.SubjectRef) ([]contextfabric.StoredSubjectOutcome, error) {
	out := make([]contextfabric.StoredSubjectOutcome, len(subjects))
	for index := range out {
		out[index] = contextfabric.StoredSubjectAdmitted
	}
	return out, nil
}

// blockingConn is a client that holds every read carrying a deadline until its
// context ends, and answers every other read. Only the recheck's reads carry a
// deadline here, so it records exactly what reached the client from the recheck.
func blockingConn(t *testing.T, seen *[]time.Time, mu *sync.Mutex) *fakeConn {
	t.Helper()
	return &fakeConn{queryFunc: func(ctx context.Context, _, _ string, _ map[string]interface{}, _ bool) ([]row, error) {
		deadline, bounded := ctx.Deadline()
		if !bounded {
			return nil, nil
		}
		mu.Lock()
		*seen = append(*seen, deadline)
		mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(5 * time.Second):
			t.Error("a read outlived its context deadline")
			return nil, errors.New("read was never bounded")
		}
	}}
}

// TestTheRecheckBudgetReachesTheGraphClient (CHAOS-12511) runs the whole chain:
// the real Engine.Investigate, the recheck, the real Adapter and its resolver,
// down to the client's query call. The read that the client holds carries the
// recheck's deadline, ends with it, and reads as the named miss.
func TestTheRecheckBudgetReachesTheGraphClient(t *testing.T) {
	t.Parallel()
	var seen []time.Time
	var mu sync.Mutex
	adapter := newFakeAdapter(t, blockingConn(t, &seen, &mu))
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_alpha", Label: "Alpha"}
	generated := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	candidate := contextfabric.InvestigationResult{
		SchemaVersion: contextfabric.InvestigationResultSchemaV1, ResultID: "result_reused_0001", RequestID: "request_original_01", GeneratedAt: generated,
		Status: contextfabric.InvestigationComplete, Question: "what is the status of alpha", DirectJudgment: "Alpha is on track.", CurrentState: "On track.",
		Interpretation:    contextfabric.InterpretedQuestion{Shape: contextfabric.ShapeOpen, RequestedJudgment: "status", TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent}},
		SubjectResolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{subject}},
		EvidenceRefIDs:    []string{}, ClaimedFacts: []contextfabric.ClaimedFact{}, Drivers: []contextfabric.DriverJudgment{}, Paths: []contextfabric.RelationshipPath{},
		DeterministicAnswer: "Alpha is on track.",
	}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	var modelCalls atomic.Int64
	engine, err := contextfabric.NewEngine(contextfabric.EngineDependencies{
		Interpreter: reuseDeadlineInterpreter{calls: &modelCalls},
		Graph:       admittingAdapter{adapter}, Facts: emptyFactReader{}, Synthesizer: countingSynthesizer{},
		Results: &routeStore{}, ReuseGate: reuseGateStore{candidate: candidate}, Telemetry: contextfabric.NewSlogEngineTelemetry(logger),
	}, contextfabric.EngineOptions{ServiceVersion: "acr-test", NewResultID: func() string { return "result_fresh_00001" }, ReuseRecheckBudget: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	_, _ = engine.Investigate(context.Background(), storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}, contextfabric.InvestigationRequest{
		SchemaVersion: contextfabric.InvestigationRequestSchemaV1, RequestID: "request_8300002", Question: "what is the status of alpha",
		TimeContext: contextfabric.TimeContext{Axis: contextfabric.TemporalCurrent},
		Options:     contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100, MaxSerializedBytes: 1 << 20, AllowClarification: true},
		Consumer:    contextfabric.ConsumerInfo{Name: "test", Version: "v1", Surface: "test"},
	})
	mu.Lock()
	reads := len(seen)
	mu.Unlock()
	if reads == 0 {
		t.Fatalf("no read reached the client with a deadline:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"outcome":"miss_recheck_deadline"`) {
		t.Fatalf("the reuse line does not name the deadline:\n%s", buf.String())
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("model calls = %d, want the fresh path to run once", modelCalls.Load())
	}
}

// reuseDeadlineInterpreter counts the fresh path's model call.
type reuseDeadlineInterpreter struct{ calls *atomic.Int64 }

func (i reuseDeadlineInterpreter) Interpret(context.Context, storage.Principal, contextfabric.InvestigationRequest) (contextfabric.InterpretedQuestion, contextfabric.QuestionFamilyOutcome, error) {
	i.calls.Add(1)
	return contextfabric.InterpretedQuestion{}, contextfabric.QuestionFamilyOutcome{}, errors.New("fresh investigation reached")
}

// TestAHeldReadEndsWithItsDeadlineOnEveryRecheckPath (CHAOS-12511) runs the three
// adapter calls of the recheck on a client that holds every read until its
// context ends: each returns at the deadline, with an error, so no call waits on
// a read past it.
func TestAHeldReadEndsWithItsDeadlineOnEveryRecheckPath(t *testing.T) {
	t.Parallel()
	principal := storage.Principal{OrgID: "org-1", RepositoryScopes: []string{"*"}}
	subject := contextfabric.SubjectRef{Kind: contextfabric.SubjectTeam, CanonicalID: "team_alpha", Label: "Alpha"}
	request := contextfabric.InvestigationRequest{Question: "what is the status of alpha", Options: contextfabric.InvestigationOptions{MaxSubjectCandidates: 10, MaxCohortMembers: 10, MaxRelationshipPaths: 50, MaxDrivers: 10, MaxEvidenceRefs: 100}}
	request.RequestedScope.SubjectHints = []contextfabric.SubjectHint{{Kind: subject.Kind, ID: subject.CanonicalID, Label: subject.Label, Source: string(hintsource.AnswerReuseAuthorizationRecheck)}}
	for name, call := range map[string]func(context.Context, *Adapter) error{
		"ResolveSubjects": func(ctx context.Context, a *Adapter) error {
			_, _, _, _, err := a.ResolveSubjects(ctx, principal, request, contextfabric.InterpretedQuestion{}, contextfabric.ResolvedGraphBinding{}, nil, nil, nil, "")
			return err
		},
		"DiscoverContext": func(ctx context.Context, a *Adapter) error {
			_, err := a.DiscoverContext(ctx, principal, contextfabric.GraphDiscoveryRequest{Request: request, Resolution: contextfabric.SubjectResolution{Candidates: []contextfabric.SubjectCandidate{}, Committed: []contextfabric.SubjectRef{subject}}})
			return err
		},
		"RemovedTeams": func(ctx context.Context, a *Adapter) error {
			_, err := a.RemovedTeams(ctx, principal, contextfabric.ResolvedGraphBinding{}, []contextfabric.SubjectRef{subject})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var seen []time.Time
			var mu sync.Mutex
			adapter := newFakeAdapter(t, blockingConn(t, &seen, &mu))
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			want, _ := ctx.Deadline()
			started := time.Now()
			err := call(ctx, adapter)
			elapsed := time.Since(started)
			t.Logf("%s returned after %s err=%v", name, elapsed.Round(time.Millisecond), err)
			mu.Lock()
			defer mu.Unlock()
			if len(seen) == 0 || !seen[0].Equal(want) {
				t.Fatalf("deadlines at the client = %v, want %v", seen, want)
			}
			if err == nil {
				t.Fatal("a read cut off by its deadline reported no error")
			}
			if elapsed > 2*time.Second {
				t.Fatalf("returned after %s, long past the 40ms deadline", elapsed)
			}
		})
	}
}
