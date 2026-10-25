package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	falkordb "github.com/FalkorDB/falkordb-go/v2"
	"github.com/redis/go-redis/v9"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
)

// slowProbeCypher is a write whose reply takes as long as n x 1000 rows take
// to count, then commits one marker node stamped with the server's own clock.
const slowProbeCypher = "UNWIND range(1, $n) AS i UNWIND range(1, 1000) AS j WITH count(j) AS c CREATE (:CutProbe {run: $run, c: c, at: timestamp()})"

func sendSlowProbe(ctx context.Context, client *falkordb.FalkorDB, key, run string, n int64) error {
	text := falkordb.BuildParamsHeader(map[string]interface{}{"n": n, "run": run}) + slowProbeCypher
	return client.Conn.Do(ctx, "GRAPH.QUERY", key, text, "--compact").Err()
}

// cutVerdict says what one deadline run measured.
type cutVerdict int

const (
	// cutNeverSent: the deadline had already expired when the command went on the wire (or it never did), so no mid-command cut was measured.
	cutNeverSent cutVerdict = iota
	// cutNotLanded: the command was sent before the deadline and the write never landed.
	cutNotLanded
	// cutCommittedBeforeDeadline: the write committed before the deadline expired, so nothing was cut.
	cutCommittedBeforeDeadline
	// cutMidCommand: sent before the deadline, committed after it: the deadline expired while the server ran the write.
	cutMidCommand
)

// classifyCut orders the three instants that make a cut: send < deadline < commit. The caller's return time is
// not one of them: under load a caller returns late, which says nothing about the server.
func classifyCut(sent, deadline time.Time, landing probeLanding) cutVerdict {
	switch {
	case sent.IsZero() || !sent.Before(deadline):
		return cutNeverSent
	case landing.count < 1:
		return cutNotLanded
	case landing.atMs <= deadline.UnixMilli():
		return cutCommittedBeforeDeadline
	}
	return cutMidCommand
}

type probeLanding struct {
	count int64
	atMs  int64
}

func readProbeLanding(t *testing.T, api *sdkAPI, key, run string) probeLanding {
	t.Helper()
	rows, err := api.query(context.Background(), key, "MATCH (p:CutProbe {run: $run}) RETURN count(p) AS n, max(p.at) AS at", map[string]interface{}{"run": run}, true)
	if err != nil {
		t.Fatalf("read probe landing for %s: %v", run, err)
	}
	if len(rows) != 1 {
		t.Fatalf("probe landing for %s returned %d rows, want 1", run, len(rows))
	}
	landing := probeLanding{}
	landing.count, _ = rows[0]["n"].(int64)
	landing.atMs, _ = rows[0]["at"].(int64)
	return landing
}

func waitForProbeLandings(t *testing.T, api *sdkAPI, key, run string, want int64, within time.Duration) probeLanding {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		landing := readProbeLanding(t, api, key, run)
		if landing.count >= want || time.Now().After(deadline) {
			return landing
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func liveSDKAPI(t *testing.T, adapter *Adapter) *sdkAPI {
	t.Helper()
	switch c := adapter.api.(type) {
	case *sdkAPI:
		return c
	case projectReachConn:
		if api, ok := c.conn.(*sdkAPI); ok {
			return api
		}
	}
	t.Fatalf("adapter conn %T does not wrap the real client", adapter.api)
	return nil
}

// replyGate holds back the server's reply, or the probe command itself, on the
// connections it wraps while armed, so a test decides when the reply arrives
// or when the server gets the command instead of racing a timer against how
// fast the server happens to run the probe. It is test-only: the production
// dialer is untouched.
type replyGate struct {
	mu       sync.Mutex
	armed    bool
	hold     bool
	holdSend bool
	release  chan struct{}
	writes   atomic.Int64
	// flushes tracks held probe commands not yet written to the server; flushErrors counts the ones that failed.
	flushes     sync.WaitGroup
	flushErrors atomic.Int64
	// dialsBeforeSend counts dials made while recording before the probe went on the wire.
	dialsBeforeSend atomic.Int64
	sentAt          atomic.Int64
	sent            chan struct{}
}

func newReplyGate() *replyGate {
	return &replyGate{sent: make(chan struct{}, 16)}
}

// arm starts holding replies back; every later probe write counts as a send.
func (g *replyGate) arm() {
	g.armWith(true, false)
}

// record stamps every probe send and lets replies through: the send time of a
// call whose reply is not held back.
func (g *replyGate) record() {
	g.armWith(false, false)
}

// holdSends stamps every probe send as the client writes it and keeps the
// command off the wire until open: the server gets it only after the release.
func (g *replyGate) holdSends() {
	g.armWith(false, true)
}

func (g *replyGate) armWith(hold, holdSend bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writes.Store(0)
	g.dialsBeforeSend.Store(0)
	g.sentAt.Store(0)
	g.hold = hold
	g.holdSend = holdSend
	for len(g.sent) > 0 {
		<-g.sent
	}
	g.release = make(chan struct{})
	g.armed = true
}

// open lets the held reply through and stops holding later ones.
func (g *replyGate) open() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.armed {
		close(g.release)
		g.armed = false
	}
}

func (g *replyGate) holding() (<-chan struct{}, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.release, g.armed && g.hold
}

func (g *replyGate) sendHold() (<-chan struct{}, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.release, g.armed && g.holdSend
}

func (g *replyGate) recording() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.armed
}

