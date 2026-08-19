package graphrank

import (
	"context"
	"fmt"
	"testing"

	"github.com/full-chaos/dev-health-acr/internal/contextfabric"
	"github.com/full-chaos/dev-health-acr/internal/storage"
)

// CHAOS-3810. NodeCandidate's documented exact label/name match override
// ("Confidence to 1.0 regardless of Relevance") was unreachable on any real
// corpus: falkorgraph's fulltextSearchNodes caps EVERY candidate at
// fulltextRelevanceFloor once the result set truncates, and
// ResolveFromMergedCandidates' searchTruncated branch then forced the whole
// resolution ambiguous BEFORE the override could be read. With a 20k+ subject
// graph and MaxSubjectCandidates=10 every real search truncates, so nothing
// ever auto-committed -- including a candidate whose label was character-for-
// character the subject term.
//
// The three tests below pin the whole rule, not just the happy case:
// exactly one exact match commits, two exact matches still clarify, and a
// truncated set with no exact match still clarifies.
func exactMatchSearchResults(term string, count int) []CandidateNode {
	nodes := make([]CandidateNode, 0, count)
	// The exact match is deliberately NOT first and NOT the highest-scoring
	// row: on a truncated result set every row carries the same floor-capped
	// relevance anyway, so a fix that merely re-sorted by score would not
	// find it.
	for i := 0; i < count; i++ {
		label := fmt.Sprintf("%s Migration Plan %d", term, i)
		if i == count/2 {
			label = term
		}
		nodes = append(nodes, candidateNode(contextfabric.SubjectProject, fmt.Sprintf("project_%d", i), label, 0.35, "*"))
	}
	return nodes
}

func TestResolveSubjectsCommitsExactLabelMatchOnATruncatedSearch(t *testing.T) {
	t.Parallel()
	const term = "Ask Dev"
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{term: exactMatchSearchResults(term, 11)},
		searchTruncated: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 1 || resolution.Committed[0].Label != term {
		t.Fatalf("resolution.Committed = %#v, want the single exact label match committed despite truncation", resolution.Committed)
	}
	if resolution.ClarificationPrompt != "" {
		t.Fatalf("resolution.ClarificationPrompt = %q, want none once the exact match committed", resolution.ClarificationPrompt)
	}
}

func TestResolveSubjectsStaysAmbiguousWhenTwoCandidatesMatchTheTermExactly(t *testing.T) {
	t.Parallel()
	const term = "Ask Dev"
	nodes := exactMatchSearchResults(term, 11)
	// A second subject carrying the identical label: the term no longer
	// identifies ONE subject, so the override must not fire for either.
	nodes[0] = candidateNode(contextfabric.SubjectProject, "project_duplicate_label", term, 0.35, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{term: nodes},
		searchTruncated: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none when two subjects share the exact label", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for a genuinely ambiguous exact label")
	}
}

func TestResolveSubjectsStaysAmbiguousOnATruncatedSearchWithNoExactMatch(t *testing.T) {
	t.Parallel()
	const term = "Ask Dev"
	nodes := exactMatchSearchResults(term, 11)
	// Remove the one exact match: nothing here is character-for-character the
	// term, so the truncation rule still owns the decision.
	nodes[len(nodes)/2] = candidateNode(contextfabric.SubjectProject, "project_near", term+" Rollout", 0.35, "*")
	backend := &fakeGraphBackend{
		searchResults:   map[string][]CandidateNode{term: nodes},
		searchTruncated: true,
	}
	resolution, _, err := ResolveSubjects(context.Background(), storage.Principal{OrgID: "org_1"}, testRequest(), testInterpreted(term), backend.deps())
	if err != nil {
		t.Fatalf("ResolveSubjects() error = %v", err)
	}
	if len(resolution.Committed) != 0 {
		t.Fatalf("resolution.Committed = %#v, want none: a truncated set with no exact match must fail toward ambiguity", resolution.Committed)
	}
	if resolution.ClarificationPrompt == "" {
		t.Fatal("resolution.ClarificationPrompt is empty for a truncated, ambiguous resolution")
	}
}
