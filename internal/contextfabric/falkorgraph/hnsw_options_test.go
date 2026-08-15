package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// extractFetchK pulls the raw k FalkorDB is asked for out of a
// db.idx.vector.queryNodes call, e.g. "...queryNodes('Subject', 'embedding',
// 41, vecf32($vec))..." -> 41.
func extractFetchK(t *testing.T, cypher string) int {
	t.Helper()
	const marker = "'embedding', "
	idx := strings.Index(cypher, marker)
	if idx < 0 {
		t.Fatalf("cypher has no queryNodes call: %q", cypher)
	}
	rest := cypher[idx+len(marker):]
	end := strings.Index(rest, ",")
	if end < 0 {
		t.Fatalf("cypher's queryNodes call has no k argument: %q", cypher)
	}
	var k int
	if _, err := fmt.Sscanf(rest[:end], "%d", &k); err != nil {
		t.Fatalf("parse k from %q: %v", rest[:end], err)
	}
	return k
}

// Production's only caller (createVectorIndex, via ensureVectorIndex) must
// keep sending EXACTLY the OPTIONS clause it always sent -- CHAOS-3832 added
// the M/efConstruction/efRuntime plumbing beside it, not underneath it.
func TestCreateVectorIndexZeroOptionsCypherUnchanged(t *testing.T) {
	var gotCypher string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotCypher = cypher
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.createVectorIndex(context.Background(), "k", 768); err != nil {
		t.Fatalf("createVectorIndex: %v", err)
	}
	want := "CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding) OPTIONS {dimension:768, similarityFunction:'cosine'}"
	if gotCypher != want {
		t.Fatalf("cypher = %q, want %q", gotCypher, want)
	}
}

// A non-zero HNSW field must be appended to the OPTIONS clause; a zero field
// must be OMITTED (not sent as a literal 0) so the server's own default
// applies rather than this code silently pinning one.
func TestCreateVectorIndexWithOptionsAppendsOnlyNonZeroFields(t *testing.T) {
	cases := []struct {
		name string
		opts hnswIndexOptions
		want string
	}{
		{"all set", hnswIndexOptions{M: 16, EfConstruction: 512, EfRuntime: 100},
			"CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding) OPTIONS {dimension:3072, similarityFunction:'cosine', M:16, efConstruction:512, efRuntime:100}"},
		{"efRuntime only", hnswIndexOptions{EfRuntime: 50},
			"CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding) OPTIONS {dimension:3072, similarityFunction:'cosine', efRuntime:50}"},
		{"efConstruction only", hnswIndexOptions{EfConstruction: 400},
			"CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding) OPTIONS {dimension:3072, similarityFunction:'cosine', efConstruction:400}"},
		{"zero value omits everything", hnswIndexOptions{},
			"CREATE VECTOR INDEX FOR (n:Subject) ON (n.embedding) OPTIONS {dimension:3072, similarityFunction:'cosine'}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var gotCypher string
			fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
				gotCypher = cypher
				return nil, nil
			}}
			adapter := newFakeAdapter(t, fake)
			if err := adapter.createVectorIndexWithOptions(context.Background(), "k", 3072, c.opts); err != nil {
				t.Fatalf("createVectorIndexWithOptions: %v", err)
			}
			if gotCypher != c.want {
				t.Fatalf("cypher = %q, want %q", gotCypher, c.want)
			}
		})
	}
}

// An "already indexed" rejection is success, exactly like createVectorIndex.
// fakeConn is a raw conn double (it stands in for sdkAPI, which is what
// normally runs a raw server message through classifyFalkorError) so the
// simulated failure here is the ALREADY-CLASSIFIED sentinel a real server
// response would have become by the time it reached this code -- mirroring
// how every other classification test in this package (see identity_test.go
// / bootstrap tests) exercises the classified error, not the raw string.
func TestCreateVectorIndexWithOptionsToleratesAlreadyExists(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, fmt.Errorf("query context graph: %w", errAlreadyExists)
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.createVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{EfRuntime: 100}); err != nil {
		t.Fatalf("createVectorIndexWithOptions must tolerate already-exists, got %v", err)
	}
}

