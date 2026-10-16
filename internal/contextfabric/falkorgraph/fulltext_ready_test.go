package falkorgraph

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
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

func TestFulltextQueryWaitsAgainWhenTheIndexReturnsToConstructionAfterNewWrites(t *testing.T) {
	script := &fulltextReadyScript{statuses: []string{"OPERATIONAL", "[Indexing] 3/3: UNDER CONSTRUCTION", "OPERATIONAL"}}
	adapter := newFakeAdapter(t, script.fake(nil))
	for i := 0; i < 2; i++ {
		if _, _, err := adapter.fulltextSearchNodes(context.Background(), "graph", "org-1", "escalation", 10, temporalFilter{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(script.queryAts) != 2 || script.queryAts[0] != 1 || script.queryAts[1] != 3 {
		t.Fatalf("status reads before each query = %v, want [1 3]: the first query found the index operational, then new writes put it back under construction and the second query must wait again (no memo of an earlier OPERATIONAL)", script.queryAts)
	}
}
