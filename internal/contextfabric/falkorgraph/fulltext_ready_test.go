package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func fulltextIndexRow(status string) indexStatus {
	return indexStatus{
		Label: labelSubject, Properties: []string{propSearchText}, EntityType: "NODE", Status: status,
		Types: map[string][]string{propSearchText: {"FULLTEXT"}},
	}
}

type fulltextReadyScript struct {
	mu       sync.Mutex
	statuses []string
	polls    int
	queried  bool
	queryAt  int
	queryAts []int
}

func (s *fulltextReadyScript) fake(indexErr error) *fakeConn {
	return &fakeConn{
		indexesFunc: func(context.Context, string) ([]indexStatus, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if indexErr != nil {
				return nil, indexErr
			}
			status := s.statuses[len(s.statuses)-1]
			if s.polls < len(s.statuses) {
				status = s.statuses[s.polls]
			}
			s.polls++
			return []indexStatus{fulltextIndexRow(status)}, nil
		},
		queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.queried = true
			s.queryAt = s.polls
			s.queryAts = append(s.queryAts, s.polls)
			return nil, nil
		},
	}
}

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buf
}

func TestFulltextQueryWaitsForTheIndexToBeOperational(t *testing.T) {
	logs := captureSlog(t)
	script := &fulltextReadyScript{statuses: []string{"[Indexing] 2/2: UNDER CONSTRUCTION", "[Indexing] 2/2: UNDER CONSTRUCTION", "OPERATIONAL"}}
	adapter := newFakeAdapter(t, script.fake(nil))
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatalf("fulltextSearchNodes() error = %v", err)
	}
	if !script.queried {
		t.Fatal("the fulltext query never ran")
	}
	if script.queryAt != 3 {
		t.Fatalf("the query ran after %d index status reads, want 3 (two under construction, then operational)", script.queryAt)
	}
	if !strings.Contains(logs.String(), "fulltext index became operational") {
		t.Fatalf("the wait left no log line:\n%s", logs.String())
	}
}

func TestFulltextQueryDoesNotWaitOnAnOperationalIndex(t *testing.T) {
	script := &fulltextReadyScript{statuses: []string{"OPERATIONAL"}}
	adapter := newFakeAdapter(t, script.fake(nil))
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatal(err)
	}
	if script.queryAt != 1 {
		t.Fatalf("the query ran after %d status reads, want exactly 1", script.queryAt)
	}
}

func TestFulltextQueryRunsAnywayAndSaysSoWhenTheIndexNeverBecomesOperational(t *testing.T) {
	logs := captureSlog(t)
	script := &fulltextReadyScript{statuses: []string{"[Indexing] 1/2: UNDER CONSTRUCTION"}}
	adapter := newFakeAdapter(t, script.fake(nil))
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatal(err)
	}
	if !script.queried {
		t.Fatal("the query did not run after the wait bound")
	}
	for _, want := range []string{"level=WARN", "still not operational", "org_id=org-1", "index_status="} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the timeout log lacks %q:\n%s", want, logs.String())
		}
	}
}

func TestFulltextQueryRunsWhenTheIndexStatusCannotBeRead(t *testing.T) {
	logs := captureSlog(t)
	script := &fulltextReadyScript{}
	adapter := newFakeAdapter(t, script.fake(errors.New("db.indexes refused with a connection string")))
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatal(err)
	}
	if !script.queried {
		t.Fatal("the query did not run when the status read failed")
	}
	if !strings.Contains(logs.String(), "status unreadable") || strings.Contains(logs.String(), "connection string") {
		t.Fatalf("the unreadable-status log is missing or carries the error text:\n%s", logs.String())
	}
}

