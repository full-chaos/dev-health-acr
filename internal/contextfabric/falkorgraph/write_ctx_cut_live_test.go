package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
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

// calibrateSlowProbe sizes the probe so one uncut run takes at least
// minimum. A probe faster than that cannot show a cut mid-command, so the
// test fails instead of passing on a write that finished before the cut.
func calibrateSlowProbe(t *testing.T, api *sdkAPI, key string, minimum time.Duration) (int64, time.Duration) {
	t.Helper()
	n := int64(200)
	for attempt := 0; attempt < 12; attempt++ {
		began := time.Now()
		if err := sendSlowProbe(context.Background(), api.db, key, fmt.Sprintf("calibrate-%d", attempt), n); err != nil {
			t.Fatalf("calibration probe n=%d: %v", n, err)
		}
		spent := time.Since(began)
		if spent >= minimum {
			t.Logf("slow probe calibrated: n=%d took %v (minimum %v)", n, spent, minimum)
			return n, spent
		}
		scale := int64(float64(minimum)/float64(spent+time.Millisecond)*1.2) + 1
		if scale < 2 {
			scale = 2
		}
		n *= scale
	}
	t.Fatalf("slow probe never reached %v; the cut tests cannot prove a mid-command cut on this server", minimum)
	return 0, 0
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

// TestLiveWriteCutOnARealFalkorDB measures, on one FalkorDB container, what a
// cut does to a write that FalkorDB is still executing. The probe write is
// sized so one uncut run takes at least probeMinimum (3x the longest cut),
// and the test fails if it cannot get there.
func TestLiveWriteCutOnARealFalkorDB(t *testing.T) {
	t.Parallel()
	const cut = 50 * time.Millisecond
	const readTimeout = 100 * time.Millisecond
	const probeMinimum = 3 * readTimeout
	adapter, _ := newLiveFalkorAdapter(t, context.Background())
	api := liveSDKAPI(t, adapter)
	key := graphKey(adapter.config.GraphPrefix, "live-write-cut-"+time.Now().UTC().Format("20060102T150405.000000000"))
	t.Cleanup(func() { _ = api.deleteGraph(context.Background(), key) })
	if err := api.read.Conn.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("warm the context-enforcing client: %v", err)
	}
	n, probe := calibrateSlowProbe(t, api, key, probeMinimum)

	// A deadline on a client that enforces the caller's ctx (the read
	// client's options) expires while FalkorDB still executes the write.
	// The caller gets an error every time, and the write commits on the
	// server every time, after the call returned: a deadline turns a
	// completing write into an error plus a landed write.
	t.Run("deadline", func(t *testing.T) {
		const runs = 10
		errorsSeen, deadlineErrors, landed, single, committedAfterCut := 0, 0, 0, 0, 0
		for i := 0; i < runs; i++ {
			run := fmt.Sprintf("deadline-%d", i)
			ctx, cancel := context.WithTimeout(context.Background(), cut)
			err := sendSlowProbe(ctx, api.read, key, run, n)
			returnedMs := time.Now().UnixMilli()
			cancel()
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
			if landing.atMs > returnedMs {
				committedAfterCut++
			}
		}
		time.Sleep(probe + 200*time.Millisecond)
		for i := 0; i < runs; i++ {
			if got := readProbeLanding(t, api, key, fmt.Sprintf("deadline-%d", i)).count; got != 1 {
				t.Errorf("run %d landed %d times after settling, want exactly 1 (a deadline cut must not resend)", i, got)
			}
		}
		t.Logf("write ctx deadline cut: runs=%d cut=%v probe=%v errors=%d deadline_errors=%d landed=%d single=%d committed_after_cut=%d",
			runs, cut, probe, errorsSeen, deadlineErrors, landed, single, committedAfterCut)
		if errorsSeen != runs || deadlineErrors != runs || landed != runs || single != runs || committedAfterCut != runs {
			t.Fatalf("want every cut run to return a deadline error AND land exactly once, committed after the caller returned: errors=%d deadline_errors=%d landed=%d single=%d committed_after_cut=%d of %d",
				errorsSeen, deadlineErrors, landed, single, committedAfterCut, runs)
		}
	})

	// A cancel (no deadline) after the command is sent does not cut it:
	// go-redis honours a cancel only before the send. The call waits for the
	// reply and the write lands once.
	t.Run("cancel", func(t *testing.T) {
		const runs = 3
		waited, nilErrors, single := 0, 0, 0
		for i := 0; i < runs; i++ {
			run := fmt.Sprintf("cancel-%d", i)
			ctx, cancel := context.WithCancel(context.Background())
			timer := time.AfterFunc(cut, cancel)
			began := time.Now()
			err := sendSlowProbe(ctx, api.read, key, run, n)
			spent := time.Since(began)
			timer.Stop()
			cancel()
			if err == nil {
				nilErrors++
			}
			if spent >= 3*cut {
				waited++
			}
			if waitForProbeLandings(t, api, key, run, 1, 30*time.Second).count == 1 {
				single++
			}
		}
		t.Logf("write ctx cancel mid-command: runs=%d cut=%v probe=%v nil_errors=%d waited_past_cut=%d landed_once=%d", runs, cut, probe, nilErrors, waited, single)
		if nilErrors != runs || waited != runs || single != runs {
			t.Fatalf("a cancel after the send must not cut the command: nil_errors=%d waited_past_cut=%d landed_once=%d of %d", nilErrors, waited, single, runs)
		}
	})

	// The write path as it is: the write client ignores the caller's ctx, but
	// go-redis resends a command whose reply missed the read timeout
	// (MaxRetries, default 3). The write client's own options are used with
	// only the read timeout shortened, so one write slower than it returns an
	// error and lands MaxRetries+1 times.
	t.Run("resend", func(t *testing.T) {
		options := *api.db.Conn.(*redis.Client).Options()
		if options.ContextTimeoutEnabled {
			t.Fatal("the write client enforces the caller's ctx; this test pins the client the write path uses")
		}
		if options.MaxRetries != 3 {
			t.Fatalf("write client MaxRetries = %d, want the driver default 3 this test measures", options.MaxRetries)
		}
		options.ReadTimeout = readTimeout
		client := redis.NewClient(&options)
		t.Cleanup(func() { _ = client.Close() })
		writeClient := &falkordb.FalkorDB{Conn: client}
		sends := int64(options.MaxRetries + 1)
		began := time.Now()
		sendErr := sendSlowProbe(context.Background(), writeClient, key, "resend", n)
		spent := time.Since(began)
		landing := waitForProbeLandings(t, api, key, "resend", sends, time.Duration(sends)*probe*3+30*time.Second)
		time.Sleep(probe + 200*time.Millisecond)
		settled := readProbeLanding(t, api, key, "resend").count
		t.Logf("write client read-timeout resend: read_timeout=%v probe=%v max_retries=%d error=%v returned_after=%v landed=%d settled=%d",
			readTimeout, probe, options.MaxRetries, sendErr != nil, spent, landing.count, settled)
		if sendErr == nil {
			t.Fatalf("a write slower than the %v read timeout returned no error", readTimeout)
		}
		if settled != sends {
			t.Fatalf("one write call slower than the read timeout landed %d times, want MaxRetries+1 = %d (the driver resends a timed-out write)", settled, sends)
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