func (g *replyGate) stampSend(began int64) {
	g.sentAt.CompareAndSwap(0, began)
	g.writes.Add(1)
	select {
	case g.sent <- struct{}{}:
	default:
	}
}

func (g *replyGate) dialer(base func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if g.recording() && g.sentAt.Load() == 0 {
			g.dialsBeforeSend.Add(1)
		}
		conn, err := base(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &gatedConn{Conn: conn, gate: g}, nil
	}
}

type gatedConn struct {
	net.Conn
	gate          *replyGate
	deadline      atomic.Int64
	writeDeadline atomic.Int64

	mu              sync.Mutex
	held            int
	closeAfterFlush bool
}

func storeDeadline(at *atomic.Int64, t time.Time) {
	if t.IsZero() {
		at.Store(0)
	} else {
		at.Store(t.UnixNano())
	}
}

func (c *gatedConn) SetReadDeadline(t time.Time) error {
	storeDeadline(&c.deadline, t)
	return c.Conn.SetReadDeadline(t)
}

func (c *gatedConn) SetWriteDeadline(t time.Time) error {
	storeDeadline(&c.writeDeadline, t)
	return c.Conn.SetWriteDeadline(t)
}

func (c *gatedConn) SetDeadline(t time.Time) error {
	storeDeadline(&c.deadline, t)
	storeDeadline(&c.writeDeadline, t)
	return c.Conn.SetDeadline(t)
}

// Write counts and stamps only the probe's own command. A new connection's
// handshake (HELLO) is a write too, and stamping it would date a send of a
// probe that never went on the wire. The stamp is taken before the write, and
// kept only when the whole command was written. A held command is accepted as
// the socket would accept it (refused once its write deadline has passed) and
// reaches the server only at the gate's release.
func (c *gatedConn) Write(b []byte) (int, error) {
	began := time.Now().UnixNano()
	probe := c.gate.recording() && bytes.Contains(b, []byte("GRAPH.QUERY"))
	if release, held := c.gate.sendHold(); held && probe {
		if at := c.writeDeadline.Load(); at != 0 && began >= at {
			return 0, os.ErrDeadlineExceeded
		}
		c.holdUntil(release, append([]byte(nil), b...))
		c.gate.stampSend(began)
		return len(b), nil
	}
	n, err := c.Conn.Write(b)
	if err == nil && probe {
		c.gate.stampSend(began)
	}
	return n, err
}

// holdUntil writes b to the server once release closes, with no deadline: the
// caller's deadline has passed by then. A Close that came first takes effect
// after the last held command is written.
func (c *gatedConn) holdUntil(release <-chan struct{}, b []byte) {
	c.mu.Lock()
	c.held++
	c.mu.Unlock()
	c.gate.flushes.Add(1)
	go func() {
		defer c.gate.flushes.Done()
		<-release
		c.mu.Lock()
		defer c.mu.Unlock()
		if err := c.Conn.SetWriteDeadline(time.Time{}); err != nil {
			c.gate.flushErrors.Add(1)
		} else if _, err := c.Conn.Write(b); err != nil {
			c.gate.flushErrors.Add(1)
		}
		c.held--
		if c.held == 0 && c.closeAfterFlush {
			_ = c.Conn.Close()
		}
	}()
}