// The classification link between a real server message and errIndexNotFound
// -- proves dropVectorIndex's tolerance (tested via the already-classified
// fake above) actually fires against the LIVE-VERIFIED message text
// (CHAOS-3832 §7 D3 probe: "Unable to drop index on :Subject(embedding): no
// such index"), not just against a sentinel this test file made up.
func TestClassifyFalkorErrorRecognizesNoSuchIndex(t *testing.T) {
	err := classifyFalkorError("drop vector index",
		errors.New("Unable to drop index on :Subject(embedding): no such index"))
	if !errors.Is(err, errIndexNotFound) {
		t.Fatalf("classifyFalkorError() = %v, want errIndexNotFound", err)
	}
}

func TestDropVectorIndexCypher(t *testing.T) {
	var gotCypher string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotCypher = cypher
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.dropVectorIndex(context.Background(), "k"); err != nil {
		t.Fatalf("dropVectorIndex: %v", err)
	}
	want := "DROP VECTOR INDEX FOR (n:Subject) ON (n.embedding)"
	if gotCypher != want {
		t.Fatalf("cypher = %q, want %q", gotCypher, want)
	}
}

// Dropping an already-absent index is treated as success -- the same
// idempotent posture createVectorIndex takes in the opposite direction --
// so a sweep can call dropVectorIndex unconditionally before recreating.
func TestDropVectorIndexTreatsNoSuchIndexAsSuccess(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, fmt.Errorf("query context graph: %w", errIndexNotFound)
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.dropVectorIndex(context.Background(), "k"); err != nil {
		t.Fatalf("dropVectorIndex must tolerate an absent index, got %v", err)
	}
}

// A genuinely unexpected drop failure must NOT be swallowed.
func TestDropVectorIndexPropagatesUnexpectedErrors(t *testing.T) {
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, errors.New("ERR some other server failure")
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.dropVectorIndex(context.Background(), "k"); err == nil {
		t.Fatal("an unclassified drop failure must propagate, not be swallowed")
	}
}

// recreateVectorIndexWithOptions: drop, create, then poll to OPERATIONAL --
// each step in that order.
func TestRecreateVectorIndexWithOptionsRunsDropCreatePoll(t *testing.T) {
	var calls []string
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			switch {
			case strings.HasPrefix(cypher, "DROP VECTOR INDEX"):
				calls = append(calls, "drop")
			case strings.HasPrefix(cypher, "CREATE VECTOR INDEX"):
				calls = append(calls, "create")
				if !strings.Contains(cypher, "efRuntime:200") {
					t.Fatalf("create cypher missing efRuntime:200: %q", cypher)
				}
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			// Called TWICE by recreateVectorIndexWithOptions: once up front
			// to capture the pre-drop options for a possible restore (Luna
			// round-1 finding 2b), once by pollVectorIndexOperational at the
			// end. Both report the same OPERATIONAL row here since this test
			// is not exercising the restore path (see the dedicated restore
			// tests below).
			calls = append(calls, "poll")
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(768)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	err := adapter.recreateVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{EfRuntime: 200})
	if err != nil {
		t.Fatalf("recreateVectorIndexWithOptions: %v", err)
	}
	want := []string{"poll", "drop", "create", "poll"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("calls = %v, want %v", calls, want)
		}
	}
}

// A drop failure must abort before ever attempting to create -- a stale
// index from a failed drop must never be silently left in place while a
// second one is created over it.
func TestRecreateVectorIndexWithOptionsStopsIfDropFails(t *testing.T) {
	var created bool
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		if strings.HasPrefix(cypher, "DROP VECTOR INDEX") {
			return nil, errors.New("ERR some other server failure")
		}
		if strings.HasPrefix(cypher, "CREATE VECTOR INDEX") {
			created = true
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)
	if err := adapter.recreateVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{}); err == nil {
		t.Fatal("a drop failure must propagate")
	}
	if created {
		t.Fatal("create must never run after a failed drop")
	}
}