func TestFulltextQueryReadsTheStatusOnEveryQueryAndKeepsNoMemoOfAnEarlierOperational(t *testing.T) {
	script := &fulltextReadyScript{statuses: []string{"OPERATIONAL", "[Indexing] 3/3: UNDER CONSTRUCTION", "OPERATIONAL"}}
	adapter := newFakeAdapter(t, script.fake(nil))
	for i := 0; i < 2; i++ {
		if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(script.queryAts) != 2 || script.queryAts[0] != 1 || script.queryAts[1] != 3 {
		t.Fatalf("status reads before each query = %v, want [1 3]: the first query found the index operational, then the status reads under construction and the second query must wait: the status is read on every query, an earlier OPERATIONAL is not remembered", script.queryAts)
	}
}

func TestFulltextQueryStillHasTimeAfterAWaitUnderACallerDeadline(t *testing.T) {
	logs := captureSlog(t)
	var queryCtxErr error
	queried := false
	fake := &fakeConn{
		indexesFunc: func(context.Context, string) ([]indexStatus, error) {
			return []indexStatus{fulltextIndexRow("[Indexing] 1/2: UNDER CONSTRUCTION")}, nil
		},
		queryFunc: func(ctx context.Context, _, _ string, _ map[string]interface{}, _ bool) ([]row, error) {
			queried = true
			queryCtxErr = ctx.Err()
			return nil, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	began := time.Now()
	if _, _, err := adapter.fulltextSearchNodes(ctx, "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatalf("the query failed after the wait: %v", err)
	}
	if !queried || queryCtxErr != nil {
		t.Fatalf("queried=%v, context error at the query = %v: the wait used up the caller's deadline", queried, queryCtxErr)
	}
	if spent := time.Since(began); spent > 300*time.Millisecond {
		t.Fatalf("the wait took %v of a 400ms deadline, want at most half", spent)
	}
	if !strings.Contains(logs.String(), "still not operational") {
		t.Fatalf("the bound was reached without the WARN:\n%s", logs.String())
	}
}

func TestFulltextWaitBoundHoldsWhenTheStatusReadHangs(t *testing.T) {
	logs := captureSlog(t)
	queried := false
	fake := &fakeConn{
		indexesFunc: func(ctx context.Context, _ string) ([]indexStatus, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		queryFunc: func(context.Context, string, string, map[string]interface{}, bool) ([]row, error) {
			queried = true
			return nil, nil
		},
	}
	adapter := newFakeAdapter(t, fake)
	began := time.Now()
	if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
		t.Fatal(err)
	}
	spent := time.Since(began)
	if !queried {
		t.Fatal("the query did not run after a status read that never answered")
	}
	if spent < 900*time.Millisecond || spent > 1500*time.Millisecond {
		t.Fatalf("the wait took %v with a 1s request timeout, want about 1s (the status read is inside the bound)", spent)
	}
	if !strings.Contains(logs.String(), "still not operational") {
		t.Fatalf("the bound was reached without the WARN:\n%s", logs.String())
	}
}

func TestFulltextWaitIsBoundedByTheCallersDeadlineThroughTheRealClient(t *testing.T) {
	logs := captureSlog(t)
	api := delayedSDKAPI(t, 800*time.Millisecond)
	adapter, err := newWithAPI(Config{
		Addr: "delayed:6379", GraphPrefix: "acr-cf-ready", RequestTimeout: 10 * time.Second,
		MaxAttempts: 1, MaxResults: 25, PoolSize: 3, AllowInsecure: true,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	began := time.Now()
	adapter.awaitFulltextIndexOperational(ctx, "graph", "org-1")
	spent := time.Since(began)
	if spent > 330*time.Millisecond {
		t.Fatalf("the wait took %v of a 400ms caller deadline over a server that answers in 800ms, want at most half (about 200ms)", spent)
	}
	if ctx.Err() != nil {
		t.Fatalf("the wait used up the caller's deadline: %v", ctx.Err())
	}
	if !strings.Contains(logs.String(), "still not operational") {
		t.Fatalf("the bound was reached without the WARN:\n%s", logs.String())
	}
}