func (c *gatedConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held > 0 {
		c.closeAfterFlush = true
		return nil
	}
	return c.Conn.Close()
}

func (c *gatedConn) Read(b []byte) (int, error) {
	if release, armed := c.gate.holding(); armed {
		var expired <-chan time.Time
		if at := c.deadline.Load(); at != 0 {
			timer := time.NewTimer(time.Until(time.Unix(0, at)))
			defer timer.Stop()
			expired = timer.C
		}
		select {
		case <-release:
		case <-expired:
			return 0, os.ErrDeadlineExceeded
		}
	}
	return c.Conn.Read(b)
}

func gatedClient(t *testing.T, options redis.Options, gate *replyGate) *falkordb.FalkorDB {
	t.Helper()
	base := options.Dialer
	if base == nil {
		dialer := &net.Dialer{Timeout: options.DialTimeout}
		base = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		}
	}
	options.Dialer = gate.dialer(base)
	client := redis.NewClient(&options)
	t.Cleanup(func() { gate.open(); _ = client.Close() })
	return &falkordb.FalkorDB{Conn: client}
}

// waitPastMillisecond returns once the wall clock reads a later millisecond
// than every instant, so a server stamp taken after it is strictly later in
// milliseconds than each of them.
func waitPastMillisecond(instants ...time.Time) {
	var last int64
	for _, at := range instants {
		if ms := at.UnixMilli(); ms > last {
			last = ms
		}
	}
	for time.Now().UnixMilli() <= last {
		time.Sleep(100 * time.Microsecond)
	}
}