// Luna round-1 finding 2b: when the CREATE half of a recreate cycle fails
// after a successful DROP, the ORIGINAL pre-drop options must be restored
// rather than leaving the target with no vector index at all.
func TestRecreateVectorIndexWithOptionsRestoresOriginalOptionsIfNewCreateFails(t *testing.T) {
	var creates []string
	firstCreateFailed := false
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.HasPrefix(cypher, "CREATE VECTOR INDEX") {
				creates = append(creates, cypher)
				if strings.Contains(cypher, "efRuntime:999") {
					firstCreateFailed = true
					return nil, errors.New("ERR simulated create failure")
				}
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			// Before the first create attempts anything, the pre-drop read
			// reports the ORIGINAL configuration this test expects restored.
			if !firstCreateFailed {
				return []indexStatus{{
					Label: labelSubject, Status: "OPERATIONAL",
					Types: map[string][]string{propEmbedding: {"VECTOR"}},
					Options: map[string]interface{}{propEmbedding: map[string]interface{}{
						"dimension": int64(768), "M": int64(16), "efConstruction": int64(200), "efRuntime": int64(10),
					}},
				}}, nil
			}
			// After the failed create + restore attempt, report OPERATIONAL
			// so pollVectorIndexOperational's best-effort wait succeeds.
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types:   map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{"dimension": int64(768)}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	err := adapter.recreateVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{EfRuntime: 999})
	if err == nil {
		t.Fatal("expected an error reporting the original create failure, even though restore succeeded")
	}
	if !strings.Contains(err.Error(), "restored the original options") || !strings.Contains(err.Error(), "successfully") {
		t.Fatalf("error must report the successful restore, got: %v", err)
	}
	if len(creates) != 2 {
		t.Fatalf("expected exactly 2 CREATE attempts (new options, then restore), got %d: %v", len(creates), creates)
	}
	if !strings.Contains(creates[0], "efRuntime:999") {
		t.Fatalf("first create should attempt the NEW options, got %q", creates[0])
	}
	if !strings.Contains(creates[1], "M:16") || !strings.Contains(creates[1], "efConstruction:200") || !strings.Contains(creates[1], "efRuntime:10") {
		t.Fatalf("second create should restore the ORIGINAL options, got %q", creates[1])
	}
}

// If BOTH the new create and the restore attempt fail, the index is
// genuinely absent -- that must be reported loudly, not swallowed.
func TestRecreateVectorIndexWithOptionsReportsLoudlyWhenRestoreAlsoFails(t *testing.T) {
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.HasPrefix(cypher, "CREATE VECTOR INDEX") {
				return nil, errors.New("ERR simulated create failure")
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types: map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{
					"dimension": int64(768), "efRuntime": int64(10),
				}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	err := adapter.recreateVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{EfRuntime: 999})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ABSENT") || !strings.Contains(err.Error(), "manual intervention required") {
		t.Fatalf("a double failure must be reported loudly with an explicit ABSENT/manual-intervention message, got: %v", err)
	}
}

