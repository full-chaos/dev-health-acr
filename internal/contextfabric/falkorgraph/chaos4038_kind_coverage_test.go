package falkorgraph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/contextfabric/graphrank"
)

// TestKindScopedFulltextSearchNodes_BlankTermSkipsTheQuery mirrors
// fulltextSearchNodes' own len(terms)==0 early return: a term that
// tokenizes to nothing must never reach the backend at all -- there is
// nothing meaningful to search for.
func TestKindScopedFulltextSearchNodes_BlankTermSkipsTheQuery(t *testing.T) {
	t.Parallel()
	called := false
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		called = true
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, degraded, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "???", contextfabric.SubjectPullRequest, 5, temporalFilter{})
	if err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if called {
		t.Fatal("queryFunc was called, want no backend call for a term with no lexical content")
	}
	if len(candidates) != 0 || truncated || degraded {
		t.Fatalf("kindScopedFulltextSearchNodes() = (%#v, %v, %v), want all-empty/false", candidates, truncated, degraded)
	}
}

// TestKindScopedFulltextSearchNodes_ScopesTheQueryToExactlyOneKind proves
// the kind argument reaches runFulltextQuery's own kindFilter mechanism
// (queries.go) -- the SAME "AND node.kind = $kind" predicate CHAOS-3838's
// kind-scoped lexicon-expansion batches already use, applied here as a
// dedicated per-kind coverage query.
func TestKindScopedFulltextSearchNodes_ScopesTheQueryToExactlyOneKind(t *testing.T) {
	t.Parallel()
	var gotParams map[string]interface{}
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		gotParams = params
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	if _, _, _, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "outage", contextfabric.SubjectPullRequest, 5, temporalFilter{}); err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if gotParams == nil {
		t.Fatal("queryFunc was never called")
	}
	if got, _ := gotParams["kind"].(string); got != string(contextfabric.SubjectPullRequest) {
		t.Fatalf("query params[\"kind\"] = %q, want %q", got, contextfabric.SubjectPullRequest)
	}
}

// TestKindScopedFulltextSearchNodes_ReturnsMatchingCandidate proves a
// matching row surfaces as a CandidateNode of the requested kind.
func TestKindScopedFulltextSearchNodes_ReturnsMatchingCandidate(t *testing.T) {
	t.Parallel()
	fake := fixedRowsFulltextConn([]row{fulltextRow("pull_request", "pr_1", "Outage PR", "Outage PR", nil)})
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, degraded, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "outage", contextfabric.SubjectPullRequest, 5, temporalFilter{})
	if err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if truncated || degraded {
		t.Fatalf("kindScopedFulltextSearchNodes() truncated=%v degraded=%v, want both false", truncated, degraded)
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want exactly 1", candidates)
	}
	if got := contextfabric.SubjectKind(graphrank.StringAttribute(candidates[0].Attributes, "subject_kind")); got != contextfabric.SubjectPullRequest {
		t.Fatalf("candidates[0] kind = %q, want %q", got, contextfabric.SubjectPullRequest)
	}
	// codex CHAOS-4038 review, finding 2: runFulltextQuery itself never sets
	// Mechanism -- every other lexical caller (hybridSearchNodes, vector.go)
	// stamps it explicitly after the call. A candidate missing it here would
	// silently lose mechanism provenance/corroboration downstream.
	if candidates[0].Mechanism != contextfabric.MatchLexical {
		t.Fatalf("candidates[0].Mechanism = %q, want %q", candidates[0].Mechanism, contextfabric.MatchLexical)
	}
}

// TestKindScopedFulltextSearchNodes_TruncatesAtLimit proves the same
// limit+1 sentinel discipline every other fulltext caller uses: more rows
// than the caller's budget reports truncated=true and returns only `limit`
// candidates.
func TestKindScopedFulltextSearchNodes_TruncatesAtLimit(t *testing.T) {
	t.Parallel()
	rows := []row{
		fulltextRow("pull_request", "pr_1", "Outage PR 1", "Outage PR 1", nil),
		fulltextRow("pull_request", "pr_2", "Outage PR 2", "Outage PR 2", nil),
	}
	fake := fixedRowsFulltextConn(rows)
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, _, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "outage", contextfabric.SubjectPullRequest, 1, temporalFilter{})
	if err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false, want true -- 2 rows arrived against a limit of 1")
	}
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want exactly 1 (the limit), never the raw sentinel row", candidates)
	}
}