// TestLiveWriteCutOnARealFalkorDB measures, on one FalkorDB container, what a
// cut does to a write whose reply has not come back: a deadline, a cancel,
// and the write client's own read timeout. The gate, not the server's speed,
// decides where each cut falls.
func TestLiveWriteCutOnARealFalkorDB(t *testing.T) {
	t.Parallel()
	const cut = time.Second
	const readTimeout = 100 * time.Millisecond
	adapter, _ := newLiveFalkorAdapter(t, context.Background())
	api := liveSDKAPI(t, adapter)
	key := graphKey(adapter.config.GraphPrefix, "live-write-cut-"+time.Now().UTC().Format("20060102T150405.000000000"))
	t.Cleanup(func() { _ = api.deleteGraph(context.Background(), key) })
	if err := api.read.Conn.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("warm the context-enforcing client: %v", err)
	}

	// A deadline on a client that enforces the caller's ctx (the read
	// client's options) expires after the client wrote the command and before
	// any reply. The gate keeps the written command off the wire until the
	// call has returned past its deadline, so the server always receives it
	// after the cut: "sent" is the client's write, before the deadline, and
	// the commit comes after the deadline and after the return. That is the
	// product risk, whatever the server's speed: the caller gets a deadline
	// error, nothing resends the write (a resend is a second held command and
	// a second landing), and the write still lands exactly once.
	t.Run("deadline", func(t *testing.T) {
		const runs = 5
		const maxUnsent = 2
		gate := newReplyGate()
		client := gatedClient(t, *api.read.Conn.(*redis.Client).Options(), gate)
		measured := make([]string, 0, runs)
		var unsent []string
		errorsSeen, deadlineErrors, landed, single, committedAfterCut, returnedBeforeCommit := 0, 0, 0, 0, 0, 0
		for i := 0; len(measured) < runs; i++ {
			run := fmt.Sprintf("deadline-%d", i)
			// A cut drops the connection; warming here keeps the next dial and handshake out of the cut window.
			if err := client.Conn.Ping(context.Background()).Err(); err != nil {
				t.Fatalf("%s: warm the recording client: %v", run, err)
			}
			gate.holdSends()
			ctx, cancel := context.WithTimeout(context.Background(), cut)
			deadline, _ := ctx.Deadline()
			done := make(chan error, 1)
			go func() { done <- sendSlowProbe(ctx, client, key, run, 1) }()
			var err error
			select {
			case err = <-done:
			case <-time.After(cut + stallBound):
				cancel()
				gate.open()
				t.Fatalf("%s: the call did not return within %v after its %v deadline although its command got no reply: the deadline did not bound the call", run, stallBound, cut)
			}
			returned := time.Now()
			cancel()
			sends, dials := gate.writes.Load(), gate.dialsBeforeSend.Load()
			sent := time.Time{}
			if ns := gate.sentAt.Load(); ns != 0 {
				sent = time.Unix(0, ns)
			}
			waitPastMillisecond(deadline, returned)
			gate.open()
			gate.flushes.Wait()
			if failed := gate.flushErrors.Load(); failed != 0 {
				t.Fatalf("%s: %d held command(s) could not be written to the server after the release", run, failed)
			}
			if dials != 0 {
				t.Fatalf("%s: the call dialed %d connection(s) before the probe went on the wire: a handshake shared the %v cut window, so the run would not measure a cut of the write", run, dials, cut)
			}
			if classifyCut(sent, deadline, probeLanding{}) == cutNeverSent {
				if sends != 0 {
					t.Fatalf("%s: the probe write began at %s, not before the %s deadline, and still succeeded: the deadline did not bound the write", run, sent.Format(time.RFC3339Nano), deadline.Format(time.RFC3339Nano))
				}
				unsent = append(unsent, run)
				if len(unsent) > maxUnsent {
					t.Fatalf("%d calls never put the probe on the wire before the %v deadline (%v), more than %d: the cut is not being measured", len(unsent), cut, unsent, maxUnsent)
				}
				continue
			}
			measured = append(measured, run)
			if sends != 1 {
				t.Errorf("%s: the probe went on the wire %d times in one call, want 1 (a deadline cut must not resend)", run, sends)
			}
			if err != nil {
				errorsSeen++
			}
			if errors.Is(err, context.DeadlineExceeded) {
				deadlineErrors++
			}
			landing := waitForProbeLandings(t, api, key, run, 1, 30*time.Second)
			if landing.count >= 1 {
				landed++
			}
			if landing.count == 1 {
				single++
			}
			switch classifyCut(sent, deadline, landing) {
			case cutMidCommand:
				committedAfterCut++
			case cutNotLanded:
				t.Errorf("%s: the probe went on the wire before the deadline and did not land within 30s", run)
			}
			if landing.atMs > returned.UnixMilli() {
				returnedBeforeCommit++
			}
		}
		for _, run := range measured {
			if got := readProbeLanding(t, api, key, run).count; got != 1 {
				t.Errorf("%s landed %d times, want exactly 1 (a deadline cut must not resend)", run, got)
			}
		}
		for _, run := range unsent {
			if got := readProbeLanding(t, api, key, run).count; got != 0 {
				t.Errorf("%s never put the probe on the wire, yet it landed %d times", run, got)
			}
		}
		t.Logf("write ctx deadline cut: runs=%d cut=%v errors=%d deadline_errors=%d landed=%d single=%d committed_after_deadline=%d returned_before_commit=%d unsent_retries=%d",
			runs, cut, errorsSeen, deadlineErrors, landed, single, committedAfterCut, returnedBeforeCommit, len(unsent))
		if errorsSeen != runs || deadlineErrors != runs || landed != runs || single != runs || committedAfterCut != runs || returnedBeforeCommit != runs {
			t.Fatalf("want every cut run to return a deadline error AND land exactly once, sent before the deadline and committed after it and after the return: errors=%d deadline_errors=%d landed=%d single=%d committed_after_deadline=%d returned_before_commit=%d of %d",
				errorsSeen, deadlineErrors, landed, single, committedAfterCut, returnedBeforeCommit, runs)
		}
	})

	// A cancel (no deadline) after the command is sent does not cut it:
	// go-redis honours a cancel only before the send. The call waits for the
	// reply and the write lands once.
	t.Run("cancel", func(t *testing.T) {
		const runs = 3
		gate := newReplyGate()
		client := gatedClient(t, *api.read.Conn.(*redis.Client).Options(), gate)
		if err := client.Conn.Ping(context.Background()).Err(); err != nil {
			t.Fatalf("warm the gated client: %v", err)
		}
		stillBlocked, nilErrors, single := 0, 0, 0
		for i := 0; i < runs; i++ {
			run := fmt.Sprintf("cancel-%d", i)
			ctx, cancel := context.WithCancel(context.Background())
			gate.arm()
			done := make(chan error, 1)
			go func() { done <- sendSlowProbe(ctx, client, key, run, 1) }()
			select {
			case <-gate.sent:
			case err := <-done:
				cancel()
				t.Fatalf("run %d: call returned before the command was sent: %v", i, err)
			case <-time.After(30 * time.Second):
				cancel()
				t.Fatalf("run %d: the command was never sent", i)
			}
			cancel()
			select {
			case err := <-done:
				t.Errorf("run %d: a cancel after the send cut the call: error=%v", i, err)
				gate.open()
			case <-time.After(200 * time.Millisecond):
				stillBlocked++
				gate.open()
				err := <-done
				if err == nil {
					nilErrors++
				}
			}
			if waitForProbeLandings(t, api, key, run, 1, 30*time.Second).count == 1 {
				single++
			}
		}
		t.Logf("write ctx cancel mid-command: runs=%d probe_rows=1 still_blocked_after_cancel=%d nil_errors=%d landed_once=%d", runs, stillBlocked, nilErrors, single)
		if stillBlocked != runs || nilErrors != runs || single != runs {
			t.Fatalf("a cancel after the send must not cut the command: still_blocked_after_cancel=%d nil_errors=%d landed_once=%d of %d", stillBlocked, nilErrors, single, runs)
		}
	})

	// The write path as it is: the write client ignores the caller's ctx and
	// does not resend (MaxRetries -1). The write client's own options are used
	// with only the read timeout shortened. The gate holds the server's reply
	// past that timeout, so the call returns an error after exactly one send,
	// and the write still lands once.
	t.Run("resend", func(t *testing.T) {
		options := *api.db.Conn.(*redis.Client).Options()
		if options.ContextTimeoutEnabled {
			t.Fatal("the write client enforces the caller's ctx; this test pins the client the write path uses")
		}
		if options.MaxRetries != 0 {
			t.Fatalf("write client MaxRetries = %d, want 0 (configured -1: no driver resend)", options.MaxRetries)
		}
		options.ReadTimeout = readTimeout
		options.MaxRetries = -1
		gate := newReplyGate()
		writeClient := gatedClient(t, options, gate)
		if err := writeClient.Conn.Ping(context.Background()).Err(); err != nil {
			t.Fatalf("warm the gated write client: %v", err)
		}
		gate.arm()
		sendErr := sendSlowProbe(context.Background(), writeClient, key, "resend", 1)
		sends := gate.writes.Load()
		gate.open()
		landing := waitForProbeLandings(t, api, key, "resend", 1, 30*time.Second)
		time.Sleep(300 * time.Millisecond)
		settled := readProbeLanding(t, api, key, "resend").count
		t.Logf("write client read-timeout resend: read_timeout=%v max_retries=%d error=%v sends=%d landed=%d settled=%d",
			readTimeout, options.MaxRetries, sendErr != nil, sends, landing.count, settled)
		if sendErr == nil {
			t.Fatalf("a write whose reply is held past the %v read timeout returned no error", readTimeout)
		}
		if sends != 1 {
			t.Fatalf("the write client sent the command %d times, want 1 (the write client does not resend)", sends)
		}
		if settled != 1 {
			t.Fatalf("one write call slower than the read timeout landed %d times, want 1", settled)
		}
	})
}

