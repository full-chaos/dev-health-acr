package falkorgraph

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestChaos4348ExactNameCandidates_ScopesToOrgAndTheThreeKinds proves the
// query is org-parameter-bound and filters to exactly repository/project/
// team, never a fifth kind -- codex review item 7 (adapter test gap).
func TestChaos4348ExactNameCandidates_ScopesToOrgAndTheThreeKinds(t *testing.T) {
	t.Parallel()
	var gotParams map[string]interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotParams = params
		if !readOnly {
			t.Fatal("exact-name fetch must be read-only")
		}
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	if _, _, err := adapter.chaos4348ExactNameCandidates(context.Background(), "k", "org-1", temporalFilter{}); err != nil {
		t.Fatalf("chaos4348ExactNameCandidates() error = %v", err)
	}
	if gotParams == nil {
		t.Fatal("queryFunc was never called")
	}
	if got, _ := gotParams["org"].(string); got != "org-1" {
		t.Fatalf("query params[\"org\"] = %q, want %q", got, "org-1")
	}
	kinds, ok := gotParams["kinds"].([]string)
	if !ok {
		t.Fatalf("query params[\"kinds\"] = %#v, want []string", gotParams["kinds"])
	}
	want := []string{"repository", "project", "team"}
	if len(kinds) != len(want) {
		t.Fatalf("query params[\"kinds\"] = %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Fatalf("query params[\"kinds\"] = %v, want %v", kinds, want)
		}
	}
}

// TestChaos4348ExactNameCandidates_ReturnsEveryRowUntruncated proves the
// ordinary, well-under-the-cap path: every row surfaces as a candidate and
// truncated is false.
func TestChaos4348ExactNameCandidates_ReturnsEveryRowUntruncated(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return []row{
			fakeSubjectNodeRow("project", "project.v2:linear:a", "Project A"),
			fakeSubjectNodeRow("team", "team:b", "Team B"),
		}, nil
	}}
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, err := adapter.chaos4348ExactNameCandidates(context.Background(), "k", "org-1", temporalFilter{})
	if err != nil {
		t.Fatalf("chaos4348ExactNameCandidates() error = %v", err)
	}
	if truncated {
		t.Fatal("truncated = true, want false for a fetch well under the cap")
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v, want exactly 2", candidates)
	}
}

// TestChaos4348ExactNameCandidates_ReportsTruncationAndTrimsToTheCap is
// codex review HIGH item 2's own regression guard: a fetch that returns
// exactNameCandidateQueryLimit+1 rows (the over-fetch signal, mirroring
// runFulltextQuery's identical "ask for one more than the budget"
// discipline) must report truncated=true AND trim its own result back down
// to exactly the cap -- a caller must never see the extra probe row itself
// as a real candidate.
func TestChaos4348ExactNameCandidates_ReportsTruncationAndTrimsToTheCap(t *testing.T) {
	t.Parallel()
	rows := make([]row, 0, exactNameCandidateQueryLimit+1)
	for i := 0; i < exactNameCandidateQueryLimit+1; i++ {
		rows = append(rows, fakeSubjectNodeRow("repository", fmt.Sprintf("repository_%d", i), fmt.Sprintf("Repo %d", i)))
	}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return rows, nil
	}}
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, err := adapter.chaos4348ExactNameCandidates(context.Background(), "k", "org-1", temporalFilter{})
	if err != nil {
		t.Fatalf("chaos4348ExactNameCandidates() error = %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true when the fetch returned exactNameCandidateQueryLimit+1 rows")
	}
	if len(candidates) != exactNameCandidateQueryLimit {
		t.Fatalf("len(candidates) = %d, want exactly exactNameCandidateQueryLimit (%d), never the extra probe row", len(candidates), exactNameCandidateQueryLimit)
	}
}

// TestChaos4348ExactNameCandidates_PropagatesBackendError proves a backend
// failure is surfaced, not swallowed into an empty, falsely-successful
// result.
func TestChaos4348ExactNameCandidates_PropagatesBackendError(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("boom")
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, wantErr
	}}
	adapter := newFakeAdapter(t, fake)

	_, _, err := adapter.chaos4348ExactNameCandidates(context.Background(), "k", "org-1", temporalFilter{})
	if err == nil {
		t.Fatal("chaos4348ExactNameCandidates() error = nil, want a non-nil error")
	}
}