// TestKindScopedFulltextSearchNodes_LexiconExpansionFindsCandidateBaseQueryMissed
// is codex CHAOS-4038 review round 2's own regression (finding 2): the
// coverage floor must widen with the SAME domain lexicon
// (fulltextSearchNodesForResolution, queries.go) the ordinary per-term
// Search pass already uses, scoped to this call's own kind -- otherwise a
// candidate discoverable ONLY through a synonym (here "pr" -> "pull
// request", a kind-agnostic domainLexiconGroups entry) would be missed by
// the floor even though the ordinary pass would have found it. Mirrors
// chaos3890_lexicon_expansion_test.go's own base+expansion fake shape.
func TestKindScopedFulltextSearchNodes_LexiconExpansionFindsCandidateBaseQueryMissed(t *testing.T) {
	t.Parallel()
	baseRow := fulltextRow("pull_request", "pr_1", "PR base hit", "PR base hit", nil)
	expansionRow := fulltextRow("pull_request", "pr_2", "Pull request expansion hit", "Pull request expansion hit", nil)
	var gotKinds []string
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		query, _ := params["query"].(string)
		if k, ok := params["kind"].(string); ok {
			gotKinds = append(gotKinds, k)
		}
		switch {
		case query == "pr":
			return []row{baseRow}, nil
		case strings.Contains(query, "pull request"):
			return []row{expansionRow}, nil
		default:
			return nil, nil
		}
	}}
	adapter := newFakeAdapter(t, fake)

	candidates, truncated, degraded, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "pr", contextfabric.SubjectPullRequest, 10, temporalFilter{})
	if err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if truncated || degraded {
		t.Fatalf("kindScopedFulltextSearchNodes() truncated=%v degraded=%v, want both false -- plenty of budget", truncated, degraded)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v, want exactly 2 (base + lexicon-expansion)", candidates)
	}
	foundExpansion := false
	for _, c := range candidates {
		if graphrank.StringAttribute(c.Attributes, "canonical_id") == "pr_2" {
			foundExpansion = true
		}
		if c.Mechanism != contextfabric.MatchLexical {
			t.Fatalf("candidate %#v Mechanism = %q, want %q on every returned candidate", c, c.Mechanism, contextfabric.MatchLexical)
		}
	}
	if !foundExpansion {
		t.Fatalf("candidates = %#v, want the lexicon-expansion-only candidate present", candidates)
	}
	// Every query this call issued (base + expansion) must stay scoped to
	// the SAME kind -- the whole point of a kind-scoped coverage floor.
	for _, k := range gotKinds {
		if k != string(contextfabric.SubjectPullRequest) {
			t.Fatalf("gotKinds = %#v, want every query scoped to %q", gotKinds, contextfabric.SubjectPullRequest)
		}
	}
	if len(gotKinds) != 2 {
		t.Fatalf("gotKinds = %#v, want exactly 2 queries (base + one expansion batch)", gotKinds)
	}
}

// TestKindScopedFulltextSearchNodes_IrrelevantKindScopedLexiconGroupNeverWidens
// proves a lexicon group scoped to a DIFFERENT kind never triggers a second
// query when this call's own kind does not match it -- "repo"/"repository"
// targets ONLY contextfabric.SubjectRepository, so a pull_request-scoped
// call must stay a single round trip even when the term contains "repo".
func TestKindScopedFulltextSearchNodes_IrrelevantKindScopedLexiconGroupNeverWidens(t *testing.T) {
	t.Parallel()
	calls := 0
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		calls++
		return nil, nil
	}}
	adapter := newFakeAdapter(t, fake)

	if _, _, _, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "repo", contextfabric.SubjectPullRequest, 10, temporalFilter{}); err != nil {
		t.Fatalf("kindScopedFulltextSearchNodes() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("backend calls = %d, want exactly 1 -- \"repo\"'s lexicon group targets repository, irrelevant to a pull_request-scoped call", calls)
	}
}

// TestKindScopedFulltextSearchNodes_PropagatesBackendError proves a genuine
// query failure surfaces as an error, never silently downgraded to "found
// nothing".
func TestKindScopedFulltextSearchNodes_PropagatesBackendError(t *testing.T) {
	t.Parallel()
	fake := &fakeConn{queryFunc: func(ctx context.Context, key, cypher string, params map[string]interface{}, readOnly bool) ([]row, error) {
		return nil, errors.New("transient backend failure")
	}}
	adapter := newFakeAdapter(t, fake)

	if _, _, _, err := adapter.kindScopedFulltextSearchNodes(context.Background(), "k", "org-1", "outage", contextfabric.SubjectPullRequest, 5, temporalFilter{}); err == nil {
		t.Fatal("kindScopedFulltextSearchNodes() error = nil, want the backend failure propagated")
	}
}