type graphSnapshot struct {
	elements   []string
	generation int64
}

var watermarkVolatileProperties = map[string]bool{"generation": true, "projected_at": true, "projected_at_ns": true}

func snapshotGraph(t *testing.T, api *sdkAPI, key string) graphSnapshot {
	t.Helper()
	ctx := context.Background()
	snapshot := graphSnapshot{}
	nodes, err := api.query(ctx, key, "MATCH (n) RETURN n", nil, true)
	if err != nil {
		t.Fatalf("snapshot nodes: %v", err)
	}
	for _, r := range nodes {
		n, ok := r["n"].(*node)
		if !ok {
			t.Fatalf("snapshot node row %v", r)
		}
		labels := append([]string(nil), n.Labels...)
		sort.Strings(labels)
		properties := map[string]interface{}{}
		for k, v := range n.Properties {
			if containsString(labels, labelWatermark) && watermarkVolatileProperties[k] {
				if k == "generation" {
					snapshot.generation, _ = v.(int64)
				}
				continue
			}
			properties[k] = v
		}
		snapshot.elements = append(snapshot.elements, fmt.Sprintf("node %v %v", labels, properties))
	}
	edges, err := api.query(ctx, key, "MATCH (a)-[r]->(b) RETURN a.canonical_id AS a, b.canonical_id AS b, r", nil, true)
	if err != nil {
		t.Fatalf("snapshot edges: %v", err)
	}
	for _, r := range edges {
		e, ok := r["r"].(*edge)
		if !ok {
			t.Fatalf("snapshot edge row %v", r)
		}
		snapshot.elements = append(snapshot.elements, fmt.Sprintf("edge %v-[%s %v]->%v", r["a"], e.Relation, e.Properties, r["b"]))
	}
	sort.Strings(snapshot.elements)
	return snapshot
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func replayBatches(orgID string) (seed, replay contextfabric.ProjectionBatch) {
	observed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	later := observed.Add(time.Hour)
	scope := contextfabric.AuthorizationScope{RepositorySlugs: []string{"acme/replay"}}
	project := contextfabric.SubjectRef{Kind: contextfabric.SubjectProject, CanonicalID: "project_replay", Label: "Replay project"}
	kept := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_replay_kept", Label: "Kept work"}
	gone := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_replay_gone", Label: "Deleted work"}
	other := contextfabric.SubjectRef{Kind: contextfabric.SubjectWorkItem, CanonicalID: "work_replay_other", Label: "Other work"}
	entity := func(subject contextfabric.SubjectRef, at time.Time) contextfabric.EntityProjection {
		return contextfabric.EntityProjection{Subject: subject, Aliases: []string{subject.Label + " alias"}, Authorization: scope, EvidenceRefIDs: []string{"evidence_" + subject.CanonicalID}, ObservedAt: at, SourceVersion: "v1"}
	}
	relationship := func(id string, from, to contextfabric.SubjectRef, at time.Time) contextfabric.RelationshipProjection {
		return contextfabric.RelationshipProjection{
			RelationshipID: id, Type: "BLOCKS", From: from, To: to,
			Derivation: contextfabric.DerivationCanonicalStructured, EpistemicStatus: contextfabric.EpistemicObserved,
			Authorization: scope, EvidenceRefIDs: []string{"evidence_" + id}, ObservedAt: at, SourceVersion: "v1",
		}
	}
	seed = contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_replay_00000001", OrgID: orgID, Source: "live-replay",
		SourceVersion: "v1", Cursor: "", NextCursor: "cursor-1", GeneratedAt: observed,
		Entities:      []contextfabric.EntityProjection{entity(project, observed), entity(kept, observed), entity(gone, observed), entity(other, observed)},
		Relationships: []contextfabric.RelationshipProjection{relationship("relationship_replay_1", project, kept, observed), relationship("relationship_replay_2", kept, other, observed)},
		Contents:      []contextfabric.ContentProjection{}, Episodes: []contextfabric.EpisodeProjection{}, Tombstones: []contextfabric.ProjectionTombstone{},
	}
	renamed := kept
	renamed.Label = "Kept work renamed"
	replay = contextfabric.ProjectionBatch{
		SchemaVersion: contextfabric.ProjectionBatchSchemaV1, BatchID: "batch_replay_00000002", OrgID: orgID, Source: "live-replay",
		SourceVersion: "v1", Cursor: "cursor-1", NextCursor: "cursor-2", GeneratedAt: later,
		Entities:      []contextfabric.EntityProjection{entity(renamed, later)},
		Relationships: []contextfabric.RelationshipProjection{relationship("relationship_replay_3", project, other, later)},
		Contents: []contextfabric.ContentProjection{{
			ContentID: "content_replay_00000001", Subject: kept, Title: "Replay note", Body: "body", ContentDigest: "digest_replay_00000001",
			Authorization: scope, EvidenceRefIDs: []string{"evidence_content_replay"}, ObservedAt: later, SourceVersion: "v1", Untrusted: true,
		}},
		Episodes: []contextfabric.EpisodeProjection{{
			EpisodeID: "episode_replay_00000001", Subject: kept, Goal: "Replay", Outcome: "resolved", Summary: "replayed",
			Authorization: scope, EvidenceRefIDs: []string{"evidence_episode_replay"}, StartedAt: later, EndedAt: later.Add(time.Minute), SourceVersion: "v1",
		}},
		Tombstones: []contextfabric.ProjectionTombstone{
			{Kind: string(contextfabric.SubjectWorkItem), CanonicalID: gone.CanonicalID, Reason: "deleted", EffectiveAt: later, SourceVersion: "v1"},
			{Kind: "relationship", CanonicalID: "relationship_replay_2", Reason: "deleted", EffectiveAt: later, SourceVersion: "v1"},
		},
	}
	return seed, replay
}