// Luna round-2 finding 2: a CREATE that SUCCEEDS but whose subsequent poll
// times out must ALSO trigger the restore path -- round-1's fix only
// checked the create error and returned bare on a poll failure, leaving the
// original configuration unrecovered.
func TestRecreateVectorIndexWithOptionsRestoresOriginalOptionsIfPollFailsAfterASuccessfulCreate(t *testing.T) {
	var currentIsNewOptions bool
	var creates []string
	fake := &fakeConn{
		queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			if strings.HasPrefix(cypher, "CREATE VECTOR INDEX") {
				creates = append(creates, cypher)
				currentIsNewOptions = strings.Contains(cypher, "efRuntime:999")
				return nil, nil // the CREATE itself succeeds both times.
			}
			return nil, nil
		},
		indexesFunc: func(ctx context.Context, graphKey string) ([]indexStatus, error) {
			if currentIsNewOptions {
				// The new-options index never reports OPERATIONAL -> its
				// poll times out, even though its CREATE call succeeded.
				return nil, nil
			}
			// Seen by the pre-drop read AND the restore's poll.
			return []indexStatus{{
				Label: labelSubject, Status: "OPERATIONAL",
				Types: map[string][]string{propEmbedding: {"VECTOR"}},
				Options: map[string]interface{}{propEmbedding: map[string]interface{}{
					"dimension": int64(768), "M": int64(16), "efConstruction": int64(200), "efRuntime": int64(10),
				}},
			}}, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	adapter.config.RequestTimeout = 10 * time.Millisecond // fail the new-options poll fast, not after 30s.

	err := adapter.recreateVectorIndexWithOptions(context.Background(), "k", 768, hnswIndexOptions{EfRuntime: 999})
	if err == nil {
		t.Fatal("expected an error reporting the poll timeout, even though restore succeeded")
	}
	if !strings.Contains(err.Error(), "restored the original options") || !strings.Contains(err.Error(), "successfully") {
		t.Fatalf("a poll failure after a SUCCESSFUL create must still trigger the restore path, got: %v", err)
	}
	if len(creates) != 2 {
		t.Fatalf("expected exactly 2 CREATE attempts (new options, then restore), got %d: %v", len(creates), creates)
	}
	if !strings.Contains(creates[1], "efRuntime:10") {
		t.Fatalf("the restore's create should target the ORIGINAL options, got %q", creates[1])
	}
}

// The over-fetch formula, exactly: raw fetch size = (multiplier × limit) + 1.
func TestVectorSearchNodesWithOverFetchFormula(t *testing.T) {
	cases := []struct {
		multiplier, limit, wantFetchK int
	}{
		{1, 20, 21},
		{2, 20, 41},
		{4, 20, 81},
		{0, 20, 21}, // multiplier <= 0 falls back to 1, matching production.
	}
	for _, c := range cases {
		var gotK int
		fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
			gotK = extractFetchK(t, cypher)
			return nil, nil
		}}
		adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0}}, 0.55)
		_, _, err := adapter.vectorSearchNodesWithOverFetch(context.Background(), "k", "org", []float32{1, 0}, 0.55, c.limit, c.multiplier)
		if err != nil {
			t.Fatalf("vectorSearchNodesWithOverFetch: %v", err)
		}
		if gotK != c.wantFetchK {
			t.Fatalf("multiplier=%d limit=%d: fetchK = %d, want %d", c.multiplier, c.limit, gotK, c.wantFetchK)
		}
	}
}

// vectorSearchNodes (the production call site) must delegate with
// multiplier=1, rendering the byte-identical cypher it always sent.
func TestVectorSearchNodesDelegatesToOverFetchMultiplierOne(t *testing.T) {
	var gotCypher string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotCypher = cypher
		return nil, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0}}, 0.55)
	if _, _, err := adapter.vectorSearchNodes(context.Background(), "k", "org", []float32{1, 0}, 0.55, 20); err != nil {
		t.Fatalf("vectorSearchNodes: %v", err)
	}
	if !strings.Contains(gotCypher, "queryNodes('Subject', 'embedding', 21, vecf32($vec))") {
		t.Fatalf("cypher = %q, want a raw fetch of limit+1=21 (multiplier=1)", gotCypher)
	}
}

// Widening the raw pool (multiplier > 1) must never itself manufacture
// truncation: truncated is still derived purely from how many rows SURVIVE
// tau beyond the caller's limit, exactly as it was before CHAOS-3832.
func TestVectorSearchNodesWithOverFetchTruncationStillDerivedFromSurvivors(t *testing.T) {
	// Exactly `limit` survivors clearing tau: not truncated, regardless of
	// how large a raw pool the multiplier requested under the hood.
	rows := []row{
		{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p1", propLabel: "A"}}, "score": 0.0},
		{"node": &node{Properties: map[string]interface{}{propKind: "project", propCanonicalID: "p2", propLabel: "B"}}, "score": 0.0},
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return rows, nil
	}}
	adapter := vectorAdapter(t, fake, &stubEmbedder{vector: []float32{1, 0}}, 0.55)
	candidates, truncated, err := adapter.vectorSearchNodesWithOverFetch(context.Background(), "k", "org", []float32{1, 0}, 0.55, 2, 4)
	if err != nil {
		t.Fatalf("vectorSearchNodesWithOverFetch: %v", err)
	}
	if truncated {
		t.Fatal("exactly `limit` survivors must not report truncated, whatever the multiplier requested")
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(candidates))
	}
}