// TestLiveProjectionBatchReplayLandsTheSameGraph applies a batch, then the
// same batch again: the state a resent statement or a replayed tick leaves.
// Every node and edge is identical after the second landing; only the
// watermark's generation counter moves, by one per landing. Bootstrap and
// purge also run twice without an error.
func TestLiveProjectionBatchReplayLandsTheSameGraph(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	adapter, _ := newLiveFalkorAdapter(t, ctx)
	api := liveSDKAPI(t, adapter)
	orgID := "live-replay-" + time.Now().UTC().Format("20060102T150405.000000000")
	t.Cleanup(func() { _ = adapter.PurgeOrganization(context.Background(), orgID) })
	key := graphKey(adapter.config.GraphPrefix, orgID)
	seed, replay := replayBatches(orgID)

	if _, err := adapter.ApplyProjectionBatch(ctx, seed); err != nil {
		t.Fatalf("seed batch: %v", err)
	}
	seeded := snapshotGraph(t, api, key)
	if _, err := adapter.ApplyProjectionBatch(ctx, replay); err != nil {
		t.Fatalf("replay batch, first landing: %v", err)
	}
	first := snapshotGraph(t, api, key)
	if _, err := adapter.ApplyProjectionBatch(ctx, replay); err != nil {
		t.Fatalf("replay batch, second landing: %v", err)
	}
	second := snapshotGraph(t, api, key)

	if fmt.Sprint(seeded.elements) == fmt.Sprint(first.elements) {
		t.Fatal("the replay batch changed nothing; the test would compare two unchanged graphs")
	}
	for _, want := range []string{"work_replay_gone", "relationship_replay_2"} {
		for _, element := range first.elements {
			if strings.Contains(element, want) {
				t.Fatalf("tombstoned %s still present after the first landing: %s", want, element)
			}
		}
	}
	if fmt.Sprint(first.elements) != fmt.Sprint(second.elements) {
		t.Fatalf("a second landing of the same batch changed the graph:\nfirst  %v\nsecond %v", first.elements, second.elements)
	}
	if seeded.generation != 1 || first.generation != 2 || second.generation != 3 {
		t.Fatalf("watermark generation = %d, %d, %d; want 1, 2, 3 (one bump per landing)", seeded.generation, first.generation, second.generation)
	}

	if err := adapter.bootstrapSchema(ctx, key); err != nil {
		t.Fatalf("second bootstrap of an existing graph: %v", err)
	}
	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("first purge: %v", err)
	}
	if err := adapter.PurgeOrganization(ctx, orgID); err != nil {
		t.Fatalf("second purge: %v", err)
	}
	if err := api.deleteGraph(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GRAPH.DELETE of a deleted graph = %v, want ErrNotFound (which every delete caller treats as success)", err)
	}
	t.Logf("projection replay: elements=%d identical_after_second_landing=true generation=%d,%d,%d bootstrap_twice=ok purge_twice=ok",
		len(second.elements), seeded.generation, first.generation, second.generation)
}
